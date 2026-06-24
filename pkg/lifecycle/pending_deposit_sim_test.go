package lifecycle

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const gwei = uint64(1_000_000_000)

func TestActivationExitChurnLimit(t *testing.T) {
	tests := []struct {
		name        string
		totalActive uint64
		minChurn    uint64
		quotient    uint64
		maxChurn    uint64
		increment   uint64
		want        uint64
	}{
		{
			name:        "below floor clamps to min",
			totalActive: 1_000 * gwei,
			minChurn:    64 * gwei,
			quotient:    65536,
			maxChurn:    256 * gwei,
			increment:   gwei,
			want:        64 * gwei, // 1000e9/65536 ~ 0.015 ETH < 64 ETH floor
		},
		{
			name:        "scales with active balance, floored to increment",
			totalActive: 65536 * 100 * gwei, // /65536 = 100 ETH
			minChurn:    64 * gwei,
			quotient:    65536,
			maxChurn:    256 * gwei,
			increment:   gwei,
			want:        100 * gwei,
		},
		{
			name:        "clamps to max",
			totalActive: 65536 * 1000 * gwei, // /65536 = 1000 ETH
			minChurn:    64 * gwei,
			quotient:    65536,
			maxChurn:    256 * gwei,
			increment:   gwei,
			want:        256 * gwei,
		},
		{
			name:        "unknown quotient yields zero",
			totalActive: 100 * gwei,
			minChurn:    64 * gwei,
			quotient:    0,
			maxChurn:    256 * gwei,
			increment:   gwei,
			want:        0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := activationExitChurnLimit(tt.totalActive, tt.minChurn, tt.quotient, tt.maxChurn, tt.increment)
			assert.Equal(t, tt.want, got)
		})
	}
}

func repeatAmounts(n int, amount uint64) []uint64 {
	out := make([]uint64, n)
	for i := range out {
		out[i] = amount
	}

	return out
}

func TestSimulateDepositSurvives(t *testing.T) {
	const (
		bigChurn  = 1_000_000 * gwei
		deposit32 = 32 * gwei
		maxPer    = uint64(16)
		noCarry   = uint64(0)
	)

	tests := []struct {
		name        string
		queue       []uint64
		ourAmount   uint64
		dbtc        uint64
		churn       uint64
		maxPerEpoch uint64
		transitions uint64
		want        bool
	}{
		{
			name:        "empty queue drains our deposit immediately",
			queue:       nil,
			ourAmount:   deposit32,
			churn:       bigChurn,
			maxPerEpoch: maxPer,
			transitions: 4,
			want:        false,
		},
		{
			name:        "long queue shields our deposit (churn-limited)",
			queue:       repeatAmounts(100, deposit32),
			ourAmount:   deposit32,
			churn:       64 * gwei, // ~2 deposits/epoch
			maxPerEpoch: maxPer,
			transitions: 4, // ~8 drained, far short of 100 ahead
			want:        true,
		},
		{
			name:        "short queue fully drains and reaches our deposit",
			queue:       repeatAmounts(4, deposit32),
			ourAmount:   deposit32,
			churn:       bigChurn, // count-limited only
			maxPerEpoch: maxPer,
			transitions: 1, // up to 16 drained > 4 ahead -> reaches ours
			want:        false,
		},
		{
			name:        "count cap shields our deposit across few transitions",
			queue:       repeatAmounts(100, 1), // tiny amounts, churn never binds
			ourAmount:   deposit32,
			churn:       bigChurn,
			maxPerEpoch: maxPer,
			transitions: 4, // 4*16 = 64 < 100 ahead
			want:        true,
		},
		{
			name:        "count cap eventually reaches our deposit over many transitions",
			queue:       repeatAmounts(100, 1),
			ourAmount:   deposit32,
			churn:       bigChurn,
			maxPerEpoch: maxPer,
			transitions: 10, // 10*16 = 160 > 100 ahead -> reaches ours
			want:        false,
		},
		{
			name:        "zero per-epoch cap is undecidable -> false",
			queue:       repeatAmounts(100, deposit32),
			ourAmount:   deposit32,
			churn:       bigChurn,
			maxPerEpoch: 0,
			transitions: 4,
			want:        false,
		},
		{
			name:        "carried deposit balance adds to first-epoch budget",
			queue:       repeatAmounts(3, deposit32),
			ourAmount:   deposit32,
			dbtc:        96 * gwei, // +3 deposits of headroom on top of churn
			churn:       deposit32, // 1 deposit/epoch from churn alone
			maxPerEpoch: maxPer,
			transitions: 1, // budget = 96+32 = 128 -> 4 deposits -> drains 3 ahead + ours
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := simulateDepositSurvives(tt.queue, tt.ourAmount, tt.dbtc, tt.churn, tt.maxPerEpoch, tt.transitions)
			assert.Equal(t, tt.want, got)
		})
	}
}
