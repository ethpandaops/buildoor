package chain

// FarFutureEpoch is the sentinel value indicating a builder/validator has not exited.
const FarFutureEpoch = uint64(0xFFFFFFFFFFFFFFFF)

const BuilderIndexFlag uint64 = 1 << 40

// IsBuilderActive returns true when a builder's deposit has been finalized and the
// builder has not exited. Pass the result of GetBuilderByPubkey and GetFinalizedEpoch.
func IsBuilderActive(info *BuilderInfo, finalizedEpoch uint64) bool {
	if info == nil {
		return false
	}
	return info.DepositEpoch < finalizedEpoch && info.WithdrawableEpoch == FarFutureEpoch
}

// HasBuilderExited returns true when the builder's exit has been initiated
// (withdrawable epoch set). Per the Gloas spec an exited builder can never be
// reactivated: a deposit for its pubkey only tops up the exited entry and is
// withdrawn back to the execution address by the sweep (pushing the withdrawable
// epoch out by MIN_BUILDER_WITHDRAWABILITY_DELAY if the entry was already swept).
// The pubkey only becomes depositable again once the entry's index is reused by a
// different builder's deposit and the pubkey leaves the registry (info == nil).
func HasBuilderExited(info *BuilderInfo) bool {
	return info != nil && info.WithdrawableEpoch != FarFutureEpoch
}
