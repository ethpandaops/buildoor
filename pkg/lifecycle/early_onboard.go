package lifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder_keys"
	"github.com/ethpandaops/buildoor/pkg/chain"
)

// earlyArrivalsWindowEpochs is how close (in epochs) to the predicted deposit window
// the evaluation also fetches the head state's queue: deposits that arrived since the
// epoch snapshot shield ours and can open the window mid-epoch.
const earlyArrivalsWindowEpochs = 32

// earlyOnboard onboards the whole target key set before the Gloas fork via the
// regular validator deposit contract, so there is no coverage gap between the
// Fulu Builder-API range and Gloas. The deposits do not race each other: they
// sit in the pending-deposit queue together and the fork transition converts
// them all into builders.
//
// It returns immediately (no-op) when early onboarding does not apply: the early
// deposit service is unavailable, Gloas is not scheduled, no deposit contract is
// known, Gloas is already active, or every target key is already registered.
//
// When applicable the phase lasts until the fork: it re-evaluates the deposit
// timing on every new head (the deposit can land in the next block at the
// earliest), on a per-slot timer that covers missed blocks, on every epoch
// snapshot and on every reconcile poke (a target change), and submits a batch for
// whichever target keys are not yet queued whenever the window is open — a key
// target raised after the first batch is deposited early too, as long as the
// fork is far enough away. Builder indices follow the queue order at the fork,
// so the first safe block is the one to land in.
func (m *Manager) earlyOnboard(ctx context.Context) {
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

	m.registry.Refresh()

	if len(m.earlyOnboardTargets()) == 0 {
		return
	}

	forkEpoch := spec.GetForkEpoch(version.DataVersionGloas)

	m.log.WithFields(logrus.Fields{
		"gloas_fork_epoch": forkEpoch,
		"target_keys":      m.cfg.BuilderKeys.EffectiveTargetCount(),
	}).Info("Gloas scheduled, evaluating early builder onboarding")
	m.fireEvent("early_onboard", fmt.Sprintf(
		"Gloas fork at epoch %d, preparing early onboarding of %d builder keys",
		forkEpoch, m.cfg.BuilderKeys.EffectiveTargetCount()), "info")

	// Subscribe before the first evaluation so an epoch transition or head can't slip
	// through between a "wait" decision and the subscription.
	epochSub := m.chainSvc.SubscribeEpochStats()
	defer epochSub.Unsubscribe()

	headSub := m.clClient.Events().SubscribeHead()
	defer headSub.Unsubscribe()

	slotTimer := time.NewTimer(m.untilMissedBlockCheck())
	defer slotTimer.Stop()

	// The block of the current slot may not be built yet, so a deposit sent now
	// could still join it.
	landingSlot := m.chainSvc.GetCurrentSlot()

	for {
		if m.tryEarlyOnboardOnce(ctx, forkEpoch, landingSlot) {
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

			landingSlot = m.chainSvc.GetCurrentSlot()
		case head := <-headSub.Channel():
			// The head block is sealed: the earliest block a deposit sent now can
			// join is the next one.
			landingSlot = head.Slot + 1
		case <-slotTimer.C:
			// Covers missed blocks, where no head event arrives for the slot.
			slotTimer.Reset(m.untilMissedBlockCheck())

			landingSlot = m.chainSvc.GetCurrentSlot()
		case <-m.poke:
			// The key target changed: new target keys may need a deposit.
			landingSlot = m.chainSvc.GetCurrentSlot()
		}
	}
}

// untilMissedBlockCheck returns the delay until a third into the next slot, by which
// the slot's block has normally arrived; when it did not, the timer evaluation
// covers the missing head event.
func (m *Manager) untilMissedBlockCheck() time.Duration {
	spec := m.chainSvc.GetChainSpec()
	next := m.chainSvc.SlotToTime(m.chainSvc.GetCurrentSlot() + 1)

	return max(time.Until(next.Add(spec.SecondsPerSlot/3)), time.Second)
}

// earlyOnboardTargets returns the target keys that still need an early deposit:
// not yet in the builder registry, no deposit of ours in flight, and not already
// waiting in the pending-deposit queue (restart safety — a prior run's deposits
// must not be submitted twice).
func (m *Manager) earlyOnboardTargets() []*builder_keys.Key {
	target := m.cfg.BuilderKeys.EffectiveTargetCount()

	pending := make([]*builder_keys.Key, 0, target)

	for keyIndex := uint64(0); keyIndex < target; keyIndex++ {
		key, err := m.registry.Key(keyIndex)
		if err != nil {
			m.log.WithError(err).WithField("key_index", keyIndex).
				Warn("Cannot derive builder key for early onboarding")

			break
		}

		if m.chainSvc.GetBuilderByPubkey(key.Pubkey()) != nil {
			continue
		}

		// A deposit we just sent is neither in the epoch snapshot nor, until its
		// block arrives, in the head queue.
		if key.Status() == builder_keys.StatusDepositing {
			continue
		}

		if m.earlyDepositSvc.HasPendingDeposit(key) {
			continue
		}

		pending = append(pending, key)
	}

	return pending
}

// tryEarlyOnboardOnce performs one early-onboarding evaluation. It returns true when the
// early-onboarding phase is over and the loop should stop: the fork is reached or too
// close for another early deposit (after waiting for the registration of a queued
// primary key); the normal post-fork flow takes over from there. Keys not yet queued
// are deposited whenever the window is open; between batches it keeps waiting, so a
// raised key target is picked up.
func (m *Manager) tryEarlyOnboardOnce(ctx context.Context, forkEpoch phase0.Epoch, landingSlot phase0.Slot) bool {
	if !m.enabled.Load() {
		return false // paused; keep waiting until re-enabled
	}

	m.registry.Refresh()

	currentEpoch := m.chainSvc.GetCurrentEpoch()
	if currentEpoch >= forkEpoch {
		m.finishEarlyOnboard(ctx, forkEpoch, currentEpoch)

		return true
	}

	remaining := m.earlyOnboardTargets()
	if len(remaining) == 0 {
		// Everything is registered or already queued; the fork transition converts
		// the queued deposits into builders. Keep watching for a raised key target.
		if m.earlyAllQueuedLogged.CompareAndSwap(false, true) {
			m.log.Info("All target builder keys onboarded or queued, waiting for the Gloas transition")
			m.fireEvent("early_onboard", "All target keys deposited, waiting for the Gloas transition", "info")
		}

		return false
	}

	m.earlyAllQueuedLogged.Store(false)

	spec := m.chainSvc.GetChainSpec()
	epochsUntilFork := uint64(forkEpoch - currentEpoch)
	forkSlot := uint64(forkEpoch) * spec.SlotsPerEpoch
	slotsUntilFork := forkSlot - uint64(m.chainSvc.GetCurrentSlot())

	// Only onboard early if there is enough runway before the fork. Closer than the
	// minimum, the early deposits may not land in (and finalize within) the pending
	// queue in time, so abandon early onboarding and let the normal post-fork flow
	// register the keys via the builder deposit contract instead.
	if slotsUntilFork < minEarlyOnboardSlots {
		m.log.WithFields(logrus.Fields{
			"fork_epoch":       forkEpoch,
			"slots_until_fork": slotsUntilFork,
			"remaining_keys":   len(remaining),
		}).Info("Fewer than minimum slots until Gloas, skipping early onboarding (will deposit via builder contract after the fork)")
		m.fireEvent("early_onboard", fmt.Sprintf(
			"Only %d slots until Gloas, skipping early onboarding of %d keys; will deposit after the fork",
			slotsUntilFork, len(remaining)), "info")
		m.finishEarlyOnboard(ctx, forkEpoch, currentEpoch)

		return true
	}

	amount := m.cfg.DepositAmount
	stats := m.chainSvc.GetCurrentEpochStats()
	count := uint64(len(remaining))

	m.logEarlyOnboardWindow(stats, spec, forkEpoch)

	// Deposit as soon as a batch landing at landingSlot is still queued at the fork:
	// first judged against the epoch snapshot alone, then — near the predicted window —
	// with the deposits that arrived since. The final epoch before the fork is the
	// fallback when the drain cannot be modeled.
	lastSafeEpoch := (epochsUntilFork-1)*spec.SlotsPerEpoch < minEarlyOnboardSlots
	shouldDeposit := lastSafeEpoch ||
		depositsSurviveUntilFork(stats, nil, spec, amount, count, landingSlot, forkEpoch)

	var arrivals []chain.PendingDepositInfo

	if !shouldDeposit && nearEarlyOnboardWindow(stats, spec, forkEpoch) {
		var err error

		arrivals, err = m.queueArrivals(ctx, stats)
		if err != nil {
			m.log.WithError(err).Debug("Failed to fetch the head pending-deposit queue")
		} else {
			shouldDeposit = depositsSurviveUntilFork(stats, arrivals, spec, amount, count, landingSlot, forkEpoch)
		}
	}

	if !shouldDeposit {
		m.log.WithFields(logrus.Fields{
			"current_epoch":     currentEpoch,
			"fork_epoch":        forkEpoch,
			"epochs_until_fork": epochsUntilFork,
			"landing_slot":      landingSlot,
			"queue_arrivals":    len(arrivals),
			"remaining_keys":    len(remaining),
		}).Debug("Waiting for early onboarding deposit window")

		return false
	}

	// The epoch snapshot cannot see a deposit of ours that landed since (e.g. before a
	// restart); the head queue can.
	remaining, err := m.withoutQueuedKeys(ctx, stats, remaining, arrivals)
	if err != nil {
		m.log.WithError(err).Warn("Failed to check the head pending-deposit queue, retrying")

		return false
	}

	if len(remaining) == 0 {
		return false
	}

	m.fireEvent("early_onboard", fmt.Sprintf(
		"Submitting %d early onboarding deposits (%d gwei each, %d slots before fork)",
		len(remaining), amount, slotsUntilFork), "info")

	if m.depositPendingCallback != nil {
		m.depositPendingCallback()
	}

	//nolint:gosec // the target key count is bounded by the derivation cap
	if !m.walletCanFund(ctx, amount*uint64(len(remaining))) {
		return false
	}

	// The deposits go out as one batch: they do not race each other, they sit in
	// the pending-deposit queue together and the fork transition converts them all.
	errs, err := m.earlyDepositSvc.CreateEarlyDeposits(ctx, remaining, amount)
	if err != nil {
		m.log.WithError(err).Warn("Early onboarding deposits failed, retrying next epoch")
		m.fireEvent("early_onboard", fmt.Sprintf("Early deposits failed: %v, retrying", err), "warning")

		return false
	}

	submitted := 0

	for i, key := range remaining {
		if errs[i] != nil {
			m.log.WithError(errs[i]).WithField("key", key.String()).
				Warn("Early onboarding deposit failed, retrying next epoch")
			m.fireEvent("early_onboard", fmt.Sprintf(
				"Early deposit for key #%d failed: %v, retrying", key.KeyIndex(), errs[i]), "warning")

			continue
		}

		m.registry.MarkDepositSubmitted(key.KeyIndex())

		submitted++
	}

	if submitted == 0 {
		return false // nothing landed; retry the whole batch next epoch
	}

	m.fireEvent("early_onboard", fmt.Sprintf(
		"%d early deposits confirmed, waiting for the Gloas transition",
		submitted), "success")

	// The phase goes on: a raised key target before the fork is deposited early too.
	return false
}

// finishEarlyOnboard ends the early-onboarding phase at the fork. When the primary
// key's early deposit sits in the pending-deposit queue, or the fork transition has
// already converted it, it waits for the registration to be observed (bounded by
// earlyRegistrationWaitMargin) so the registration callback fires; otherwise the
// reconciler registers the keys via the builder deposit contract right away.
func (m *Manager) finishEarlyOnboard(ctx context.Context, forkEpoch, currentEpoch phase0.Epoch) {
	primary := m.registry.Primary()

	if m.chainSvc.GetBuilderByPubkey(primary.Pubkey()) == nil &&
		!m.earlyDepositSvc.HasPendingDeposit(primary) {
		return
	}

	m.waitForEarlyRegistration(ctx, forkEpoch, currentEpoch)
}

// waitForEarlyRegistration waits for the primary key to be registered after the early
// deposits. The registration only happens once the Gloas fork converts the pending
// deposits into builders, which can be several epochs out, so the timeout spans until a
// few epochs past the fork. On timeout it logs and returns; the reconciler then retries
// via the builder deposit contract as a fallback.
func (m *Manager) waitForEarlyRegistration(ctx context.Context, forkEpoch, currentEpoch phase0.Epoch) {
	spec := m.chainSvc.GetChainSpec()

	epochsToWait := uint64(forkEpoch-currentEpoch) + earlyRegistrationWaitMargin
	timeout := max(time.Duration(epochsToWait*spec.SlotsPerEpoch)*spec.SecondsPerSlot, 5*time.Minute)

	if err := m.WaitForRegistration(ctx, m.registry.Primary(), timeout); err != nil {
		m.log.WithError(err).Warn("Builder not registered after early deposits; the reconciler will retry")
		m.fireEvent("early_onboard",
			"Builder not yet registered after the early deposits; the reconciler will retry", "warning")
	}
}

// nearEarlyOnboardWindow reports whether the fork is close enough to the window
// predicted from the epoch snapshot that deposits arriving within the epoch could
// open it, which is when the head queue is worth fetching on every evaluation.
func nearEarlyOnboardWindow(stats *chain.EpochStats, spec *chain.ChainSpec, forkEpoch phase0.Epoch) bool {
	drain, ok := queueDrainEpochs(stats, spec)

	return ok && uint64(forkEpoch-stats.Epoch) <= drain+earlyArrivalsWindowEpochs
}

// logEarlyOnboardWindow logs the predicted deposit window once per epoch snapshot.
func (m *Manager) logEarlyOnboardWindow(stats *chain.EpochStats, spec *chain.ChainSpec, forkEpoch phase0.Epoch) {
	if stats == nil || m.earlyWindowLoggedEpoch.Load() == uint64(stats.Epoch)+1 {
		return
	}

	m.earlyWindowLoggedEpoch.Store(uint64(stats.Epoch) + 1)

	drain, ok := queueDrainEpochs(stats, spec)
	if !ok || forkEpoch <= stats.Epoch {
		return
	}

	// A deposit survives once the fork is closer than the drain time, and in any
	// case from the fork-2 epoch on, where it cannot finalize before the fork.
	lead := max(drain, 3) - 1

	window := stats.Epoch
	if uint64(forkEpoch-stats.Epoch) > lead {
		window = forkEpoch - phase0.Epoch(lead)
	}

	m.log.WithFields(logrus.Fields{
		"window_epoch":     window,
		"epochs_to_window": uint64(window - stats.Epoch),
		"fork_epoch":       forkEpoch,
		"queue_length":     len(stats.PendingDeposits),
		"queue_drain":      drain,
	}).Info("Early onboarding deposit window predicted (moves with the deposit queue length)")
}

// queueArrivals returns the head state's pending deposits that arrived after the
// epoch snapshot, in queue order. Deposits for validators the snapshot already
// flagged as exited keep their no-churn flag.
func (m *Manager) queueArrivals(ctx context.Context, stats *chain.EpochStats) ([]chain.PendingDepositInfo, error) {
	deposits, err := m.clClient.GetPendingDeposits(ctx, "head")
	if err != nil {
		return nil, err
	}

	noChurn := make(map[phase0.BLSPubKey]bool)

	for _, deposit := range stats.PendingDeposits {
		if deposit.NoChurn {
			noChurn[deposit.Pubkey] = true
		}
	}

	arrivals := make([]chain.PendingDepositInfo, 0, 16)

	for _, deposit := range deposits {
		if deposit == nil || deposit.Slot <= stats.StateSlot {
			continue
		}

		arrivals = append(arrivals, chain.PendingDepositInfo{
			Pubkey:  deposit.Pubkey,
			Amount:  uint64(deposit.Amount),
			Slot:    deposit.Slot,
			NoChurn: noChurn[deposit.Pubkey],
		})
	}

	return arrivals, nil
}

// withoutQueuedKeys drops the keys whose deposit already sits in the head queue's
// arrivals. It fetches the arrivals when the caller has none yet.
func (m *Manager) withoutQueuedKeys(
	ctx context.Context,
	stats *chain.EpochStats,
	keys []*builder_keys.Key,
	arrivals []chain.PendingDepositInfo,
) ([]*builder_keys.Key, error) {
	if arrivals == nil {
		var err error

		arrivals, err = m.queueArrivals(ctx, stats)
		if err != nil {
			return nil, err
		}
	}

	queued := make(map[phase0.BLSPubKey]bool, len(arrivals))
	for _, deposit := range arrivals {
		queued[deposit.Pubkey] = true
	}

	result := make([]*builder_keys.Key, 0, len(keys))

	for _, key := range keys {
		if !queued[key.Pubkey()] {
			result = append(result, key)
		}
	}

	return result, nil
}
