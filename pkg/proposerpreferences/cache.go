// Package proposerpreferences handles listening to, validating, and caching proposer preferences
// received via the P2P gossip network.
package proposerpreferences

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/db"
)

// Cache stores proposer preferences by slot. Thread-safe.
// Only the first valid preference per slot is stored (subsequent ones for the same slot are ignored).
type Cache struct {
	mu          sync.RWMutex
	preferences map[phase0.Slot]*gloas.SignedProposerPreferences
	stateDB     *db.Database
	log         logrus.FieldLogger
}

// NewCache creates an empty proposer preferences cache.
func NewCache() *Cache {
	return &Cache{
		preferences: make(map[phase0.Slot]*gloas.SignedProposerPreferences),
	}
}

// SetStateDB attaches an optional state-db for best-effort write-through
// persistence and loads any previously-persisted preferences into the cache.
func (c *Cache) SetStateDB(stateDB *db.Database, log logrus.FieldLogger) {
	c.mu.Lock()
	c.stateDB = stateDB
	c.log = log.WithField("component", "proposer-preferences-cache")
	c.mu.Unlock()

	c.loadFromDB()
}

// loadFromDB rehydrates cached preferences from the state-db.
func (c *Cache) loadFromDB() {
	if !c.stateDB.Enabled() {
		return
	}

	prefs, err := c.stateDB.GetProposerPreferences(0)
	if err != nil {
		c.log.WithError(err).Warn("failed to load proposer preferences from state-db")
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, p := range prefs {
		var sp gloas.SignedProposerPreferences
		if err := json.Unmarshal([]byte(p.Raw), &sp); err != nil {
			continue
		}

		c.preferences[phase0.Slot(p.Slot)] = &sp
	}
}

// Add stores proposer preferences for a slot. If the slot already exists,
// the existing value is kept and false is returned.
func (c *Cache) Add(slot phase0.Slot, prefs *gloas.SignedProposerPreferences) bool {
	c.mu.Lock()

	if _, ok := c.preferences[slot]; ok {
		c.mu.Unlock()
		return false
	}

	c.preferences[slot] = prefs
	stateDB := c.stateDB
	c.mu.Unlock()

	if stateDB != nil {
		c.persist(slot, prefs)
	}

	return true
}

// persist write-through stores a preference to the state-db (best-effort).
func (c *Cache) persist(slot phase0.Slot, prefs *gloas.SignedProposerPreferences) {
	if prefs == nil || prefs.Message == nil {
		return
	}

	raw, err := json.Marshal(prefs)
	if err != nil {
		return
	}

	if err := c.stateDB.PutProposerPreference(db.ProposerPreference{
		Slot:           uint64(slot),
		ValidatorIndex: uint64(prefs.Message.ValidatorIndex),
		FeeRecipient:   fmt.Sprintf("0x%x", prefs.Message.FeeRecipient),
		TargetGasLimit: prefs.Message.TargetGasLimit,
		Raw:            string(raw),
	}); err != nil && c.log != nil {
		c.log.WithError(err).Debug("failed to persist proposer preference")
	}
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
