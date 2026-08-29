package lifecycle

import (
	"context"
	"fmt"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder_keys"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
)

// topupCooldownEpochs suppresses further top-ups for a key after one is
// submitted, giving a builder deposit time to land before the low balance
// triggers another (a builder deposit takes several epochs through the queue +
// fee-limit delay).
const topupCooldownEpochs phase0.Epoch = 8

// BalanceService monitors builder key balances and performs automatic top-ups.
// Balances come from the key registry, which resolves them from the same epoch
// snapshot for the whole key set, so monitoring hundreds of keys costs one pass
// rather than one beacon query per key.
type BalanceService struct {
	cfgSvc     *config.Service // settings source; one snapshot per check
	chainSvc   chain.Service
	registry   *builder_keys.Registry
	depositSvc *DepositService
	log        logrus.FieldLogger
}

// NewBalanceService creates a new balance service.
func NewBalanceService(
	cfgSvc *config.Service,
	chainSvc chain.Service,
	registry *builder_keys.Registry,
	depositSvc *DepositService,
	log logrus.FieldLogger,
) *BalanceService {
	return &BalanceService{
		cfgSvc:     cfgSvc,
		chainSvc:   chainSvc,
		registry:   registry,
		depositSvc: depositSvc,
		log:        log.WithField("component", "balance-service"),
	}
}

// GetEffectiveBalance returns the key's spendable balance: the chain balance
// plus local adjustments (top-ups credited, revealed payments debited) minus
// the pending payments the beacon state still owes out of it.
func (s *BalanceService) GetEffectiveBalance(key *builder_keys.Key) (uint64, error) {
	state := key.State()
	if !state.HasBuilderIndex {
		return 0, fmt.Errorf("builder key %s not registered", key)
	}

	return s.registry.EffectiveBalance(key.KeyIndex()), nil
}

// NeedsTopup checks whether a key needs a top-up and returns the required amount.
// It returns ErrBuilderExited for a key whose exit has been initiated: after the
// sweep zeroes the balance a top-up would otherwise trigger every cooldown,
// cycling funds wallet -> exited entry -> (64 epochs locked) -> wallet forever.
func (s *BalanceService) NeedsTopup(key *builder_keys.Key) (bool, uint64, error) {
	state := key.State()

	switch state.Status {
	case builder_keys.StatusExiting, builder_keys.StatusExited:
		return false, 0, ErrBuilderExited
	case builder_keys.StatusPending, builder_keys.StatusActive:
		// Registered: a top-up can land.
	default:
		return false, 0, nil
	}

	cfg := s.cfgSvc.Current()

	threshold := cfg.TopupThreshold
	if s.registry.EffectiveBalance(key.KeyIndex()) >= threshold {
		return false, 0, nil
	}

	// Hold off while a recent top-up is still expected to land, so the low
	// balance does not trigger duplicate deposits before the queued one arrives.
	if state.LastTopupEpoch != 0 {
		if s.chainSvc.GetCurrentEpoch() < state.LastTopupEpoch+topupCooldownEpochs {
			return false, 0, nil
		}
	}

	topupAmount := cfg.TopupAmount
	if topupAmount == 0 {
		topupAmount = threshold
	}

	return true, topupAmount, nil
}

// CheckAndTopup tops the key up when its balance is below the threshold. It
// returns the amount deposited in gwei, or 0 when no top-up was needed — the
// caller credits exactly that to the live balance rather than guessing it.
func (s *BalanceService) CheckAndTopup(ctx context.Context, key *builder_keys.Key) (uint64, error) {
	needsTopup, amount, err := s.NeedsTopup(key)
	if err != nil {
		return 0, fmt.Errorf("failed to check if topup needed: %w", err)
	}

	if !needsTopup {
		return 0, nil
	}

	s.log.WithFields(logrus.Fields{
		"key":         key.String(),
		"amount_gwei": amount,
	}).Info("Balance below threshold, topping up")

	if err := s.depositSvc.CreateTopup(ctx, key, amount); err != nil {
		return 0, fmt.Errorf("failed to create topup: %w", err)
	}

	s.registry.MarkToppedUp(key.KeyIndex(), s.chainSvc.GetCurrentEpoch())

	return amount, nil
}
