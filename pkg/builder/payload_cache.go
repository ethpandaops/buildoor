package builder

import (
	"sync"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

const (
	// DefaultCacheSize is the maximum number of (slot, blockHash) entries to keep.
	DefaultCacheSize = 1000
)

// slotHashKey is the composite key for the cache: (slot, blockHash).
// Multiple payloads for the same slot (built on different EL heads) each get their own entry.
type slotHashKey struct {
	Slot      phase0.Slot
	BlockHash phase0.Hash32
}

// PayloadCache stores built payloads keyed by (slot, blockHash).
type PayloadCache struct {
	payloads map[slotHashKey]*PayloadReadyEvent
	maxSize  int
	mu       sync.RWMutex
}

// NewPayloadCache creates a new payload cache with the specified maximum entries.
func NewPayloadCache(maxSize int) *PayloadCache {
	if maxSize <= 0 {
		maxSize = DefaultCacheSize
	}

	return &PayloadCache{
		payloads: make(map[slotHashKey]*PayloadReadyEvent, maxSize),
		maxSize:  maxSize,
	}
}

// Store stores a payload in the cache keyed by (slot, blockHash).
func (c *PayloadCache) Store(event *PayloadReadyEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.payloads[slotHashKey{event.Slot, event.BlockHash}] = event
	c.evictOld()
}

// Get returns the most-recently stored payload for a slot, or nil if none exists.
// When multiple payloads exist for a slot use GetAllForSlot.
func (c *PayloadCache) Get(slot phase0.Slot) *PayloadReadyEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var latest *PayloadReadyEvent
	for k, p := range c.payloads {
		if k.Slot == slot {
			if latest == nil || p.ReadyAt.After(latest.ReadyAt) {
				latest = p
			}
		}
	}

	return latest
}

// GetAllForSlot returns all payloads built for a slot (primary + any fallback).
func (c *PayloadCache) GetAllForSlot(slot phase0.Slot) []*PayloadReadyEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var result []*PayloadReadyEvent
	for k, p := range c.payloads {
		if k.Slot == slot {
			result = append(result, p)
		}
	}

	return result
}

// GetByBlockHash retrieves a payload by its EL block hash.
func (c *PayloadCache) GetByBlockHash(blockHash phase0.Hash32) *PayloadReadyEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for k, p := range c.payloads {
		if k.BlockHash == blockHash {
			return p
		}
	}

	return nil
}

// Delete removes all payloads for the given slot.
func (c *PayloadCache) Delete(slot phase0.Slot) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for k := range c.payloads {
		if k.Slot == slot {
			delete(c.payloads, k)
		}
	}
}

// GetAll returns all cached payloads.
func (c *PayloadCache) GetAll() []*PayloadReadyEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]*PayloadReadyEvent, 0, len(c.payloads))
	for _, p := range c.payloads {
		result = append(result, p)
	}

	return result
}

// Size returns the number of entries in the cache.
func (c *PayloadCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.payloads)
}

// Cleanup removes all entries older than olderThan.
func (c *PayloadCache) Cleanup(olderThan phase0.Slot) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for k := range c.payloads {
		if k.Slot < olderThan {
			delete(c.payloads, k)
		}
	}
}

// evictOld removes the oldest entries when the cache exceeds its size limit.
// Must be called with the write lock held.
func (c *PayloadCache) evictOld() {
	for len(c.payloads) > c.maxSize {
		var oldest slotHashKey
		first := true

		for k := range c.payloads {
			if first || k.Slot < oldest.Slot {
				oldest = k
				first = false
			}
		}

		delete(c.payloads, oldest)
	}
}
