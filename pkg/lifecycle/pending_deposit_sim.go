package lifecycle

import (
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/chain"
)

// This file models Electra's process_pending_deposits well enough to decide whether
// a builder deposit submitted *now* would still be sitting in the beacon state's
// pending_deposits queue at the Gloas fork boundary (so Gloas converts it into a
// builder). It is used by the early-onboarding flow in manager.go.
//
// The estimate is a deliberate UPPER BOUND on how many entries drain before the
// fork: finalization is treated as already reached for every entry, so processing
// is limited only by the per-epoch deposit cap and the activation-exit churn limit.
// Over-estimating the drain can only make us *more* cautious about depositing early
// (falling back to the safe "deposit in the epoch directly before the fork" path);
// it can never cause us to deposit early when our deposit would actually be drained.
// Our own deposit's finalization (it must be finalized before the fork for the
// conversion to pick it up) is handled separately by the >=4-epoch margin in the
// manager, not here.

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

// simulateDepositSurvives appends our deposits (amounts in gwei) to the back of the
// current pending-deposit queue (amounts in gwei) and replays `transitions` epoch
// boundaries of Electra deposit processing. It returns true if the LAST of our
// deposits is never drained — i.e. the whole batch is still queued at the fork
// boundary. Onboarding several builder keys appends several entries back to back,
// so the batch survives exactly when its last entry does.
//
// Per transition the processable budget is depositBalanceToConsume (carried) + churnLimit;
// entries drain FIFO while the per-epoch count stays under maxPerEpoch and the running
// amount stays within the budget. The leftover budget is carried only when the churn
// limit was hit, matching the spec.
func simulateDepositSurvives(
	queue, ourAmounts []uint64,
	depositBalanceToConsume, churnLimit, maxPerEpoch, transitions uint64,
) bool {
	if maxPerEpoch == 0 {
		// No per-epoch cap means we cannot model draining; treat as undecidable and
		// let the caller fall back to the deposit-just-before-fork path.
		return false
	}

	if len(ourAmounts) == 0 {
		return true
	}

	// ourIdx is the index of the last of our deposits: the one that must survive.
	ourIdx := len(queue) + len(ourAmounts) - 1

	sim := make([]uint64, 0, len(queue)+len(ourAmounts))
	sim = append(sim, queue...)
	sim = append(sim, ourAmounts...)

	dbtc := depositBalanceToConsume
	head := 0

	for range transitions {
		available := dbtc + churnLimit
		processed := uint64(0)
		count := uint64(0)
		churnLimited := false

		for head < len(sim) {
			if count >= maxPerEpoch {
				break
			}

			amount := sim[head]
			if processed+amount > available {
				churnLimited = true

				break
			}

			processed += amount
			count++
			head++

			if head > ourIdx {
				// Our deposit was just drained — it would not survive to the fork.
				return false
			}
		}

		if churnLimited {
			dbtc = available - processed
		} else {
			dbtc = 0
		}
	}

	return head <= ourIdx
}

// depositsSurviveUntilFork is the chain-typed wrapper around simulateDepositSurvives. It
// returns true if a batch of depositCount deposits of ourAmountGwei each, submitted at
// stats.Epoch, would all still be in the pending_deposits queue at forkEpoch. It returns
// false (be conservative — prefer the deposit-just-before-fork path) when the fork is not
// in the future or the spec lacks the deposit-processing parameters needed to model
// draining.
func depositsSurviveUntilFork(
	stats *chain.EpochStats,
	spec *chain.ChainSpec,
	ourAmountGwei, depositCount uint64,
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

	amounts := make([]uint64, len(stats.PendingDeposits))
	for i, deposit := range stats.PendingDeposits {
		amounts[i] = deposit.Amount
	}

	ourAmounts := make([]uint64, depositCount)
	for i := range ourAmounts {
		ourAmounts[i] = ourAmountGwei
	}

	transitions := uint64(forkEpoch - stats.Epoch)

	return simulateDepositSurvives(
		amounts,
		ourAmounts,
		stats.DepositBalanceToConsume,
		churnLimit,
		spec.MaxPendingDepositsPerEpoch,
		transitions,
	)
}
