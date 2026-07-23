package lifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// topupCooldownEpochs suppresses further top-ups after one is submitted, giving
// a builder deposit time to land before the low balance triggers another (a
// builder deposit takes several epochs through the queue + fee-limit delay).
const topupCooldownEpochs phase0.Epoch = 8

// BalanceService handles balance monitoring and automatic top-ups.
type BalanceService struct {
	cfg        *config.Config
	clClient   *beacon.Client
	depositSvc *DepositService
	payments   *payload_bidder.PaymentTracker
	lastCheck  time.Time
	// lastTopupEpoch is the epoch of the most recent submitted top-up; 0 means
	// none. Guards the cooldown so an in-flight deposit is not duplicated.
	lastTopupEpoch phase0.Epoch
	log            logrus.FieldLogger
}

// NewBalanceService creates a new balance service.
func NewBalanceService(
	cfg *config.Config,
	clClient *beacon.Client,
	depositSvc *DepositService,
	payments *payload_bidder.PaymentTracker,
	log logrus.FieldLogger,
) *BalanceService {
	return &BalanceService{
		cfg:        cfg,
		clClient:   clClient,
		depositSvc: depositSvc,
		payments:   payments,
		log:        log.WithField("component", "balance-service"),
	}
}

// GetCurrentBalance returns the builder's current balance from the beacon state.
func (s *BalanceService) GetCurrentBalance(ctx context.Context) (uint64, error) {
	isRegistered, state, err := s.depositSvc.IsBuilderRegistered(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to check registration: %w", err)
	}

	if !isRegistered {
		return 0, fmt.Errorf("builder not registered")
	}

	return state.Balance, nil
}

// GetEffectiveBalance returns the live balance minus pending payments.
// Live balance = chain state balance + local adjustments (topups, revealed bid deductions).
// Pending payments = from chain state's BuilderPendingPayments (ground truth, survives restarts).
func (s *BalanceService) GetEffectiveBalance(ctx context.Context) (uint64, error) {
	isRegistered, state, err := s.depositSvc.IsBuilderRegistered(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to check registration: %w", err)
	}

	if !isRegistered {
		return 0, fmt.Errorf("builder not registered")
	}

	liveBalance := int64(state.Balance)

	// Apply local adjustments (topups add, revealed bids subtract since last state refresh)
	if s.payments != nil {
		liveBalance += s.payments.GetBalanceAdjustment()
	}

	if liveBalance < 0 {
		liveBalance = 0
	}

	// Get pending payments from chain state (ground truth from beacon state)
	builderInfo := s.depositSvc.chainSvc.GetBuilderByPubkey(s.depositSvc.signer.PublicKey())
	if builderInfo != nil && builderInfo.PendingPayments > 0 {
		effective := uint64(liveBalance)
		if builderInfo.PendingPayments >= effective {
			return 0, nil
		}

		return effective - builderInfo.PendingPayments, nil
	}

	return uint64(liveBalance), nil
}

// NeedsTopup checks if a top-up is needed and returns the required amount.
// It returns ErrBuilderExited for a builder whose exit has been initiated: after the
// sweep zeroes the balance a top-up would otherwise trigger every cooldown, cycling
// funds wallet -> exited entry -> (64 epochs locked) -> wallet forever.
func (s *BalanceService) NeedsTopup(ctx context.Context) (bool, uint64, error) {
	if chain.HasBuilderExited(s.depositSvc.chainSvc.GetBuilderByPubkey(s.depositSvc.signer.PublicKey())) {
		return false, 0, ErrBuilderExited
	}

	effectiveBalance, err := s.GetEffectiveBalance(ctx)
	if err != nil {
		return false, 0, err
	}

	threshold := s.cfg.TopupThreshold
	if effectiveBalance >= threshold {
		return false, 0, nil
	}

	// Hold off while a recent top-up is still expected to land, so the low
	// balance does not trigger duplicate deposits before the queued one arrives.
	if s.lastTopupEpoch != 0 {
		currentEpoch := s.depositSvc.chainSvc.GetCurrentEpoch()
		if currentEpoch < s.lastTopupEpoch+topupCooldownEpochs {
			return false, 0, nil
		}
	}

	topupAmount := s.cfg.TopupAmount
	if topupAmount == 0 {
		topupAmount = threshold
	}

	return true, topupAmount, nil
}

// CheckAndTopup checks the balance and performs a top-up if needed.
func (s *BalanceService) CheckAndTopup(ctx context.Context) error {
	needsTopup, amount, err := s.NeedsTopup(ctx)
	if err != nil {
		return fmt.Errorf("failed to check if topup needed: %w", err)
	}

	if !needsTopup {
		return nil
	}

	s.log.WithFields(logrus.Fields{
		"amount_gwei": amount,
	}).Info("Balance below threshold, topping up")

	if err := s.depositSvc.CreateTopup(ctx, amount); err != nil {
		return fmt.Errorf("failed to create topup: %w", err)
	}

	s.lastCheck = time.Now()
	s.lastTopupEpoch = s.depositSvc.chainSvc.GetCurrentEpoch()

	return nil
}

// RunBalanceMonitor runs a periodic balance check loop.
func (s *BalanceService) RunBalanceMonitor(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.CheckAndTopup(ctx); err != nil {
				s.log.WithError(err).Warn("Balance check failed")
			}
		}
	}
}
