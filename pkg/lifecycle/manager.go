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

	"github.com/ethpandaops/buildoor/pkg/builder_keys"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/wallet"
	"github.com/ethpandaops/go-eth2-client/spec/version"
)

// earlyOnboardFinalizationMargin is the preferred minimum number of epochs before the
// Gloas fork to submit an early-onboarding deposit, leaving time for it to finalize so
// the fork transition converts it into a builder.
const earlyOnboardFinalizationMargin = 4

// minEarlyOnboardSlots is the hard floor of slots remaining before the Gloas fork for an
// early-onboarding deposit to be worthwhile. Closer than this we skip early onboarding
// and let the normal post-fork builder deposit register the builder instead.
const minEarlyOnboardSlots uint64 = 10

// LifecycleEvent represents a lifecycle action for UI logging.
type LifecycleEvent struct {
	Action  string // "deposit", "topup", "exit", "state_change", "waiting_gloas", "balance_topup"
	Message string // Human-readable description
	Status  string // "info", "success", "warning", "error"
}

// Manager orchestrates builder lifecycle operations.
type Manager struct {
	cfgSvc          *config.Service // settings source; one snapshot per pass
	clClient        *beacon.Client
	chainSvc        chain.Service
	registry        *builder_keys.Registry
	wallet          *wallet.Wallet
	builderState    *BuilderState
	stateMu         sync.RWMutex
	depositSvc      *DepositService
	earlyDepositSvc *EarlyDepositService
	balanceSvc      *BalanceService
	payments        *payload_bidder.PaymentTracker
	exitSvc         *ExitService
	log             logrus.FieldLogger
	stopCh          chan struct{}
	wg              sync.WaitGroup

	registrationCallback   func(index uint64)
	depositPendingCallback func()
	registrationDone       atomic.Bool
	enabled                atomic.Bool
	// exitNoticed dedupes the exited-builder warning event; re-armed when the
	// pubkey shows up unexited again (fresh registration after registry reuse).
	exitNoticed atomic.Bool
	// walletUnderfunded dedupes the "fund the wallet" event; re-armed once the
	// wallet can cover a deposit again.
	walletUnderfunded atomic.Bool
	eventCallback     func(*LifecycleEvent)

	// poke requests an immediate reconcile pass (cap 1).
	poke chan struct{}
}

// NewManager creates a new lifecycle manager.
func NewManager(
	cfgSvc *config.Service,
	clClient *beacon.Client,
	chainSvc chain.Service,
	registry *builder_keys.Registry,
	w *wallet.Wallet,
	log logrus.FieldLogger,
) (*Manager, error) {
	managerLog := log.WithField("component", "lifecycle-manager")

	m := &Manager{
		cfgSvc:       cfgSvc,
		clClient:     clClient,
		chainSvc:     chainSvc,
		registry:     registry,
		wallet:       w,
		builderState: &BuilderState{},
		log:          managerLog,
		stopCh:       make(chan struct{}),
		poke:         make(chan struct{}, 1),
	}

	// Initialize services
	depositSvc, err := NewDepositService(cfgSvc, chainSvc, w, managerLog)
	if err != nil {
		return nil, fmt.Errorf("failed to create deposit service: %w", err)
	}

	m.depositSvc = depositSvc

	// Early deposit service (regular validator deposit contract, used to onboard the
	// builder before the Gloas fork so there is no Builder-API-to-Gloas coverage gap).
	earlyDepositSvc, err := NewEarlyDepositService(cfgSvc, chainSvc, w, managerLog)
	if err != nil {
		return nil, fmt.Errorf("failed to create early deposit service: %w", err)
	}

	m.earlyDepositSvc = earlyDepositSvc

	// Exit service (builder exit system contract, sent from the funding wallet)
	m.exitSvc = NewExitService(chainSvc, w, managerLog)

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

	go m.runReconciler(ctx)

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
func (m *Manager) GetBuilderState() *BuilderState {
	m.stateMu.RLock()
	defer m.stateMu.RUnlock()

	state := *m.builderState

	return &state
}

// Registry returns the managed builder key set.
func (m *Manager) Registry() *builder_keys.Registry {
	return m.registry
}

// GetWallet returns the wallet instance.
func (m *Manager) GetWallet() *wallet.Wallet {
	return m.wallet
}

// EnsureBuilderRegistered checks whether the given builder key is registered and
// deposits if needed. This is the synchronous version used by CLI commands
// (e.g. cmd/deposit.go) and the lifecycle API.
func (m *Manager) EnsureBuilderRegistered(ctx context.Context, key *builder_keys.Key) error {
	isRegistered, state, err := m.depositSvc.IsBuilderRegistered(key)
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

		if state.WithdrawableEpoch != chain.FarFutureEpoch {
			m.noticeExitOnce(state.WithdrawableEpoch)
		}

		return nil
	}

	m.log.Info("Builder not registered, creating deposit")
	m.fireEvent("deposit", fmt.Sprintf("Builder not registered, submitting deposit (%d gwei)", m.cfgSvc.Current().DepositAmount), "info")

	if m.depositPendingCallback != nil {
		m.depositPendingCallback()
	}

	if err := m.depositSvc.CreateDeposit(ctx, key, m.cfgSvc.Current().DepositAmount); err != nil {
		if isDepositDeferred(err) {
			// Fee too high or contract not active yet — delay, don't treat as failure.
			m.fireEvent("deposit", fmt.Sprintf("Deposit deferred: %v", err), "info")
		} else {
			m.fireEvent("deposit", fmt.Sprintf("Deposit failed: %v", err), "error")
		}

		return fmt.Errorf("failed to create deposit: %w", err)
	}

	m.fireEvent("deposit", "Deposit transaction confirmed, waiting for beacon chain inclusion", "success")

	// Wait for registration
	return m.WaitForRegistration(ctx, key, 5*time.Minute)
}

// CheckAndTopup tops the given key up when its balance is below the threshold.
func (m *Manager) CheckAndTopup(ctx context.Context, key *builder_keys.Key) error {
	if m.balanceSvc == nil {
		return nil
	}

	amount, err := m.balanceSvc.CheckAndTopup(ctx, key)
	if err != nil {
		return err
	}

	if amount > 0 {
		if tracker := m.GetPaymentTracker(); tracker != nil {
			tracker.AddDeposit(key.KeyIndex(), amount)
		}
	}

	return nil
}

// TopupKey submits a top-up deposit for the key regardless of its current
// balance — the operator asked for it explicitly. amountGwei of 0 uses the
// configured top-up amount.
func (m *Manager) TopupKey(ctx context.Context, key *builder_keys.Key, amountGwei uint64) error {
	if amountGwei == 0 {
		amountGwei = m.cfgSvc.Current().TopupAmount
	}

	if err := m.depositSvc.CreateTopup(ctx, key, amountGwei); err != nil {
		return err
	}

	m.registry.MarkToppedUp(key.KeyIndex(), m.chainSvc.GetCurrentEpoch())

	if tracker := m.GetPaymentTracker(); tracker != nil {
		tracker.AddDeposit(key.KeyIndex(), amountGwei)
	}

	m.fireEvent("balance_topup", fmt.Sprintf(
		"Key #%d topped up by %d gwei", key.KeyIndex(), amountGwei), "success")

	return nil
}

// InitiateExit submits a builder exit request for the given key via the builder
// exit system contract.
func (m *Manager) InitiateExit(ctx context.Context, key *builder_keys.Key) error {
	// Check the live chain entry, not a cached snapshot: an already-exited builder
	// fails is_active_builder on the beacon side, so a second request only wastes
	// the queue fee.
	info := m.chainSvc.GetBuilderByPubkey(key.Pubkey())
	if info == nil {
		return fmt.Errorf("builder key #%d is not registered", key.KeyIndex())
	}

	if chain.HasBuilderExited(info) {
		return fmt.Errorf("builder exit already initiated (withdrawable epoch %d)", info.WithdrawableEpoch)
	}

	// The beacon chain silently ignores exit requests while the builder has pending
	// payments (get_pending_balance_to_withdraw_for_builder != 0) — the transaction
	// would confirm but the exit never happen.
	if info.PendingPayments > 0 {
		return fmt.Errorf("builder key #%d has %d gwei in pending payments; the exit request would be ignored on chain — retry after they settle",
			key.KeyIndex(), info.PendingPayments)
	}

	m.fireEvent("exit", fmt.Sprintf("Submitting exit for builder key #%d (builder index %d)",
		key.KeyIndex(), info.Index), "info")

	if err := m.exitSvc.CreateExit(ctx, key); err != nil {
		m.fireEvent("exit", fmt.Sprintf("Exit for key #%d failed: %v", key.KeyIndex(), err), "error")

		return err
	}

	m.registry.MarkExitSubmitted(key.KeyIndex())
	m.fireEvent("exit", fmt.Sprintf("Builder key #%d exit submitted (builder index %d)",
		key.KeyIndex(), info.Index), "success")

	return nil
}

// WaitForRegistration waits for the given builder key to be registered.
func (m *Manager) WaitForRegistration(
	ctx context.Context, key *builder_keys.Key, timeout time.Duration,
) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(m.chainSvc.GetChainSpec().SecondsPerSlot) // Check every slot
	defer ticker.Stop()

	pubkey := key.Pubkey()

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
				m.builderState = &BuilderState{
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

// SetPaymentTracker sets the shared payment tracker for the balance service and
// stores it for direct access.
func (m *Manager) SetPaymentTracker(payments *payload_bidder.PaymentTracker) {
	m.payments = payments
	m.balanceSvc = NewBalanceService(m.cfgSvc, m.chainSvc, m.registry, m.depositSvc, m.log)
}

// GetPaymentTracker returns the shared payment tracker.
func (m *Manager) GetPaymentTracker() *payload_bidder.PaymentTracker {
	return m.payments
}

// onRegistered marks registration as done and fires the callback.
func (m *Manager) onRegistered(index uint64) {
	m.registrationDone.Store(true)

	if m.registrationCallback != nil {
		m.registrationCallback(index)
	}
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

// waitForGloasState blocks until the chain service has loaded a Gloas (or later)
// beacon state — the first EpochStats from which the on-chain builder set is
// available. It logs/fires a waiting event only when it actually has to wait.
// Returns false if the context is cancelled or the manager is stopped.
func (m *Manager) waitForGloasState(ctx context.Context) bool {
	// Subscribe before reading the current stats so the transition can't slip
	// through between the check below and the subscription.
	epochSub := m.chainSvc.SubscribeEpochStats()
	defer epochSub.Unsubscribe()

	if stats := m.chainSvc.GetCurrentEpochStats(); stats != nil && stats.Version >= version.DataVersionGloas {
		return true
	}

	m.log.Info("Waiting for first Gloas beacon state before builder registration")
	m.fireEvent("waiting_gloas", "Waiting for Gloas state before builder registration", "info")

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

			if stats.Version >= version.DataVersionGloas {
				m.log.Info("Gloas state loaded, proceeding with builder registration")
				m.fireEvent("state_change", "Gloas state loaded, proceeding with builder registration", "success")

				return true
			}
		}
	}
}

// noticeExitOnce logs and fires the exited-builder warning the first time an
// initiated exit is seen on chain: the key can never be reactivated, so top-ups
// stay disabled until the pubkey leaves the builder registry.
func (m *Manager) noticeExitOnce(withdrawableEpoch uint64) {
	if !m.exitNoticed.CompareAndSwap(false, true) {
		return
	}

	m.log.WithField("withdrawable_epoch", withdrawableEpoch).
		Warn("Builder exit initiated; top-ups disabled (an exited builder cannot be reactivated)")
	m.fireEvent("state_change",
		fmt.Sprintf(
			"Builder exit initiated (withdrawable epoch %d). The key cannot be reactivated — deposits would be withdrawn back to the wallet, so top-ups are disabled until the pubkey leaves the builder set.",
			withdrawableEpoch),
		"warning")
}

// refreshBuilderState updates the cached builder state from the chain service.
func (m *Manager) refreshBuilderState() {
	pubkey := m.registry.Primary().Pubkey()
	info := m.chainSvc.GetBuilderByPubkey(pubkey)

	if info == nil {
		return
	}

	if chain.HasBuilderExited(info) {
		m.noticeExitOnce(info.WithdrawableEpoch)
	} else {
		// The pubkey shows up unexited again (fresh registration after the old
		// entry left the registry) — re-arm the warning for a future exit.
		m.exitNoticed.Store(false)
	}

	m.stateMu.Lock()
	m.builderState = &BuilderState{
		Pubkey:            pubkey[:],
		Index:             info.Index,
		IsRegistered:      true,
		Balance:           info.Balance,
		DepositEpoch:      info.DepositEpoch,
		WithdrawableEpoch: info.WithdrawableEpoch,
	}
	m.stateMu.Unlock()
}
