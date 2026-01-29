package legacybuilder

import (
	"sync"

	"github.com/attestantio/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/config"
)

// SlotScheduler handles slot scheduling decisions for the legacy builder.
type SlotScheduler struct {
	cfg        *config.LegacyBuilderConfig
	slotsBuilt uint64
	startSlot  phase0.Slot
	mu         sync.Mutex
}

// NewSlotScheduler creates a new slot scheduler.
func NewSlotScheduler(cfg *config.LegacyBuilderConfig) *SlotScheduler {
	return &SlotScheduler{
		cfg:       cfg,
		startSlot: phase0.Slot(cfg.Schedule.StartSlot),
	}
}

// ShouldBuildForSlot returns true if we should build for the given slot.
func (s *SlotScheduler) ShouldBuildForSlot(slot phase0.Slot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check start slot
	if s.startSlot > 0 && slot < s.startSlot {
		return false
	}

	switch s.cfg.Schedule.Mode {
	case config.ScheduleModeAll:
		return true

	case config.ScheduleModeEveryN:
		if s.cfg.Schedule.EveryNth == 0 {
			return true
		}

		// Calculate slots since start
		var slotsSinceStart uint64
		if s.startSlot > 0 {
			slotsSinceStart = uint64(slot - s.startSlot)
		} else {
			slotsSinceStart = uint64(slot)
		}

		return slotsSinceStart%s.cfg.Schedule.EveryNth == 0

	case config.ScheduleModeNextN:
		if s.cfg.Schedule.NextN == 0 {
			return false
		}

		return s.slotsBuilt < s.cfg.Schedule.NextN

	default:
		return true
	}
}

// OnSlotBuilt records that a slot was built.
func (s *SlotScheduler) OnSlotBuilt() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.slotsBuilt++
}

// UpdateConfig updates the scheduler configuration.
func (s *SlotScheduler) UpdateConfig(cfg *config.LegacyBuilderConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cfg = cfg

	// Update start slot if changed
	if cfg.Schedule.StartSlot > 0 {
		s.startSlot = phase0.Slot(cfg.Schedule.StartSlot)
	}

	// Reset slots built if mode changed to next_n
	if cfg.Schedule.Mode == config.ScheduleModeNextN {
		s.slotsBuilt = 0
	}
}

// GetSlotsRemaining returns the number of slots remaining to build.
// Returns -1 if unlimited.
func (s *SlotScheduler) GetSlotsRemaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cfg.Schedule.Mode != config.ScheduleModeNextN {
		return -1
	}

	if s.slotsBuilt >= s.cfg.Schedule.NextN {
		return 0
	}

	return int(s.cfg.Schedule.NextN - s.slotsBuilt)
}
