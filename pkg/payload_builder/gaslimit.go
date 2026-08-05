package payload_builder

// expectedBidGasLimit returns the only gas limit a Gloas bid may carry for a
// given EL parent and proposer target: the bid gossip rules require exact
// equality with the target clamped into the EIP-1559 adjustment band around
// the parent's gas limit (at most parent/1024 - 1 away per block).
func expectedBidGasLimit(parentGasLimit, targetGasLimit uint64) uint64 {
	maxDiff := parentGasLimit / 1024
	if maxDiff == 0 {
		maxDiff = 1
	}

	maxDiff--

	minLimit := parentGasLimit - maxDiff
	maxLimit := parentGasLimit + maxDiff

	switch {
	case targetGasLimit < minLimit:
		return minLimit
	case targetGasLimit > maxLimit:
		return maxLimit
	default:
		return targetGasLimit
	}
}
