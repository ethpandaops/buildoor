package txpool

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/stretchr/testify/require"
)

func baseFeeParent(gasUsed uint64, timestamp uint64) *types.Header {
	return &types.Header{GasLimit: 30_000_000, GasUsed: gasUsed, BaseFee: big.NewInt(1_000_000_000), Time: timestamp}
}

func TestCalcNextBaseFee(t *testing.T) {
	full, empty, target := uint64(30_000_000), uint64(0), uint64(15_000_000)

	tests := []struct {
		name    string
		gasUsed uint64
		spacing baseFeeSpacing
		want    int64
	}{
		{"classic full", full, baseFeeSpacing{}, 1_125_000_000},
		{"classic empty", empty, baseFeeSpacing{}, 875_000_000},
		{"classic target", target, baseFeeSpacing{}, 1_000_000_000},
		// The slot-spaced rule equals the classic one at 12s per slot.
		{"spaced 12s", full, baseFeeSpacing{seconds: 12, slots: 1}, 1_125_000_000},
		{"spaced 12s missed slot", empty, baseFeeSpacing{seconds: 24, slots: 2}, 875_000_000},
		// 9s slots: 3/32 per block.
		{"spaced 9s full", full, baseFeeSpacing{seconds: 9, slots: 1}, 1_093_750_000},
		{"spaced 9s empty", empty, baseFeeSpacing{seconds: 9, slots: 1}, 906_250_000},
		// The first 9s slot follows a 12s one: the elapsed time is capped at
		// 12s per slot, so the change never exceeds 1/8 per slot.
		{"spaced capped", full, baseFeeSpacing{seconds: 30, slots: 2}, 1_125_000_000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calcNextBaseFee(baseFeeParent(tt.gasUsed, 0), tt.spacing)
			require.Equal(t, big.NewInt(tt.want), got)
		})
	}

	// An increase moves the base fee by at least 1 wei.
	require.Equal(t, big.NewInt(1_001),
		calcNextBaseFee(&types.Header{GasLimit: 30_000_000, GasUsed: 15_000_001, BaseFee: big.NewInt(1_000)},
			baseFeeSpacing{seconds: 9, slots: 1}))
}

func TestPoolBaseFeeSpacing(t *testing.T) {
	// Heze (9s slots) from slot 160; 12s slots before.
	chainSvc := &stubChainService{fork: version.DataVersionGloas, hezeSlot: 160}
	pool := &Pool{chainSvc: chainSvc}

	slotTime := func(slot phase0.Slot) uint64 { return uint64(chainSvc.SlotToTime(slot).Unix()) }

	// Before Heze: the classic rule.
	require.Equal(t, baseFeeSpacing{}, pool.baseFeeSpacing(baseFeeParent(0, slotTime(100)), 0))

	chainSvc.fork = version.DataVersionHeze

	// The slot after the parent (slot 0) and an explicit slot after a miss.
	require.Equal(t, baseFeeSpacing{seconds: 9, slots: 1}, pool.baseFeeSpacing(baseFeeParent(0, slotTime(170)), 0))
	require.Equal(t, baseFeeSpacing{seconds: 18, slots: 2}, pool.baseFeeSpacing(baseFeeParent(0, slotTime(170)), 172))
	// The first 9s slot follows the last 12s one.
	require.Equal(t, baseFeeSpacing{seconds: 12, slots: 1}, pool.baseFeeSpacing(baseFeeParent(0, slotTime(159)), 160))
	// A slot not after the parent falls back to the classic rule.
	require.Equal(t, baseFeeSpacing{}, pool.baseFeeSpacing(baseFeeParent(0, slotTime(170)), 170))

	require.Equal(t, big.NewInt(1_093_750_000), pool.NextBaseFee(baseFeeParent(30_000_000, slotTime(170)), 0))
	require.Equal(t, time.Unix(int64(slotTime(161)), 0), chainSvc.SlotToTime(161))
}
