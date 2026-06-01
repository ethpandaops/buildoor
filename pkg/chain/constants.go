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
