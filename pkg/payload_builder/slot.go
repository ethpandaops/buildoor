package payload_builder

import (
	"sync"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

// SlotManager handles slot scheduling decisions.
type SlotManager struct {
	cfg         *config.Config
	currentSlot phase0.Slot
	slotsBuilt  uint64
	mu          sync.Mutex
}

// NewSlotManager creates a new slot manager.
func NewSlotManager(cfg *config.Config) *SlotManager {
	return &SlotManager{
		cfg: cfg,
	}
}

// ShouldBuildForSlot returns true if we should build for the given slot.
func (m *SlotManager) ShouldBuildForSlot(slot phase0.Slot) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Read the start slot live from config so UI overrides take effect immediately.
	startSlot := phase0.Slot(m.cfg.Schedule.StartSlot)

	// Check start slot
	if startSlot > 0 && slot < startSlot {
		return false
	}

	switch m.cfg.Schedule.Mode {
	case config.ScheduleModeAll:
		return true

	case config.ScheduleModeEveryN:
		if m.cfg.Schedule.EveryNth == 0 {
			return true
		}

		// Calculate slots since start
		var slotsSinceStart uint64
		if startSlot > 0 {
			slotsSinceStart = uint64(slot - startSlot)
		} else {
			slotsSinceStart = uint64(slot)
		}

		return slotsSinceStart%m.cfg.Schedule.EveryNth == 0

	case config.ScheduleModeNextN:
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
func (m *SlotManager) UpdateConfig(cfg *config.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cfg = cfg

	// Reset slots built if mode changed to next_n
	if cfg.Schedule.Mode == config.ScheduleModeNextN {
		m.slotsBuilt = 0
	}
}

// GetSlotsRemaining returns the number of slots remaining to build.
// Returns -1 if unlimited.
func (m *SlotManager) GetSlotsRemaining() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg.Schedule.Mode != config.ScheduleModeNextN {
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
