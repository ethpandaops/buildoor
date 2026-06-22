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

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/epbs"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/wallet"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
)

// earlyOnboardFinalizationMargin is the preferred minimum number of epochs before the
// Gloas fork to submit an early-onboarding deposit, leaving time for it to finalize so
// the fork transition converts it into a builder.
const earlyOnboardFinalizationMargin = 4

// LifecycleEvent represents a lifecycle action for UI logging.
type LifecycleEvent struct {
	Action  string // "deposit", "topup", "exit", "state_change", "waiting_gloas", "balance_topup"
	Message string // Human-readable description
	Status  string // "info", "success", "warning", "error"
}

// Manager orchestrates builder lifecycle operations.
type Manager struct {
	cfg             *config.Config
	clClient        *beacon.Client
	chainSvc        chain.Service
	signer          *signer.BLSSigner
	wallet          *wallet.Wallet
	builderState    *BuilderState
	stateMu         sync.RWMutex
	depositSvc      *DepositService
	earlyDepositSvc *EarlyDepositService
	balanceSvc      *BalanceService
	bidTracker      *epbs.BidTracker
	exitSvc         *ExitService
	log             logrus.FieldLogger
	stopCh          chan struct{}
	wg              sync.WaitGroup

	registrationCallback   func(index uint64)
	depositPendingCallback func()
	registrationDone       atomic.Bool
	enabled                atomic.Bool
	eventCallback          func(*LifecycleEvent)
}

// NewManager creates a new lifecycle manager.
func NewManager(
	cfg *config.Config,
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
		builderState: &BuilderState{},
		log:          managerLog,
		stopCh:       make(chan struct{}),
	}

	// Initialize services
	depositSvc, err := NewDepositService(cfg, chainSvc, blsSigner, w, managerLog)
	if err != nil {
		return nil, fmt.Errorf("failed to create deposit service: %w", err)
	}

	m.depositSvc = depositSvc

	// Early deposit service (regular validator deposit contract, used to onboard the
	// builder before the Gloas fork so there is no Builder-API-to-Gloas coverage gap).
	earlyDepositSvc, err := NewEarlyDepositService(cfg, chainSvc, blsSigner, w, managerLog)
	if err != nil {
		return nil, fmt.Errorf("failed to create early deposit service: %w", err)
	}

	m.earlyDepositSvc = earlyDepositSvc

	// Exit service (builder exit system contract, sent from the funding wallet)
	m.exitSvc = NewExitService(chainSvc, blsSigner, w, managerLog)

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
func (m *Manager) GetBuilderState() *BuilderState {
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
	return m.WaitForRegistration(ctx, 5*time.Minute)
}

// CheckAndTopup checks balance and tops up if needed.
func (m *Manager) CheckAndTopup(ctx context.Context) error {
	if m.balanceSvc == nil {
		return nil
	}

	return m.balanceSvc.CheckAndTopup(ctx)
}

// InitiateExit submits a builder exit request via the builder exit system contract.
func (m *Manager) InitiateExit(ctx context.Context) error {
	m.stateMu.RLock()
	builderIndex := m.builderState.Index
	m.stateMu.RUnlock()

	if builderIndex == 0 {
		return fmt.Errorf("builder not registered")
	}

	m.fireEvent("exit", fmt.Sprintf("Submitting builder exit for builder index %d", builderIndex), "info")

	if err := m.exitSvc.CreateExit(ctx); err != nil {
		m.fireEvent("exit", fmt.Sprintf("Exit failed: %v", err), "error")

		return err
	}

	m.fireEvent("exit", fmt.Sprintf("Builder exit submitted for builder index %d", builderIndex), "success")

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

	// Step 0: Try to onboard the builder before the Gloas fork via the regular deposit
	// contract (no-op when not applicable), so coverage is continuous across the fork.
	m.maybeEarlyOnboard(ctx)

	// Step 1: Wait until the chain has loaded a Gloas (or later) beacon state.
	// The on-chain builder set is available from the first Gloas EpochStats; the
	// fork being active by epoch is not sufficient — the state must be fetched
	// and cached before we can tell whether this builder is already registered
	// (otherwise we'd read an empty set and deposit again unnecessarily).
	if !m.waitForGloasState(ctx) {
		return // stopped or context cancelled
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

// maybeEarlyOnboard onboards the builder before the Gloas fork via the regular validator
// deposit contract, so there is no coverage gap between the Fulu Builder-API range and
// Gloas. It returns immediately (no-op) when early onboarding does not apply: the early
// deposit service is unavailable, Gloas is not scheduled, no deposit contract is known,
// Gloas is already active, or the builder is already registered.
//
// When applicable it re-evaluates the deposit timing once per epoch until it submits the
// deposit (and waits for registration), the builder gets registered, the fork is reached,
// or the manager stops.
func (m *Manager) maybeEarlyOnboard(ctx context.Context) {
	if m.earlyDepositSvc == nil {
		return
	}

	spec := m.chainSvc.GetChainSpec()
	if !spec.IsForkScheduled(version.DataVersionGloas) || spec.DepositContractAddress == nil {
		return
	}

	if spec.IsForkActive(version.DataVersionGloas, m.chainSvc.GetCurrentEpoch()) {
		return
	}

	if m.chainSvc.GetBuilderByPubkey(m.signer.PublicKey()) != nil {
		return
	}

	forkEpoch := spec.GetForkEpoch(version.DataVersionGloas)

	m.log.WithField("gloas_fork_epoch", forkEpoch).Info("Gloas scheduled, evaluating early builder onboarding")
	m.fireEvent("early_onboard", fmt.Sprintf("Gloas fork at epoch %d, preparing early builder onboarding", forkEpoch), "info")

	// Subscribe before the first evaluation so an epoch transition can't slip through
	// between a "wait" decision and the subscription.
	epochSub := m.chainSvc.SubscribeEpochStats()
	defer epochSub.Unsubscribe()

	for {
		if m.tryEarlyOnboardOnce(ctx, forkEpoch) {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case _, ok := <-epochSub.Channel():
			if !ok {
				return
			}
		}
	}
}

// tryEarlyOnboardOnce performs one early-onboarding evaluation. It returns true when the
// early-onboarding phase is complete and the loop should stop: the builder is registered,
// the deposit was submitted (and we waited for registration), or the fork was reached
// without onboarding (handed off to the normal post-fork flow).
func (m *Manager) tryEarlyOnboardOnce(ctx context.Context, forkEpoch phase0.Epoch) bool {
	if !m.enabled.Load() {
		return false // paused; keep waiting until re-enabled
	}

	if info := m.chainSvc.GetBuilderByPubkey(m.signer.PublicKey()); info != nil {
		m.onRegistered(info.Index)

		return true
	}

	currentEpoch := m.chainSvc.GetCurrentEpoch()
	if currentEpoch >= forkEpoch {
		// Fork reached without onboarding — let the normal post-fork flow take over.
		return true
	}

	// Restart safety: if our deposit is already in the pending queue, don't submit again;
	// just wait for the fork transition to convert it into a builder.
	if m.earlyDepositSvc.HasPendingDeposit() {
		m.log.Info("Early builder deposit already pending, waiting for registration")
		m.fireEvent("early_onboard", "Early deposit already pending, waiting for registration", "info")
		m.waitForEarlyRegistration(ctx, forkEpoch, currentEpoch)

		return true
	}

	epochsUntilFork := uint64(forkEpoch - currentEpoch)
	amount := m.cfg.DepositAmount

	// Two deposit windows (per design): at least earlyOnboardFinalizationMargin epochs
	// before the fork if the pending-deposit queue is long enough to shield our deposit
	// from being processed before the fork; otherwise in the epoch directly before the
	// fork (a fresh back-of-queue deposit is guaranteed to survive a single transition).
	shouldDeposit := epochsUntilFork == 1 ||
		(epochsUntilFork >= earlyOnboardFinalizationMargin &&
			depositSurvivesUntilFork(m.chainSvc.GetCurrentEpochStats(), m.chainSvc.GetChainSpec(), amount, forkEpoch))

	if !shouldDeposit {
		m.log.WithFields(logrus.Fields{
			"current_epoch":     currentEpoch,
			"fork_epoch":        forkEpoch,
			"epochs_until_fork": epochsUntilFork,
		}).Debug("Waiting for early onboarding deposit window")

		return false
	}

	m.fireEvent("early_onboard", fmt.Sprintf("Submitting early onboarding deposit (%d gwei, %d epochs before fork)", amount, epochsUntilFork), "info")

	if m.depositPendingCallback != nil {
		m.depositPendingCallback()
	}

	if err := m.earlyDepositSvc.CreateEarlyDeposit(ctx, amount); err != nil {
		m.log.WithError(err).Warn("Early onboarding deposit failed, retrying next epoch")
		m.fireEvent("early_onboard", fmt.Sprintf("Early deposit failed: %v, retrying", err), "warning")

		return false // retry on the next epoch
	}

	m.fireEvent("early_onboard", "Early deposit confirmed, waiting for fork transition and registration", "success")
	m.waitForEarlyRegistration(ctx, forkEpoch, currentEpoch)

	return true
}

// waitForEarlyRegistration waits for the builder to be registered after an early deposit.
// The registration only happens once the Gloas fork converts the pending deposit into a
// builder, which can be several epochs out, so the timeout spans until a few epochs past
// the fork. On timeout it logs and returns; the normal post-fork flow then retries via the
// builder deposit contract as a fallback.
func (m *Manager) waitForEarlyRegistration(ctx context.Context, forkEpoch, currentEpoch phase0.Epoch) {
	spec := m.chainSvc.GetChainSpec()

	epochsToWait := uint64(forkEpoch-currentEpoch) + earlyOnboardFinalizationMargin
	timeout := max(time.Duration(epochsToWait*spec.SlotsPerEpoch)*spec.SecondsPerSlot, 5*time.Minute)

	if err := m.WaitForRegistration(ctx, timeout); err != nil {
		m.log.WithError(err).Warn("Builder not registered after early deposit; normal post-fork flow will retry")
		m.fireEvent("early_onboard", "Builder not yet registered after early deposit; post-fork flow will retry", "warning")
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

		if isDepositDeferred(err) {
			// Queue fee too high or contract not active yet — keep retrying quietly.
			m.log.WithError(err).Info("Builder registration deferred, retrying in 30s")
		} else {
			m.log.WithError(err).Warn("Builder registration attempt failed, retrying in 30s")
			m.fireEvent("deposit", fmt.Sprintf("Registration attempt failed: %v, retrying in 30s", err), "warning")
		}

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
					if isDepositDeferred(err) {
						// Queue fee too high or contract not active — delay this top-up
						// to the next monitor tick instead of failing.
						m.log.WithError(err).Info("Balance top-up deferred")
						m.fireEvent("balance_topup", fmt.Sprintf("Top-up deferred: %v", err), "info")
					} else {
						m.log.WithError(err).Warn("Balance topup failed")
						m.fireEvent("balance_topup", fmt.Sprintf("Balance topup failed: %v", err), "error")
					}
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
	oldBalance := m.builderState.Balance
	m.builderState = &BuilderState{
		Pubkey:            pubkey[:],
		Index:             info.Index,
		IsRegistered:      true,
		Balance:           info.Balance,
		DepositEpoch:      info.DepositEpoch,
		WithdrawableEpoch: info.WithdrawableEpoch,
	}
	m.stateMu.Unlock()

	// Only reset balance adjustment when the chain state actually changed (new epoch).
	// Between epochs the chain state returns the same stale balance, so we must
	// keep the local adjustment to avoid re-triggering topups.
	if info.Balance != oldBalance && m.bidTracker != nil {
		m.bidTracker.ResetBalanceAdjustment()
	}
}
