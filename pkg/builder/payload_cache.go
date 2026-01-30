package builder

import (
	"sync"

	"github.com/attestantio/go-eth2-client/spec/phase0"
)

const (
	// DefaultCacheSize is the number of slots to keep in the cache.
	DefaultCacheSize = 1000
)

// PayloadCache stores built payloads for a limited number of slots.
// It uses a simple LRU-like approach, keeping only the most recent slots.
type PayloadCache struct {
	payloads map[phase0.Slot]*PayloadReadyEvent
	maxSlots int
	mu       sync.RWMutex
}

// NewPayloadCache creates a new payload cache with the specified maximum slots.
func NewPayloadCache(maxSlots int) *PayloadCache {
	if maxSlots <= 0 {
		maxSlots = DefaultCacheSize
	}

	return &PayloadCache{
		payloads: make(map[phase0.Slot]*PayloadReadyEvent, maxSlots),
		maxSlots: maxSlots,
	}
}

// Store stores a payload in the cache.
// It automatically evicts old payloads to maintain the size limit.
func (c *PayloadCache) Store(event *PayloadReadyEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.payloads[event.Slot] = event
	c.evictOld(event.Slot)
}

// Get retrieves a payload for the given slot.
func (c *PayloadCache) Get(slot phase0.Slot) *PayloadReadyEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.payloads[slot]
}

// GetByBlockHash retrieves a payload by its block hash.
func (c *PayloadCache) GetByBlockHash(blockHash phase0.Hash32) *PayloadReadyEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, payload := range c.payloads {
		if payload.BlockHash == blockHash {
			return payload
		}
	}

	return nil
}

// Delete removes a payload for the given slot.
func (c *PayloadCache) Delete(slot phase0.Slot) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.payloads, slot)
}

// GetAll returns all cached payloads.
func (c *PayloadCache) GetAll() []*PayloadReadyEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]*PayloadReadyEvent, 0, len(c.payloads))
	for _, payload := range c.payloads {
		result = append(result, payload)
	}

	return result
}

// Size returns the number of payloads in the cache.
func (c *PayloadCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.payloads)
}

// evictOld removes payloads older than the retention limit.
// Must be called with lock held.
func (c *PayloadCache) evictOld(_ phase0.Slot) {
	if len(c.payloads) <= c.maxSlots {
		return
	}

	// Find and remove the oldest slots beyond our limit
	var oldestSlot phase0.Slot

	for slot := range c.payloads {
		if oldestSlot == 0 || slot < oldestSlot {
			oldestSlot = slot
		}
	}

	// Keep evicting until we're at the limit
	for len(c.payloads) > c.maxSlots {
		delete(c.payloads, oldestSlot)

		// Find next oldest
		oldestSlot = 0

		for slot := range c.payloads {
			if oldestSlot == 0 || slot < oldestSlot {
				oldestSlot = slot
			}
		}
	}
}

// Cleanup removes payloads older than the given slot.
func (c *PayloadCache) Cleanup(olderThan phase0.Slot) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for slot := range c.payloads {
		if slot < olderThan {
			delete(c.payloads, slot)
		}
	}
}
