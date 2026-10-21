package chain

import (
	"math"
	"testing"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/stretchr/testify/require"
)

// forkDurationSpec builds a 32-slot-epoch spec whose slot duration changes at
// the given forks.
func forkDurationSpec(genesis time.Duration, changes map[version.DataVersion]struct {
	epoch    phase0.Epoch
	duration time.Duration
}) *ChainSpec {
	spec := &ChainSpec{SlotDuration: genesis, SlotsPerEpoch: 32}

	for fork, change := range changes {
		spec.ForkSchedule = append(spec.ForkSchedule, ForkSchedule{Fork: fork, Epoch: change.epoch})

		if spec.ForkSlotDurations == nil {
			spec.ForkSlotDurations = make(map[version.DataVersion]time.Duration, len(changes))
		}

		spec.ForkSlotDurations[fork] = change.duration
	}

	return spec
}

type forkChange = struct {
	epoch    phase0.Epoch
	duration time.Duration
}

func hezeSpec(genesis, heze time.Duration, forkEpoch phase0.Epoch) *ChainSpec {
	return forkDurationSpec(genesis, map[version.DataVersion]forkChange{
		version.DataVersionHeze: {epoch: forkEpoch, duration: heze},
	})
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
	require.Equal(t, 9*time.Second, spec.ForkSlotDurations[version.DataVersionHeze])
	require.Equal(t, []SlotSegment{
		{Duration: 12 * time.Second},
		{Epoch: 5, Slot: 160, Offset: 160 * 12 * time.Second, Duration: 9 * time.Second},
	}, spec.SlotSegments())

	spec, err = ParseChainSpec(map[string]string{
		"SLOT_DURATION_MS": "12000",
		"SLOTS_PER_EPOCH":  "32",
	}, nil)
	require.NoError(t, err)
	require.Empty(t, spec.ForkSlotDurations)
	require.Equal(t, []SlotSegment{{Duration: 12 * time.Second}}, spec.SlotSegments())

	_, err = ParseChainSpec(map[string]string{
		"SLOT_DURATION_MS":      "12000",
		"SLOT_DURATION_MS_HEZE": "0",
		"SLOTS_PER_EPOCH":       "32",
	}, nil)
	require.ErrorContains(t, err, "SLOT_DURATION_MS_HEZE")
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
	require.Equal(t, forkSlot, spec.SegmentOfSlot(forkSlot+5).Slot)
	require.Equal(t, phase0.Slot(0), spec.SegmentOfSlot(forkSlot-1).Slot)
}

func TestSlotClockMultipleChanges(t *testing.T) {
	// Two changes: 12s → 10s at epoch 2, → 6s at epoch 4.
	spec := forkDurationSpec(12*time.Second, map[version.DataVersion]forkChange{
		version.DataVersionGloas: {epoch: 2, duration: 10 * time.Second},
		version.DataVersionHeze:  {epoch: 4, duration: 6 * time.Second},
	})

	require.Equal(t, []SlotSegment{
		{Duration: 12 * time.Second},
		{Epoch: 2, Slot: 64, Offset: 64 * 12 * time.Second, Duration: 10 * time.Second},
		{Epoch: 4, Slot: 128, Offset: 64*12*time.Second + 64*10*time.Second, Duration: 6 * time.Second},
	}, spec.SlotSegments())

	for slot := phase0.Slot(0); slot < 200; slot++ {
		start := spec.SlotStartOffset(slot)
		require.Equal(t, slot, spec.SlotAtOffset(start))
		require.Equal(t, slot, spec.SlotAtOffset(start+spec.SlotDurationAt(slot)-1))
		require.Equal(t, start+spec.SlotDurationAt(slot), spec.SlotStartOffset(slot+1))
	}

	require.Equal(t, []time.Duration{12 * time.Second, 10 * time.Second, 6 * time.Second},
		spec.SlotDurationsBetween(0, math.MaxUint64))
	require.Equal(t, []time.Duration{10 * time.Second}, spec.SlotDurationsBetween(64, 127))
	require.Equal(t, []time.Duration{12 * time.Second, 10 * time.Second}, spec.SlotDurationsBetween(63, 64))
	require.Equal(t, []time.Duration{6 * time.Second}, spec.SlotDurationsBetween(500, 600))
}

func TestSlotClockSegmentEdgeCases(t *testing.T) {
	t.Run("change at genesis", func(t *testing.T) {
		spec := hezeSpec(12*time.Second, 9*time.Second, 0)
		require.Equal(t, []SlotSegment{{Duration: 9 * time.Second}}, spec.SlotSegments())
		require.Equal(t, 90*time.Second, spec.SlotStartOffset(10))
	})

	t.Run("same epoch, later fork wins", func(t *testing.T) {
		spec := forkDurationSpec(12*time.Second, map[version.DataVersion]forkChange{
			version.DataVersionGloas: {epoch: 3, duration: 9 * time.Second},
			version.DataVersionHeze:  {epoch: 3, duration: 12 * time.Second},
		})
		require.Equal(t, []SlotSegment{{Duration: 12 * time.Second}}, spec.SlotSegments())
	})

	t.Run("saturating offsets", func(t *testing.T) {
		spec := hezeSpec(12*time.Second, 9*time.Second, 5)
		require.Equal(t, time.Duration(math.MaxInt64), spec.SlotStartOffset(math.MaxUint64))
		require.Equal(t, phase0.Slot(math.MaxUint64), spec.EpochStartSlot(math.MaxUint64))
	})
}

func TestSlotClockWithoutHezeChange(t *testing.T) {
	fixed := &ChainSpec{SlotDuration: 12 * time.Second, SlotsPerEpoch: 32}

	specs := map[string]*ChainSpec{
		"same duration":    hezeSpec(12*time.Second, 12*time.Second, 5),
		"far future epoch": hezeSpec(12*time.Second, 9*time.Second, math.MaxUint64),
		"heze not scheduled": {
			SlotDuration:      12 * time.Second,
			SlotsPerEpoch:     32,
			ForkSlotDurations: map[version.DataVersion]time.Duration{version.DataVersionHeze: 9 * time.Second},
		},
	}

	for name, spec := range specs {
		t.Run(name, func(t *testing.T) {
			require.Len(t, spec.SlotSegments(), 1)

			for _, slot := range []phase0.Slot{0, 1, 159, 160, 161, 1_000_000} {
				require.Equal(t, fixed.SlotStartOffset(slot), spec.SlotStartOffset(slot))
				require.Equal(t, time.Duration(slot)*12*time.Second, spec.SlotStartOffset(slot))
				require.Equal(t, fixed.SlotDurationAt(slot), spec.SlotDurationAt(slot))

				offset := spec.SlotStartOffset(slot) + 11*time.Second
				require.Equal(t, slot, spec.SlotAtOffset(offset))
			}
		})
	}
}
