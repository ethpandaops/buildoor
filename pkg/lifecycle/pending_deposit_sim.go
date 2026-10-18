package lifecycle

import (
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/chain"
)

// This file models Electra's process_pending_deposits well enough to decide whether
// a builder deposit landing at a given slot would still be sitting in the beacon
// state's pending_deposits queue at the Gloas fork boundary (so the fork's
// onboard_builders_from_pending_deposits converts it into a builder). Builder
// indices are handed out in queue order at the fork, so the earliest surviving
// deposit becomes builder 0: the model must be tight, not just safe.
//
// The estimate is an UPPER BOUND on how many entries drain before the fork, so a
// "survives" verdict is never wrong:
//   - Finality is assumed as good as it can possibly be: at the transition into
//     epoch k the finalized checkpoint is at most epoch k-2, and processing stops
//     at the first entry whose slot is newer than that checkpoint's start slot.
//     Real finality can only lag further, which drains less.
//   - Deposits for exited validators (postponed behind the queue tail, or applied
//     without churn once withdrawn) are counted against the per-epoch cap but never
//     against the churn budget, which lets the most entries behind them drain.
// It ignores the Eth1 bridge gate (assumes it is long passed), which also only
// over-estimates the drain.

// simDeposit is one pending-deposit queue entry as the simulation sees it.
type simDeposit struct {
	amount  uint64      // gwei
	slot    phase0.Slot // slot the deposit request was processed in
	noChurn bool        // consumes no churn (deposit for an exited validator)
}

// activationExitChurnLimit mirrors the consensus spec get_activation_exit_churn_limit:
//
//	churn = max(MIN_PER_EPOCH_CHURN_LIMIT, total_active_balance // CHURN_LIMIT_QUOTIENT)
//	churn = churn - churn % EFFECTIVE_BALANCE_INCREMENT
//	return min(MAX_PER_EPOCH_ACTIVATION_EXIT_CHURN_LIMIT, churn)
//
// All balances are in gwei. Returns 0 if CHURN_LIMIT_QUOTIENT is unknown (0).
func activationExitChurnLimit(
	totalActiveBalance, minPerEpochChurn, churnQuotient, maxActivationExitChurn, effBalanceIncrement uint64,
) uint64 {
	if churnQuotient == 0 {
		return 0
	}

	churn := max(totalActiveBalance/churnQuotient, minPerEpochChurn)

	if effBalanceIncrement > 0 {
		churn -= churn % effBalanceIncrement
	}

	if maxActivationExitChurn > 0 && churn > maxActivationExitChurn {
		churn = maxActivationExitChurn
	}

	return churn
}

// drainTransition replays process_pending_deposits for the transition into epoch
// next: entries drain FIFO from head until one is not finalized yet, the per-epoch
// count reaches maxPerEpoch, or the running amount would exceed the budget
// (depositBalanceToConsume + churnLimit). It returns the new queue head and the
// carried balance, which is only kept when the churn limit was hit, matching the
// spec. The transition into epoch next finalizes at best epoch next-2.
func drainTransition(
	queue []simDeposit, head int,
	depositBalanceToConsume, churnLimit, maxPerEpoch uint64,
	next phase0.Epoch, slotsPerEpoch uint64,
) (int, uint64) {
	var finalizedSlot phase0.Slot
	if next >= 2 {
		finalizedSlot = phase0.Slot(uint64(next-2) * slotsPerEpoch)
	}

	available := depositBalanceToConsume + churnLimit
	processed := uint64(0)
	count := uint64(0)

	for head < len(queue) {
		deposit := queue[head]
		if deposit.slot > finalizedSlot || count >= maxPerEpoch {
			break
		}

		if !deposit.noChurn {
			if processed+deposit.amount > available {
				return head, available - processed
			}

			processed += deposit.amount
		}

		count++
		head++
	}

	return head, 0
}

// simulateDepositSurvives appends our deposits to the back of the queue and
// replays the epoch transitions from the queue's epoch (fromEpoch) up to and
// including the transition into forkEpoch. It returns true if the FIRST of our
// deposits is never drained — i.e. the whole batch is still queued when the fork
// upgrade runs. The queue drains from the front, so the batch's first entry is
// the first to go: once it survives, every entry behind it does too. A drained
// entry is applied as an ordinary validator deposit and never becomes a builder.
func simulateDepositSurvives(
	queue, ours []simDeposit,
	depositBalanceToConsume, churnLimit, maxPerEpoch uint64,
	fromEpoch, forkEpoch phase0.Epoch,
	slotsPerEpoch uint64,
) bool {
	if maxPerEpoch == 0 || slotsPerEpoch == 0 {
		// Without the per-epoch cap or the epoch length the drain cannot be modeled;
		// treat as undecidable and let the caller fall back to the last-epoch path.
		return false
	}

	if len(ours) == 0 {
		return true
	}

	sim := make([]simDeposit, 0, len(queue)+len(ours))
	sim = append(sim, queue...)
	sim = append(sim, ours...)

	dbtc := depositBalanceToConsume
	head := 0
	firstOurs := len(queue)

	for next := fromEpoch + 1; next <= forkEpoch; next++ {
		head, dbtc = drainTransition(sim, head, dbtc, churnLimit, maxPerEpoch, next, slotsPerEpoch)
		if head > firstOurs {
			// Our first deposit was drained — it would be applied before the fork.
			return false
		}
	}

	return true
}

// depositsSurviveUntilFork is the chain-typed wrapper around simulateDepositSurvives.
// The queue is the epoch snapshot's pending deposits followed by the arrivals since
// the snapshot (deposits newer than stats.StateSlot, in queue order). It returns true
// if a batch of depositCount deposits of ourAmountGwei each, landing at landingSlot,
// would all still be in the pending_deposits queue at forkEpoch (judged by its
// first entry, see simulateDepositSurvives). It returns false (be
// conservative) when the fork is not in the future or the spec lacks the
// deposit-processing parameters needed to model draining.
func depositsSurviveUntilFork(
	stats *chain.EpochStats,
	arrivals []chain.PendingDepositInfo,
	spec *chain.ChainSpec,
	ourAmountGwei, depositCount uint64,
	landingSlot phase0.Slot,
	forkEpoch phase0.Epoch,
) bool {
	if stats == nil || spec == nil || forkEpoch <= stats.Epoch {
		return false
	}

	if spec.MaxPendingDepositsPerEpoch == 0 || spec.ChurnLimitQuotient == 0 {
		return false
	}

	churnLimit := activationExitChurnLimit(
		stats.TotalActiveBalance,
		spec.MinPerEpochChurnLimit,
		spec.ChurnLimitQuotient,
		spec.MaxPerEpochActivationExitChurnLimit,
		spec.EffectiveBalanceIncrement,
	)

	queue := make([]simDeposit, 0, len(stats.PendingDeposits)+len(arrivals))
	for _, deposit := range stats.PendingDeposits {
		queue = append(queue, simDeposit{amount: deposit.Amount, slot: deposit.Slot, noChurn: deposit.NoChurn})
	}

	for _, deposit := range arrivals {
		queue = append(queue, simDeposit{amount: deposit.Amount, slot: deposit.Slot, noChurn: deposit.NoChurn})
	}

	ours := make([]simDeposit, depositCount)
	for i := range ours {
		ours[i] = simDeposit{amount: ourAmountGwei, slot: landingSlot}
	}

	return simulateDepositSurvives(
		queue,
		ours,
		stats.DepositBalanceToConsume,
		churnLimit,
		spec.MaxPendingDepositsPerEpoch,
		stats.Epoch,
		forkEpoch,
		spec.SlotsPerEpoch,
	)
}

// maxQueueDrainEpochs bounds the drain-time simulation.
const maxQueueDrainEpochs = 1 << 16

// queueDrainEpochs returns how many epoch transitions the snapshot's pending-deposit
// queue takes to drain completely. While the queue keeps roughly its length (new
// deposits replacing drained ones), a fresh deposit survives once the fork is at
// most that many epochs away, which makes it the predicted deposit window. ok is
// false when the drain cannot be modeled.
func queueDrainEpochs(stats *chain.EpochStats, spec *chain.ChainSpec) (uint64, bool) {
	if stats == nil || spec == nil || spec.MaxPendingDepositsPerEpoch == 0 ||
		spec.ChurnLimitQuotient == 0 || spec.SlotsPerEpoch == 0 {
		return 0, false
	}

	churnLimit := activationExitChurnLimit(
		stats.TotalActiveBalance,
		spec.MinPerEpochChurnLimit,
		spec.ChurnLimitQuotient,
		spec.MaxPerEpochActivationExitChurnLimit,
		spec.EffectiveBalanceIncrement,
	)

	queue := make([]simDeposit, 0, len(stats.PendingDeposits))
	for _, deposit := range stats.PendingDeposits {
		queue = append(queue, simDeposit{amount: deposit.Amount, slot: deposit.Slot, noChurn: deposit.NoChurn})
	}

	dbtc := stats.DepositBalanceToConsume
	head := 0

	for epochs := uint64(0); epochs < maxQueueDrainEpochs; epochs++ {
		if head == len(queue) {
			return epochs, true
		}

		head, dbtc = drainTransition(queue, head, dbtc, churnLimit, spec.MaxPendingDepositsPerEpoch,
			stats.Epoch+phase0.Epoch(epochs)+1, spec.SlotsPerEpoch)
	}

	return 0, false
}
