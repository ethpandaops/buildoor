package payload_builder

// expectedBidGasLimit returns the gas limit a Gloas execution payload bid
// must commit to for gossip validation to accept it: the parent block's
// committed gas limit moved toward the proposer's preferred target, clamped
// to the maximum per-block adjustment of parent/1024 - 1. Any other value is
// rejected with InvalidGasLimit, so an EL that builds toward a different
// target produces unbiddable payloads unless the gas limit is adjusted to
// this value.
func expectedBidGasLimit(parentGasLimit, targetGasLimit uint64) uint64 {
	maxDiff := max(parentGasLimit/1024, 1) - 1

	switch {
	case targetGasLimit > parentGasLimit && targetGasLimit-parentGasLimit > maxDiff:
		return parentGasLimit + maxDiff
	case targetGasLimit < parentGasLimit && parentGasLimit-targetGasLimit > maxDiff:
		return parentGasLimit - maxDiff
	default:
		return targetGasLimit
	}
}
