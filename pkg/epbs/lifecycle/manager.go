// Package lifecycle provides builder lifecycle management (deposit, balance, exit)
// as an ePBS sub-concern.
package lifecycle

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/epbs"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/wallet"
)

// LifecycleEvent represents a lifecycle action for UI logging.
type LifecycleEvent struct {
	Action  string // "deposit", "topup", "exit", "state_change", "waiting_gloas", "balance_topup"
	Message string // Human-readable description
	Status  string // "info", "success", "warning", "error"
}

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
	bidTracker   *epbs.BidTracker
	exitSvc      *ExitService
	log          logrus.FieldLogger
	stopCh       chan struct{}
	wg           sync.WaitGroup

	registrationCallback   func(index uint64)
	depositPendingCallback func()
	registrationDone       atomic.Bool
	enabled                atomic.Bool
	eventCallback          func(*LifecycleEvent)
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

// SetEnabled sets whether the lifecycle manager is actively managing the builder.
func (m *Manager) SetEnabled(enabled bool) {
	m.enabled.Store(enabled)
}

// IsEnabled returns whether the lifecycle manager is enabled.
func (m *Manager) IsEnabled() bool {
	return m.enabled.Load()
}

// SetRegistrationCallback sets the callback invoked when builder registration completes.
func (m *Manager) SetRegistrationCallback(cb func(index uint64)) {
	m.registrationCallback = cb
}

// SetDepositPendingCallback sets the callback invoked when a deposit is submitted.
func (m *Manager) SetDepositPendingCallback(cb func()) {
	m.depositPendingCallback = cb
}

// SetEventCallback sets the callback invoked when lifecycle events occur (for UI logging).
func (m *Manager) SetEventCallback(cb func(*LifecycleEvent)) {
	m.eventCallback = cb
}

// fireEvent sends a lifecycle event to the UI if a callback is registered.
func (m *Manager) fireEvent(action, message, status string) {
	if m.eventCallback != nil {
		m.eventCallback(&LifecycleEvent{
			Action:  action,
			Message: message,
			Status:  status,
		})
	}
}

// Start starts the lifecycle manager with async registration and balance monitoring.
func (m *Manager) Start(ctx context.Context) error {
	m.wg.Add(1)

	go m.runRegistrationAndMonitor(ctx)

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
// This is the synchronous version used by CLI commands (e.g. cmd/deposit.go).
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

		m.fireEvent("state_change", fmt.Sprintf("Builder already registered (index: %d, balance: %d gwei)", state.Index, state.Balance), "info")
		m.onRegistered(state.Index)

		return nil
	}

	m.log.Info("Builder not registered, creating deposit")
	m.fireEvent("deposit", fmt.Sprintf("Builder not registered, submitting deposit (%d gwei)", m.cfg.DepositAmount), "info")

	if m.depositPendingCallback != nil {
		m.depositPendingCallback()
	}

	if err := m.depositSvc.CreateDeposit(ctx, m.cfg.DepositAmount); err != nil {
		m.fireEvent("deposit", fmt.Sprintf("Deposit failed: %v", err), "error")

		return fmt.Errorf("failed to create deposit: %w", err)
	}

	m.fireEvent("deposit", "Deposit transaction confirmed, waiting for beacon chain inclusion", "success")

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

	m.fireEvent("exit", fmt.Sprintf("Submitting voluntary exit for builder index %d", builderIndex), "info")

	if err := m.exitSvc.CreateVoluntaryExit(ctx, builderIndex); err != nil {
		m.fireEvent("exit", fmt.Sprintf("Exit failed: %v", err), "error")

		return err
	}

	m.fireEvent("exit", fmt.Sprintf("Voluntary exit submitted for builder index %d", builderIndex), "success")

	return nil
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

		case <-m.stopCh:
			return fmt.Errorf("lifecycle manager stopped")

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
				m.fireEvent("state_change", fmt.Sprintf("Builder registered on beacon chain (index: %d, deposit epoch: %d)", info.Index, info.DepositEpoch), "success")
				m.onRegistered(info.Index)

				return nil
			}
		}
	}
}

// SetBidTracker sets the bid tracker for balance service and stores it for direct access.
func (m *Manager) SetBidTracker(tracker *epbs.BidTracker) {
	m.bidTracker = tracker
	m.balanceSvc = NewBalanceService(m.cfg, m.clClient, m.depositSvc, tracker, m.log)
}

// GetBidTracker returns the bid tracker.
func (m *Manager) GetBidTracker() *epbs.BidTracker {
	return m.bidTracker
}

// onRegistered marks registration as done and fires the callback.
func (m *Manager) onRegistered(index uint64) {
	m.registrationDone.Store(true)

	if m.registrationCallback != nil {
		m.registrationCallback(index)
	}
}

// runRegistrationAndMonitor handles async registration then balance monitoring.
// When disabled, it waits until re-enabled before proceeding.
func (m *Manager) runRegistrationAndMonitor(ctx context.Context) {
	defer m.wg.Done()

	// Wait until enabled before doing anything
	if !m.waitForEnabled(ctx) {
		return
	}

	// Step 1: Wait for Gloas fork activation if not yet active
	if !m.chainSvc.IsGloas() {
		m.log.Info("Waiting for Gloas fork activation before builder registration")
		m.fireEvent("waiting_gloas", "Waiting for Gloas fork activation before builder registration", "info")

		if !m.waitForGloas(ctx) {
			return // stopped or context cancelled
		}

		m.log.Info("Gloas fork activated, proceeding with registration")
		m.fireEvent("state_change", "Gloas fork activated, proceeding with builder registration", "success")
	}

	// Step 2: Ensure builder is registered (with retries)
	if !m.registrationDone.Load() {
		m.ensureRegisteredWithRetry(ctx)
	}

	// Step 3: Run balance monitor (checks enabled flag each tick)
	m.runBalanceMonitor(ctx)
}

// waitForEnabled waits until the manager is enabled or stopped.
func (m *Manager) waitForEnabled(ctx context.Context) bool {
	if m.enabled.Load() {
		return true
	}

	m.log.Info("Lifecycle manager waiting to be enabled")

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-m.stopCh:
			return false
		case <-ticker.C:
			if m.enabled.Load() {
				m.log.Info("Lifecycle manager enabled")
				m.fireEvent("state_change", "Lifecycle management enabled", "success")

				return true
			}
		}
	}
}

// waitForGloas waits for the Gloas fork to activate by subscribing to epoch stats.
func (m *Manager) waitForGloas(ctx context.Context) bool {
	epochSub := m.chainSvc.SubscribeEpochStats()
	defer epochSub.Unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-m.stopCh:
			return false
		case stats, ok := <-epochSub.Channel():
			if !ok {
				return false
			}

			if stats.IsGloas {
				return true
			}
		}
	}
}

// ensureRegisteredWithRetry attempts registration in a loop until success or stop.
func (m *Manager) ensureRegisteredWithRetry(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		default:
		}

		err := m.EnsureBuilderRegistered(ctx)
		if err == nil {
			return
		}

		m.log.WithError(err).Warn("Builder registration attempt failed, retrying in 30s")
		m.fireEvent("deposit", fmt.Sprintf("Registration attempt failed: %v, retrying in 30s", err), "warning")

		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-time.After(30 * time.Second):
		}
	}
}

// runBalanceMonitor periodically refreshes builder state and tops up balance.
func (m *Manager) runBalanceMonitor(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			// Always refresh builder state so the UI stays up to date
			m.refreshBuilderState()

			// Skip active lifecycle operations when disabled
			if !m.enabled.Load() || m.balanceSvc == nil {
				continue
			}

			needsTopup, amount, err := m.balanceSvc.NeedsTopup(ctx)
			if err != nil {
				m.log.WithError(err).Warn("Balance check failed")

				continue
			}

			if needsTopup {
				m.fireEvent("balance_topup", fmt.Sprintf("Balance below threshold, topping up %d gwei", amount), "info")

				if err := m.balanceSvc.CheckAndTopup(ctx); err != nil {
					m.log.WithError(err).Warn("Balance topup failed")
					m.fireEvent("balance_topup", fmt.Sprintf("Balance topup failed: %v", err), "error")
				} else {
					// Immediately reflect the topup in the live balance (no finalization delay)
					if tracker := m.GetBidTracker(); tracker != nil {
						tracker.AddDeposit(amount)
					}

					m.fireEvent("balance_topup", fmt.Sprintf("Balance topped up by %d gwei", amount), "success")
				}
			}
		}
	}
}

// refreshBuilderState updates the cached builder state from the chain service.
func (m *Manager) refreshBuilderState() {
	pubkey := m.signer.PublicKey()
	info := m.chainSvc.GetBuilderByPubkey(pubkey)

	if info == nil {
		return
	}

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

	// Reset balance adjustment — fresh state includes deposits and deductions
	if m.bidTracker != nil {
		m.bidTracker.ResetBalanceAdjustment()
	}
}
