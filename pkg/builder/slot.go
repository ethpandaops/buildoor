package builder

import (
	"sync"

	"github.com/attestantio/go-eth2-client/spec/phase0"
)

// SlotManager handles slot scheduling decisions.
type SlotManager struct {
	cfg         *Config
	currentSlot phase0.Slot
	slotsBuilt  uint64
	startSlot   phase0.Slot
	mu          sync.Mutex
}

// NewSlotManager creates a new slot manager.
func NewSlotManager(cfg *Config) *SlotManager {
	return &SlotManager{
		cfg:       cfg,
		startSlot: phase0.Slot(cfg.Schedule.StartSlot),
	}
}

// ShouldBuildForSlot returns true if we should build for the given slot.
func (m *SlotManager) ShouldBuildForSlot(slot phase0.Slot) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check start slot
	if m.startSlot > 0 && slot < m.startSlot {
		return false
	}

	switch m.cfg.Schedule.Mode {
	case ScheduleModeAll:
		return true

	case ScheduleModeEveryN:
		if m.cfg.Schedule.EveryNth == 0 {
			return true
		}

		// Calculate slots since start
		var slotsSinceStart uint64
		if m.startSlot > 0 {
			slotsSinceStart = uint64(slot - m.startSlot)
		} else {
			slotsSinceStart = uint64(slot)
		}

		return slotsSinceStart%m.cfg.Schedule.EveryNth == 0

	case ScheduleModeNextN:
		if m.cfg.Schedule.NextN == 0 {
			return false
		}

		return m.slotsBuilt < m.cfg.Schedule.NextN

	default:
		return true
	}
}

// OnSlotBuilt records that a slot was built.
func (m *SlotManager) OnSlotBuilt(slot phase0.Slot) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.currentSlot = slot
	m.slotsBuilt++
}

// UpdateConfig updates the slot manager configuration.
func (m *SlotManager) UpdateConfig(cfg *Config) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cfg = cfg

	// Update start slot if changed
	if cfg.Schedule.StartSlot > 0 {
		m.startSlot = phase0.Slot(cfg.Schedule.StartSlot)
	}

	// Reset slots built if mode changed to next_n
	if cfg.Schedule.Mode == ScheduleModeNextN {
		m.slotsBuilt = 0
	}
}

// GetSlotsRemaining returns the number of slots remaining to build.
// Returns -1 if unlimited.
func (m *SlotManager) GetSlotsRemaining() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg.Schedule.Mode != ScheduleModeNextN {
		return -1
	}

	if m.slotsBuilt >= m.cfg.Schedule.NextN {
		return 0
	}

	return int(m.cfg.Schedule.NextN - m.slotsBuilt)
}

// GetSlotsBuilt returns the number of slots built.
func (m *SlotManager) GetSlotsBuilt() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.slotsBuilt
}

// GetCurrentSlot returns the current slot.
func (m *SlotManager) GetCurrentSlot() phase0.Slot {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.currentSlot
}
