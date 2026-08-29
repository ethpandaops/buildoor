package lifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder_keys"
)

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
// When applicable it re-evaluates the deposit timing once per epoch until the
// batch is submitted (and it waited for registration), the fork is reached, or
// the manager stops.
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
		"target_keys":      m.cfgSvc.Current().BuilderKeys.EffectiveTargetCount(),
	}).Info("Gloas scheduled, evaluating early builder onboarding")
	m.fireEvent("early_onboard", fmt.Sprintf(
		"Gloas fork at epoch %d, preparing early onboarding of %d builder keys",
		forkEpoch, m.cfgSvc.Current().BuilderKeys.EffectiveTargetCount()), "info")

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

// earlyOnboardTargets returns the target keys that still need an early deposit:
// not yet in the builder registry and not already waiting in the pending-deposit
// queue (restart safety — a prior run's deposits must not be submitted twice).
func (m *Manager) earlyOnboardTargets() []*builder_keys.Key {
	target := m.cfgSvc.Current().BuilderKeys.EffectiveTargetCount()

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

		if m.earlyDepositSvc.HasPendingDeposit(key) {
			continue
		}

		pending = append(pending, key)
	}

	return pending
}

// tryEarlyOnboardOnce performs one early-onboarding evaluation. It returns true when the
// early-onboarding phase is complete and the loop should stop: every target key is
// onboarded or queued (and we waited for registration), or the fork was reached without
// onboarding (handed off to the normal post-fork flow).
func (m *Manager) tryEarlyOnboardOnce(ctx context.Context, forkEpoch phase0.Epoch) bool {
	if !m.enabled.Load() {
		return false // paused; keep waiting until re-enabled
	}

	m.registry.Refresh()

	currentEpoch := m.chainSvc.GetCurrentEpoch()
	if currentEpoch >= forkEpoch {
		// Fork reached without onboarding — let the normal post-fork flow take over.
		return true
	}

	remaining := m.earlyOnboardTargets()
	if len(remaining) == 0 {
		// Everything is registered or already queued; wait for the fork transition
		// to convert the queued deposits into builders.
		m.log.Info("All target builder keys onboarded or queued, waiting for registration")
		m.fireEvent("early_onboard", "All target keys deposited, waiting for the Gloas transition", "info")
		m.waitForEarlyRegistration(ctx, forkEpoch, currentEpoch)

		return true
	}

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

		return true
	}

	amount := m.cfgSvc.Current().DepositAmount

	// Two deposit windows (per design): at least earlyOnboardFinalizationMargin epochs
	// before the fork if the pending-deposit queue is long enough to shield the whole
	// batch from being processed before the fork; otherwise at the latest epoch boundary
	// that still leaves at least minEarlyOnboardSlots before the fork (a fresh
	// back-of-queue batch is guaranteed to survive the remaining transitions).
	lastSafeEpoch := (epochsUntilFork-1)*spec.SlotsPerEpoch < minEarlyOnboardSlots
	shouldDeposit := lastSafeEpoch ||
		(epochsUntilFork >= earlyOnboardFinalizationMargin &&
			depositsSurviveUntilFork(m.chainSvc.GetCurrentEpochStats(), spec,
				amount, uint64(len(remaining)), forkEpoch))

	if !shouldDeposit {
		m.log.WithFields(logrus.Fields{
			"current_epoch":     currentEpoch,
			"fork_epoch":        forkEpoch,
			"epochs_until_fork": epochsUntilFork,
			"slots_until_fork":  slotsUntilFork,
			"remaining_keys":    len(remaining),
		}).Debug("Waiting for early onboarding deposit window")

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
		"%d early deposits confirmed, waiting for fork transition and registration",
		submitted), "success")
	m.waitForEarlyRegistration(ctx, forkEpoch, currentEpoch)

	return true
}

// waitForEarlyRegistration waits for the primary key to be registered after the early
// deposits. The registration only happens once the Gloas fork converts the pending
// deposits into builders, which can be several epochs out, so the timeout spans until a
// few epochs past the fork. On timeout it logs and returns; the reconciler then retries
// via the builder deposit contract as a fallback.
func (m *Manager) waitForEarlyRegistration(ctx context.Context, forkEpoch, currentEpoch phase0.Epoch) {
	spec := m.chainSvc.GetChainSpec()

	epochsToWait := uint64(forkEpoch-currentEpoch) + earlyOnboardFinalizationMargin
	timeout := max(time.Duration(epochsToWait*spec.SlotsPerEpoch)*spec.SecondsPerSlot, 5*time.Minute)

	if err := m.WaitForRegistration(ctx, m.registry.Primary(), timeout); err != nil {
		m.log.WithError(err).Warn("Builder not registered after early deposits; the reconciler will retry")
		m.fireEvent("early_onboard",
			"Builder not yet registered after the early deposits; the reconciler will retry", "warning")
	}
}
