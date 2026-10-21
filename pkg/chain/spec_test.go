package chain

import (
	"math"
	"testing"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/stretchr/testify/require"
)

func TestParseChainSpecSlotDuration(t *testing.T) {
	tests := []struct {
		name     string
		specData map[string]string
		want     time.Duration
		wantErr  bool
	}{
		{
			// EIP-7782 networks serve a stale SECONDS_PER_SLOT alongside
			// SLOT_DURATION_MS; the ms value must win.
			name: "SLOT_DURATION_MS preferred over stale SECONDS_PER_SLOT",
			specData: map[string]string{
				"SLOT_DURATION_MS": "6000",
				"SECONDS_PER_SLOT": "12",
				"SLOTS_PER_EPOCH":  "32",
			},
			want: 6 * time.Second,
		},
		{
			name: "SECONDS_PER_SLOT fallback",
			specData: map[string]string{
				"SECONDS_PER_SLOT": "12",
				"SLOTS_PER_EPOCH":  "32",
			},
			want: 12 * time.Second,
		},
		{
			name: "sub-second SLOT_DURATION_MS",
			specData: map[string]string{
				"SLOT_DURATION_MS": "500",
				"SLOTS_PER_EPOCH":  "32",
			},
			want: 500 * time.Millisecond,
		},
		{
			name: "zero SLOT_DURATION_MS rejected",
			specData: map[string]string{
				"SLOT_DURATION_MS": "0",
				"SECONDS_PER_SLOT": "12",
				"SLOTS_PER_EPOCH":  "32",
			},
			wantErr: true,
		},
		{
			name: "neither slot duration key present",
			specData: map[string]string{
				"SLOTS_PER_EPOCH": "32",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := ParseChainSpec(tt.specData, nil)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, spec.SlotDuration)
		})
	}
}

func hezeSpec(slotDuration, slotDurationHeze time.Duration, forkEpoch phase0.Epoch) *ChainSpec {
	return &ChainSpec{
		SlotDuration:     slotDuration,
		SlotDurationHeze: slotDurationHeze,
		SlotsPerEpoch:    32,
		ForkSchedule:     []ForkSchedule{{Fork: version.DataVersionHeze, Epoch: forkEpoch}},
	}
}

func TestParseChainSpecHezeSlotDuration(t *testing.T) {
	spec, err := ParseChainSpec(map[string]string{
		"SLOT_DURATION_MS":      "12000",
		"SLOT_DURATION_MS_HEZE": "9000",
		"SLOTS_PER_EPOCH":       "32",
		"HEZE_FORK_VERSION":     "0x80000038",
		"HEZE_FORK_EPOCH":       "5",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 12*time.Second, spec.SlotDuration)
	require.Equal(t, 9*time.Second, spec.SlotDurationHeze)

	forkSlot, ok := spec.HezeForkSlot()
	require.True(t, ok)
	require.Equal(t, phase0.Slot(160), forkSlot)

	spec, err = ParseChainSpec(map[string]string{
		"SLOT_DURATION_MS": "12000",
		"SLOTS_PER_EPOCH":  "32",
	}, nil)
	require.NoError(t, err)
	require.Zero(t, spec.SlotDurationHeze)

	_, ok = spec.HezeForkSlot()
	require.False(t, ok)
}

func TestSlotClockAcrossHeze(t *testing.T) {
	spec := hezeSpec(12*time.Second, 9*time.Second, 5)

	const forkSlot = phase0.Slot(160)

	forkOffset := 160 * 12 * time.Second

	tests := []struct {
		slot     phase0.Slot
		start    time.Duration
		duration time.Duration
	}{
		{0, 0, 12 * time.Second},
		{1, 12 * time.Second, 12 * time.Second},
		{forkSlot - 1, forkOffset - 12*time.Second, 12 * time.Second},
		{forkSlot, forkOffset, 9 * time.Second},
		{forkSlot + 1, forkOffset + 9*time.Second, 9 * time.Second},
		{forkSlot + 32, forkOffset + 32*9*time.Second, 9 * time.Second},
	}

	for _, tt := range tests {
		require.Equal(t, tt.start, spec.SlotStartOffset(tt.slot), "start of slot %d", tt.slot)
		require.Equal(t, tt.duration, spec.SlotDurationAt(tt.slot), "duration of slot %d", tt.slot)
		require.Equal(t, tt.slot, spec.SlotAtOffset(tt.start), "slot at start of slot %d", tt.slot)
		require.Equal(t, tt.slot, spec.SlotAtOffset(tt.start+tt.duration-time.Millisecond),
			"slot at end of slot %d", tt.slot)
		require.Equal(t, tt.slot+1, spec.SlotAtOffset(tt.start+tt.duration), "slot after slot %d", tt.slot)
	}

	require.Equal(t, phase0.Slot(0), spec.SlotAtOffset(-time.Second))
	require.Equal(t, 9*time.Second, spec.ShortestSlotDuration())
}

func TestSlotClockWithoutHezeChange(t *testing.T) {
	fixed := &ChainSpec{SlotDuration: 12 * time.Second, SlotsPerEpoch: 32}

	specs := map[string]*ChainSpec{
		"same duration":      hezeSpec(12*time.Second, 12*time.Second, 5),
		"far future epoch":   hezeSpec(12*time.Second, 9*time.Second, math.MaxUint64),
		"no heze duration":   hezeSpec(12*time.Second, 0, 5),
		"heze not scheduled": {SlotDuration: 12 * time.Second, SlotDurationHeze: 9 * time.Second, SlotsPerEpoch: 32},
	}

	for name, spec := range specs {
		t.Run(name, func(t *testing.T) {
			_, ok := spec.HezeForkSlot()
			require.False(t, ok)
			require.Equal(t, 12*time.Second, spec.ShortestSlotDuration())

			for _, slot := range []phase0.Slot{0, 1, 159, 160, 161, 1_000_000} {
				require.Equal(t, fixed.SlotStartOffset(slot), spec.SlotStartOffset(slot))
				require.Equal(t, time.Duration(slot)*12*time.Second, spec.SlotStartOffset(slot))
				require.Equal(t, fixed.SlotDurationAt(slot), spec.SlotDurationAt(slot))

				offset := spec.SlotStartOffset(slot) + 11*time.Second
				require.Equal(t, fixed.SlotAtOffset(offset), spec.SlotAtOffset(offset))
				require.Equal(t, slot, spec.SlotAtOffset(offset))
			}
		})
	}
}
