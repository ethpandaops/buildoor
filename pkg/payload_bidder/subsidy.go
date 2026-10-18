package payload_bidder

import "math"

// ApplySubsidyGwei applies a signed subsidy to a gwei amount. A positive
// subsidy is added on top; a negative one is deducted, so a bid derived from
// the block value leaves that much of it with the builder as its margin (the
// Gloas payload's coinbase collects the block value, the bid pays the
// proposer). The result saturates at 0 and MaxUint64.
func ApplySubsidyGwei(value uint64, subsidy int64) uint64 {
	if subsidy >= 0 {
		if value > math.MaxUint64-uint64(subsidy) {
			return math.MaxUint64
		}

		return value + uint64(subsidy)
	}

	deduction := uint64(-(subsidy + 1)) + 1 // -MinInt64 without overflow
	if deduction >= value {
		return 0
	}

	return value - deduction
}
