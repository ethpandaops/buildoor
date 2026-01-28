// Package lifecycle provides builder lifecycle management (deposit, balance, exit).
package lifecycle

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/epbs"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/wallet"
)

// Manager orchestrates builder lifecycle operations.
type Manager struct {
	cfg          *builder.Config
	clClient     *beacon.Client
	chainSvc     chain.Service
	signer       *signer.BLSSigner
	wallet       *wallet.Wallet
	builderState *builder.BuilderState
	stateMu      sync.RWMutex
	depositSvc   *DepositService
	balanceSvc   *BalanceService
	exitSvc      *ExitService
	log          logrus.FieldLogger
	stopCh       chan struct{}
	wg           sync.WaitGroup
}

// NewManager creates a new lifecycle manager.
func NewManager(
	cfg *builder.Config,
	clClient *beacon.Client,
	chainSvc chain.Service,
	blsSigner *signer.BLSSigner,
	w *wallet.Wallet,
	log logrus.FieldLogger,
) (*Manager, error) {
	managerLog := log.WithField("component", "lifecycle-manager")

	m := &Manager{
		cfg:          cfg,
		clClient:     clClient,
		chainSvc:     chainSvc,
		signer:       blsSigner,
		wallet:       w,
		builderState: &builder.BuilderState{},
		log:          managerLog,
		stopCh:       make(chan struct{}),
	}

	// Initialize services
	depositSvc, err := NewDepositService(cfg, chainSvc, blsSigner, w, managerLog)
	if err != nil {
		return nil, fmt.Errorf("failed to create deposit service: %w", err)
	}

	m.depositSvc = depositSvc

	// Exit service
	m.exitSvc = NewExitService(clClient, chainSvc, blsSigner, managerLog)

	return m, nil
}

// Start starts the lifecycle manager.
func (m *Manager) Start(ctx context.Context) error {
	m.wg.Add(1)

	go m.runBalanceMonitor(ctx)

	m.log.Info("Lifecycle manager started")

	return nil
}

// Stop stops the lifecycle manager.
func (m *Manager) Stop() {
	close(m.stopCh)
	m.wg.Wait()

	m.log.Info("Lifecycle manager stopped")
}

// GetBuilderState returns the current builder state.
func (m *Manager) GetBuilderState() *builder.BuilderState {
	m.stateMu.RLock()
	defer m.stateMu.RUnlock()

	state := *m.builderState

	return &state
}

// GetWallet returns the wallet instance.
func (m *Manager) GetWallet() *wallet.Wallet {
	return m.wallet
}

// EnsureBuilderRegistered checks if builder is registered and deposits if needed.
func (m *Manager) EnsureBuilderRegistered(ctx context.Context) error {
	isRegistered, state, err := m.depositSvc.IsBuilderRegistered(ctx)
	if err != nil {
		return fmt.Errorf("failed to check builder registration: %w", err)
	}

	m.stateMu.Lock()
	m.builderState = state
	m.stateMu.Unlock()

	if isRegistered {
		m.log.WithFields(logrus.Fields{
			"builder_index": state.Index,
			"balance":       state.Balance,
		}).Info("Builder already registered")

		return nil
	}

	m.log.Info("Builder not registered, creating deposit")

	if err := m.depositSvc.CreateDeposit(ctx, m.cfg.DepositAmount); err != nil {
		return fmt.Errorf("failed to create deposit: %w", err)
	}

	// Wait for registration
	return m.WaitForRegistration(ctx, 5*time.Minute)
}

// CheckAndTopup checks balance and tops up if needed.
func (m *Manager) CheckAndTopup(ctx context.Context) error {
	if m.balanceSvc == nil {
		return nil
	}

	return m.balanceSvc.CheckAndTopup(ctx)
}

// InitiateExit initiates a voluntary exit.
func (m *Manager) InitiateExit(ctx context.Context) error {
	m.stateMu.RLock()
	builderIndex := m.builderState.Index
	m.stateMu.RUnlock()

	if builderIndex == 0 {
		return fmt.Errorf("builder not registered")
	}

	return m.exitSvc.CreateVoluntaryExit(ctx, builderIndex)
}

// WaitForRegistration waits for the builder to be registered.
func (m *Manager) WaitForRegistration(ctx context.Context, timeout time.Duration) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(m.chainSvc.GetChainSpec().SecondsPerSlot) // Check every slot
	defer ticker.Stop()

	pubkey := m.signer.PublicKey()

	for {
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("timeout waiting for registration: %w", timeoutCtx.Err())

		case <-ticker.C:
			// Refresh builders cache to pick up new registrations
			if err := m.chainSvc.RefreshBuilders(ctx); err != nil {
				m.log.WithError(err).Debug("Error refreshing builders")
				continue
			}

			info := m.chainSvc.GetBuilderByPubkey(pubkey)
			if info != nil {
				m.stateMu.Lock()
				m.builderState = &builder.BuilderState{
					Pubkey:            pubkey[:],
					Index:             info.Index,
					IsRegistered:      true,
					Balance:           info.Balance,
					DepositEpoch:      info.DepositEpoch,
					WithdrawableEpoch: info.WithdrawableEpoch,
				}
				m.stateMu.Unlock()

				m.log.WithField("builder_index", info.Index).Info("Builder registered")

				return nil
			}
		}
	}
}

// SetBidTracker sets the bid tracker for balance service.
func (m *Manager) SetBidTracker(tracker *epbs.BidTracker) {
	m.balanceSvc = NewBalanceService(m.cfg, m.clClient, m.depositSvc, tracker, m.log)
}

// runBalanceMonitor periodically checks and tops up balance.
func (m *Manager) runBalanceMonitor(ctx context.Context) {
	defer m.wg.Done()

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			if m.balanceSvc == nil {
				continue
			}

			if err := m.balanceSvc.CheckAndTopup(ctx); err != nil {
				m.log.WithError(err).Warn("Balance check/topup failed")
			}
		}
	}
}
