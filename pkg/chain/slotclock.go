package chain

import (
	"cmp"
	"math"
	"slices"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
)

// slotDurationKeys maps each fork that may change the slot duration to its
// config key (EIP-8198): the duration is in effect from the fork's epoch.
var slotDurationKeys = map[version.DataVersion]string{
	version.DataVersionHeze: "SLOT_DURATION_MS_HEZE",
}

// SlotSegment is a span of the chain with a constant slot duration. It lasts
// until the start of the next segment; the last one is open-ended.
type SlotSegment struct {
	Epoch    phase0.Epoch  // first epoch of the segment
	Slot     phase0.Slot   // first slot of the segment
	Offset   time.Duration // start of the segment relative to genesis
	Duration time.Duration // slot duration within the segment
}

// buildSlotSegments derives the slot timeline with every cutoff
// pre-calculated, so lookups never walk the fork schedule: one segment from
// genesis plus one per fork that changes the slot duration. A fork at epoch 0
// replaces the genesis duration; a fork at an epoch whose start cannot be
// represented (far future) is ignored.
func (s *ChainSpec) buildSlotSegments() []SlotSegment {
	segments := []SlotSegment{{Duration: s.SlotDuration}}
	if s.SlotsPerEpoch == 0 || len(s.ForkSlotDurations) == 0 {
		return segments
	}

	type change struct {
		fork     version.DataVersion
		epoch    phase0.Epoch
		duration time.Duration
	}

	changes := make([]change, 0, len(s.ForkSlotDurations))

	for fork, duration := range s.ForkSlotDurations {
		epoch := s.GetForkEpoch(fork)
		if duration <= 0 || uint64(epoch) > math.MaxUint64/s.SlotsPerEpoch {
			continue
		}

		changes = append(changes, change{fork: fork, epoch: epoch, duration: duration})
	}

	// Forks at the same epoch: the later fork's duration wins.
	slices.SortFunc(changes, func(a, b change) int {
		if c := cmp.Compare(a.epoch, b.epoch); c != 0 {
			return c
		}

		return cmp.Compare(a.fork, b.fork)
	})

	for _, c := range changes {
		last := &segments[len(segments)-1]

		if c.epoch == last.Epoch {
			last.Duration = c.duration

			continue
		}

		if c.duration == last.Duration {
			continue
		}

		slot := phase0.Slot(uint64(c.epoch) * s.SlotsPerEpoch)

		offset := segmentSlotOffset(last, slot)
		if offset == math.MaxInt64 {
			break
		}

		segments = append(segments, SlotSegment{
			Epoch:    c.epoch,
			Slot:     slot,
			Offset:   offset,
			Duration: c.duration,
		})
	}

	// Merge neighbours that a same-epoch override left with equal durations.
	merged := segments[:1]
	for _, segment := range segments[1:] {
		if segment.Duration != merged[len(merged)-1].Duration {
			merged = append(merged, segment)
		}
	}

	return merged
}

// segmentSlotOffset returns the start of slot (within or after segment)
// relative to genesis, saturating at math.MaxInt64.
func segmentSlotOffset(segment *SlotSegment, slot phase0.Slot) time.Duration {
	if segment.Duration <= 0 {
		return segment.Offset
	}

	slots := uint64(slot - segment.Slot)
	if slots > uint64((math.MaxInt64-segment.Offset)/segment.Duration) {
		return math.MaxInt64
	}

	return segment.Offset + time.Duration(slots)*segment.Duration
}

// segments returns the cached timeline. Specs built without ParseChainSpec
// (test literals) derive it on every call.
func (s *ChainSpec) segments() []SlotSegment {
	if s.slotSegments != nil {
		return s.slotSegments
	}

	return s.buildSlotSegments()
}

// SlotSegments returns a copy of the slot timeline, oldest segment first.
func (s *ChainSpec) SlotSegments() []SlotSegment {
	return slices.Clone(s.segments())
}

// segmentOfSlot returns the segment containing slot.
func segmentOfSlot(segments []SlotSegment, slot phase0.Slot) *SlotSegment {
	for i := len(segments) - 1; i > 0; i-- {
		if slot >= segments[i].Slot {
			return &segments[i]
		}
	}

	return &segments[0]
}

// SegmentOfSlot returns the timeline segment containing slot.
func (s *ChainSpec) SegmentOfSlot(slot phase0.Slot) SlotSegment {
	return *segmentOfSlot(s.segments(), slot)
}

// SlotDurationAt returns the duration of the given slot.
func (s *ChainSpec) SlotDurationAt(slot phase0.Slot) time.Duration {
	return segmentOfSlot(s.segments(), slot).Duration
}

// SlotStartOffset returns the start of the given slot relative to genesis,
// saturating for slots beyond the representable time range.
func (s *ChainSpec) SlotStartOffset(slot phase0.Slot) time.Duration {
	return segmentSlotOffset(segmentOfSlot(s.segments(), slot), slot)
}

// SlotAtOffset returns the slot in progress at the given offset from genesis
// (0 before genesis).
func (s *ChainSpec) SlotAtOffset(offset time.Duration) phase0.Slot {
	if offset < 0 {
		return 0
	}

	segments := s.segments()
	segment := &segments[0]

	for i := len(segments) - 1; i > 0; i-- {
		if offset >= segments[i].Offset {
			segment = &segments[i]

			break
		}
	}

	if segment.Duration <= 0 {
		return segment.Slot
	}

	return segment.Slot + phase0.Slot((offset-segment.Offset)/segment.Duration)
}

// EpochStartSlot returns the first slot of the given epoch, saturating at the
// highest slot.
func (s *ChainSpec) EpochStartSlot(epoch phase0.Epoch) phase0.Slot {
	if s.SlotsPerEpoch == 0 || uint64(epoch) > math.MaxUint64/s.SlotsPerEpoch {
		return math.MaxUint64
	}

	return phase0.Slot(uint64(epoch) * s.SlotsPerEpoch)
}

// SlotDurationsBetween returns the distinct slot durations of the inclusive
// slot range, in timeline order.
func (s *ChainSpec) SlotDurationsBetween(from, to phase0.Slot) []time.Duration {
	segments := s.segments()
	durations := make([]time.Duration, 0, len(segments))

	for i, segment := range segments {
		if segment.Slot > to {
			break
		}

		if i+1 < len(segments) && segments[i+1].Slot <= from {
			continue
		}

		if !slices.Contains(durations, segment.Duration) {
			durations = append(durations, segment.Duration)
		}
	}

	return durations
}
