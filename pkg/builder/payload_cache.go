package builder

import (
	"sync"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

const (
	// DefaultCacheSize is the number of slots to keep in the cache.
	DefaultCacheSize = 1000
)

// payloadKey is the composite cache key. FULL and EMPTY variants for the same
// slot end up under different keys because their ParentBlockHash differs
// (FULL uses bid.block_hash, EMPTY uses bid.parent_block_hash).
type payloadKey struct {
	parentBlockRoot phase0.Root
	parentBlockHash phase0.Hash32
	slot            phase0.Slot
}

// PayloadCache stores built payloads keyed by (parent_block_root, parent_block_hash, slot).
// When more than one event arrives for the same key, the highest-value one is retained.
type PayloadCache struct {
	payloads map[payloadKey]*PayloadReadyEvent
	maxSlots int
	mu       sync.RWMutex
}

// NewPayloadCache creates a new payload cache with the specified maximum slots.
func NewPayloadCache(maxSlots int) *PayloadCache {
	if maxSlots <= 0 {
		maxSlots = DefaultCacheSize
	}

	return &PayloadCache{
		payloads: make(map[payloadKey]*PayloadReadyEvent, maxSlots),
		maxSlots: maxSlots,
	}
}

func keyFor(event *PayloadReadyEvent) payloadKey {
	return payloadKey{
		parentBlockRoot: event.ParentBlockRoot,
		parentBlockHash: event.ParentBlockHash,
		slot:            event.Slot,
	}
}

// Store stores a payload in the cache. If an entry already exists for the same
// (parent_block_root, parent_block_hash, slot) key, the higher-value event wins.
func (c *PayloadCache) Store(event *PayloadReadyEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := keyFor(event)
	if existing, ok := c.payloads[key]; ok && existing.BlockValue >= event.BlockValue {
		return
	}

	c.payloads[key] = event
	c.evictOld()
}

// Get retrieves the highest-value cached payload for the given slot, regardless
// of variant. Used by callers (e.g. Builder API pre-Gloas) that don't care
// about FULL vs EMPTY.
func (c *PayloadCache) Get(slot phase0.Slot) *PayloadReadyEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var best *PayloadReadyEvent
	for k, p := range c.payloads {
		if k.slot != slot {
			continue
		}
		if best == nil || p.BlockValue > best.BlockValue {
			best = p
		}
	}

	return best
}

// GetByVariant retrieves the cached payload for a specific (slot, variant) pair.
// Returns nil if no payload was built for that variant.
func (c *PayloadCache) GetByVariant(slot phase0.Slot, variant PayloadVariant) *PayloadReadyEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for k, p := range c.payloads {
		if k.slot == slot && p.Variant == variant {
			return p
		}
	}

	return nil
}

// GetAllForSlot returns every cached payload for the given slot (one per variant
// at most under normal operation).
func (c *PayloadCache) GetAllForSlot(slot phase0.Slot) []*PayloadReadyEvent {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]*PayloadReadyEvent, 0, 2)
	for k, p := range c.payloads {
		if k.slot == slot {
			out = append(out, p)
		}
	}

	return out
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

// Delete removes all cached payloads for the given slot (every variant).
func (c *PayloadCache) Delete(slot phase0.Slot) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for k := range c.payloads {
		if k.slot == slot {
			delete(c.payloads, k)
		}
	}
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

// evictOld removes payloads belonging to the oldest slots once we exceed maxSlots
// distinct slot count. Must be called with the lock held.
func (c *PayloadCache) evictOld() {
	slots := make(map[phase0.Slot]struct{}, len(c.payloads))
	for k := range c.payloads {
		slots[k.slot] = struct{}{}
	}

	for len(slots) > c.maxSlots {
		var oldest phase0.Slot
		first := true
		for s := range slots {
			if first || s < oldest {
				oldest = s
				first = false
			}
		}

		for k := range c.payloads {
			if k.slot == oldest {
				delete(c.payloads, k)
			}
		}
		delete(slots, oldest)
	}
}

// Cleanup removes payloads older than the given slot.
func (c *PayloadCache) Cleanup(olderThan phase0.Slot) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for k := range c.payloads {
		if k.slot < olderThan {
			delete(c.payloads, k)
		}
	}
}
