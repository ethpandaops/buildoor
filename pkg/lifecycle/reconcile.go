package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	// reconcileInterval is the idle cadence of the reconcile loop. Epoch
	// transitions and target changes wake it sooner.
	reconcileInterval = 30 * time.Second

	// reconcileWorkDelay is the pause between two reconcile passes that each
	// performed work. Deposits, exits and top-ups all go through the single
	// funding wallet, so the fleet ramps one transaction at a time; this spaces
	// those out instead of hammering the deposit queue (whose fee grows with
	// its length) in one burst.
	reconcileWorkDelay = 2 * time.Second

	// walletFeeHeadroomGwei is the slack required on top of a deposit's stake
	// before the funding wallet is considered able to cover it: the queue fee
	// plus gas. A rough bound is enough — the transaction itself fails loudly.
	walletFeeHeadroomGwei = 100_000_000 // 0.1 ETH
)

// Reconcile requests an immediate reconcile pass. Called when the target key
// count changes so a UI edit acts within a second rather than at the next tick.
func (m *Manager) Reconcile() {
	select {
	case m.poke <- struct{}{}:
	default: // a pass is already pending
	}
}

// runReconciler is the manager's main loop: it onboards the fleet before the
// Gloas fork, then keeps the managed key count at the configured target and
// every managed key funded.
func (m *Manager) runReconciler(ctx context.Context) {
	defer m.wg.Done()

	if !m.waitForEnabled(ctx) {
		return
	}

	// Onboard the fleet before the Gloas fork via the regular deposit contract
	// (no-op when not applicable), so coverage is continuous across the fork.
	m.earlyOnboard(ctx)

	// The on-chain builder set is available from the first Gloas EpochStats; the
	// fork being active by epoch is not sufficient — the state must be fetched
	// and cached before we can tell which keys are already registered (otherwise
	// we would read an empty set and deposit for all of them again).
	if !m.waitForGloasState(ctx) {
		return
	}

	epochSub := m.chainSvc.SubscribeEpochStats()
	defer epochSub.Unsubscribe()

	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()

	for {
		worked := m.reconcileOnce(ctx)

		if worked {
			// More work may be pending; re-evaluate promptly rather than
			// waiting out the idle interval.
			select {
			case <-ctx.Done():
				return
			case <-m.stopCh:
				return
			case <-time.After(reconcileWorkDelay):
			}

			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
		case <-m.poke:
		case _, ok := <-epochSub.Channel():
			if !ok {
				return
			}
		}
	}
}

// reconcileOnce runs a single reconcile pass and reports whether it performed a
// lifecycle transaction. At most one transaction happens per pass: they all go
// through the same funding wallet, and each waits for its receipt.
func (m *Manager) reconcileOnce(ctx context.Context) bool {
	m.registry.Refresh()
	m.refreshBuilderState()

	if !m.enabled.Load() || m.balanceSvc == nil {
		return false
	}

	target := m.cfg.BuilderKeys.EffectiveTargetCount()
	managed := m.registry.Aggregate().Managed

	switch {
	case managed < target && m.cfg.BuilderKeys.AutoDeposit:
		return m.depositNextKey(ctx, target, managed)

	case managed > target && m.cfg.BuilderKeys.AutoExit:
		return m.exitSurplusKey(ctx, target, managed)
	}

	return m.topupNextKey(ctx)
}

// depositNextKey deposits for the lowest-index key eligible for one, bringing
// the fleet closer to the target.
func (m *Manager) depositNextKey(ctx context.Context, target, managed uint64) bool {
	key := m.registry.NextDepositCandidate()
	if key == nil {
		m.log.WithFields(logrus.Fields{
			"target":  target,
			"managed": managed,
		}).Warn("No builder key available for deposit; raise builder_keys.max_index")

		return false
	}

	amount := m.cfg.DepositAmount

	if !m.walletCanFund(ctx, amount) {
		return false
	}

	m.log.WithFields(logrus.Fields{
		"key":     key.String(),
		"target":  target,
		"managed": managed,
	}).Info("Depositing builder key to reach the target count")
	m.fireEvent("deposit", fmt.Sprintf(
		"Depositing builder key #%d (%d gwei) — %d of %d keys managed",
		key.KeyIndex(), amount, managed, target), "info")

	if m.depositPendingCallback != nil {
		m.depositPendingCallback()
	}

	if err := m.depositSvc.CreateDeposit(ctx, key, amount); err != nil {
		if isDepositDeferred(err) {
			// Queue fee too high or contract not active yet — retry later. This
			// is also the automatic backoff when a large ramp pushes the fee up.
			m.log.WithError(err).Info("Builder key deposit deferred")
			m.fireEvent("deposit", fmt.Sprintf("Deposit deferred: %v", err), "info")
		} else {
			m.log.WithError(err).WithField("key", key.String()).Warn("Builder key deposit failed")
			m.fireEvent("deposit", fmt.Sprintf("Deposit for key #%d failed: %v", key.KeyIndex(), err), "error")
		}

		return false
	}

	m.registry.MarkDepositSubmitted(key.KeyIndex())
	m.fireEvent("deposit", fmt.Sprintf(
		"Deposit for key #%d confirmed, waiting for beacon chain inclusion", key.KeyIndex()), "success")

	return true
}

// exitSurplusKey exits the highest-index key that can be exited, bringing the
// fleet down to the target. Keys with pending payments are skipped: the beacon
// chain silently ignores their exit requests.
func (m *Manager) exitSurplusKey(ctx context.Context, target, managed uint64) bool {
	key := m.registry.NextExitCandidate()
	if key == nil {
		m.log.WithFields(logrus.Fields{
			"target":  target,
			"managed": managed,
		}).Debug("Above the target key count but no key is exitable yet (pending payments)")

		return false
	}

	m.log.WithFields(logrus.Fields{
		"key":     key.String(),
		"target":  target,
		"managed": managed,
	}).Info("Exiting surplus builder key")
	m.fireEvent("exit", fmt.Sprintf(
		"Exiting surplus builder key #%d — %d of %d keys managed",
		key.KeyIndex(), managed, target), "info")

	if err := m.exitSvc.CreateExit(ctx, key); err != nil {
		m.log.WithError(err).WithField("key", key.String()).Warn("Builder key exit failed")
		m.fireEvent("exit", fmt.Sprintf("Exit for key #%d failed: %v", key.KeyIndex(), err), "error")

		return false
	}

	m.registry.MarkExitSubmitted(key.KeyIndex())
	m.fireEvent("exit", fmt.Sprintf("Builder key #%d exit submitted", key.KeyIndex()), "success")

	return true
}

// topupNextKey tops up the first managed key whose balance fell below the
// threshold. One key per pass, so a fleet-wide dip ramps instead of queueing a
// transaction per key at once.
func (m *Manager) topupNextKey(ctx context.Context) bool {
	for _, key := range m.registry.Keys() {
		needsTopup, amount, err := m.balanceSvc.NeedsTopup(key)
		if err != nil {
			if errors.Is(err, ErrBuilderExited) {
				// Expected steady state after an exit; the one-time warning
				// already fired from refreshBuilderState.
				continue
			}

			m.log.WithError(err).WithField("key", key.String()).Warn("Balance check failed")

			continue
		}

		if !needsTopup {
			continue
		}

		if !m.walletCanFund(ctx, amount) {
			return false
		}

		m.fireEvent("balance_topup", fmt.Sprintf(
			"Key #%d below threshold, topping up %d gwei", key.KeyIndex(), amount), "info")

		if err := m.balanceSvc.CheckAndTopup(ctx, key); err != nil {
			if isDepositDeferred(err) {
				// Queue fee too high or contract not active — delay this top-up
				// to the next pass instead of failing.
				m.log.WithError(err).Info("Balance top-up deferred")
				m.fireEvent("balance_topup", fmt.Sprintf("Top-up deferred: %v", err), "info")
			} else {
				m.log.WithError(err).WithField("key", key.String()).Warn("Balance topup failed")
				m.fireEvent("balance_topup", fmt.Sprintf(
					"Top-up for key #%d failed: %v", key.KeyIndex(), err), "error")
			}

			return false
		}

		// Immediately reflect the topup in the live balance (no finalization delay)
		if tracker := m.GetPaymentTracker(); tracker != nil {
			tracker.AddDeposit(key.KeyIndex(), amount)
		}

		m.fireEvent("balance_topup", fmt.Sprintf(
			"Key #%d topped up by %d gwei", key.KeyIndex(), amount), "success")

		return true
	}

	return false
}

// walletCanFund reports whether the funding wallet still covers a stake of
// amountGwei plus headroom for the queue fee and gas. The whole fleet is funded
// from one wallet, so running it dry is a fleet-wide stall — worth one clear
// event rather than a failing transaction per key.
func (m *Manager) walletCanFund(ctx context.Context, amountGwei uint64) bool {
	if m.wallet == nil {
		return false
	}

	balance, err := m.wallet.GetBalance(ctx)
	if err != nil {
		m.log.WithError(err).Warn("Failed to read funding wallet balance")

		return false
	}

	required := new(big.Int).Add(GweiToWei(amountGwei), GweiToWei(walletFeeHeadroomGwei))
	if balance.Cmp(required) >= 0 {
		m.walletUnderfunded.Store(false)

		return true
	}

	if m.walletUnderfunded.CompareAndSwap(false, true) {
		m.log.WithFields(logrus.Fields{
			"wallet":       m.wallet.Address().Hex(),
			"balance_wei":  balance.String(),
			"required_wei": required.String(),
		}).Warn("Funding wallet cannot cover the next builder key deposit")
		m.fireEvent("deposit", fmt.Sprintf(
			"Funding wallet %s cannot cover the next %d gwei deposit; fund it to continue",
			m.wallet.Address().Hex(), amountGwei), "error")
	}

	return false
}
