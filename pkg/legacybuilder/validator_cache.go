package legacybuilder

import (
	"sync"

	"github.com/attestantio/go-eth2-client/spec/phase0"
)

// ValidatorCache caches relay validator registrations keyed by proposal slot.
type ValidatorCache struct {
	registrations map[phase0.Slot]*ValidatorRegistration
	mu            sync.RWMutex
}

// NewValidatorCache creates a new validator cache.
func NewValidatorCache() *ValidatorCache {
	return &ValidatorCache{
		registrations: make(map[phase0.Slot]*ValidatorRegistration, 64),
	}
}

// Update replaces the cache with fresh registration data from relays.
func (c *ValidatorCache) Update(regs []ValidatorRegistration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Clear and repopulate
	c.registrations = make(map[phase0.Slot]*ValidatorRegistration, len(regs))

	for i := range regs {
		reg := regs[i]
		c.registrations[reg.Slot] = &reg
	}
}

// GetRegistration returns the validator registration for a given slot, if any.
func (c *ValidatorCache) GetRegistration(slot phase0.Slot) *ValidatorRegistration {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.registrations[slot]
}

// Len returns the number of cached registrations.
func (c *ValidatorCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.registrations)
}

// Cleanup removes registrations older than the given slot.
func (c *ValidatorCache) Cleanup(olderThan phase0.Slot) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for slot := range c.registrations {
		if slot < olderThan {
			delete(c.registrations, slot)
		}
	}
}
