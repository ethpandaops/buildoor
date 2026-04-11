package lifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/epbs"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// BalanceService handles balance monitoring and automatic top-ups.
type BalanceService struct {
	cfg        *builder.Config
	clClient   *beacon.Client
	depositSvc *DepositService
	bidTracker *epbs.BidTracker
	lastCheck  time.Time
	log        logrus.FieldLogger
}

// NewBalanceService creates a new balance service.
func NewBalanceService(
	cfg *builder.Config,
	clClient *beacon.Client,
	depositSvc *DepositService,
	bidTracker *epbs.BidTracker,
	log logrus.FieldLogger,
) *BalanceService {
	return &BalanceService{
		cfg:        cfg,
		clClient:   clClient,
		depositSvc: depositSvc,
		bidTracker: bidTracker,
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
	if s.bidTracker != nil {
		liveBalance += s.bidTracker.GetBalanceAdjustment()
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
func (s *BalanceService) NeedsTopup(ctx context.Context) (bool, uint64, error) {
	effectiveBalance, err := s.GetEffectiveBalance(ctx)
	if err != nil {
		return false, 0, err
	}

	threshold := s.cfg.TopupThreshold
	if effectiveBalance >= threshold {
		return false, 0, nil
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
