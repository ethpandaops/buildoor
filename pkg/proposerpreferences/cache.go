// Package proposerpreferences handles listening to, validating, and caching proposer preferences
// received via the P2P gossip network.
package proposerpreferences

import (
	"sync"

	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

// Cache stores proposer preferences by slot. Thread-safe.
// Only the first valid preference per slot is stored (subsequent ones for the same slot are ignored).
type Cache struct {
	mu          sync.RWMutex
	preferences map[phase0.Slot]*gloas.SignedProposerPreferences
}

// NewCache creates an empty proposer preferences cache.
func NewCache() *Cache {
	return &Cache{
		preferences: make(map[phase0.Slot]*gloas.SignedProposerPreferences),
	}
}

// Add stores proposer preferences for a slot. If the slot already exists,
// the existing value is kept and false is returned.
func (c *Cache) Add(slot phase0.Slot, prefs *gloas.SignedProposerPreferences) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.preferences[slot]; ok {
		return false
	}

	c.preferences[slot] = prefs

	return true
}

// Get returns proposer preferences for a slot.
func (c *Cache) Get(slot phase0.Slot) (*gloas.SignedProposerPreferences, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	pref, ok := c.preferences[slot]

	return pref, ok
}

// Has returns true if proposer preferences for the slot already exist.
func (c *Cache) Has(slot phase0.Slot) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	_, ok := c.preferences[slot]

	return ok
}

// PruneBefore removes all proposer preferences for slots before the provided slot.
func (c *Cache) PruneBefore(slot phase0.Slot) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for s := range c.preferences {
		if s < slot {
			delete(c.preferences, s)
		}
	}
}

// GetAll returns a copy of all cached proposer preferences.
func (c *Cache) GetAll() map[phase0.Slot]*gloas.SignedProposerPreferences {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[phase0.Slot]*gloas.SignedProposerPreferences, len(c.preferences))
	for k, v := range c.preferences {
		result[k] = v
	}

	return result
}

// Clear removes all cached proposer preferences.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.preferences = make(map[phase0.Slot]*gloas.SignedProposerPreferences)
}
