package payload_builder

import (
	"sync"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

const (
	// DefaultCacheSize is the number of slots to keep in the cache.
	DefaultCacheSize = 1000
)

// candidatePriority orders candidate keys from most to least canonical; it
// decides which of a slot's payloads Get returns as the primary one.
var candidatePriority = []chain.CandidateKey{
	chain.CandidateParentFull,
	chain.CandidateParentEmpty,
	chain.CandidateGrandparentFull,
	chain.CandidateGrandparentEmpty,
}

// PayloadCache stores built payloads per slot, keyed by the parent tuple they
// were built on: reorg and payload-miss handling can produce several
// candidate payloads for the same slot.
type PayloadCache struct {
	payloads map[phase0.Slot]map[beacon.AttrParentKey]*Payload
	maxSlots int
	mu       sync.RWMutex
}

// NewPayloadCache creates a new payload cache with the specified maximum slots.
func NewPayloadCache(maxSlots int) *PayloadCache {
	if maxSlots <= 0 {
		maxSlots = DefaultCacheSize
	}

	return &PayloadCache{
		payloads: make(map[phase0.Slot]map[beacon.AttrParentKey]*Payload, maxSlots),
		maxSlots: maxSlots,
	}
}

// Store stores a payload in the cache (replacing an earlier build on the same
// parent tuple) and evicts old slots to maintain the size limit.
func (c *PayloadCache) Store(event *Payload) {
	c.mu.Lock()
	defer c.mu.Unlock()

	slot := event.Attributes.ProposalSlot

	variants := c.payloads[slot]
	if variants == nil {
		variants = make(map[beacon.AttrParentKey]*Payload, 2)
		c.payloads[slot] = variants
	}

	variants[beacon.AttrParentKeyOf(event.Attributes)] = event
	c.evictOld()
}

// Get retrieves the slot's primary payload: the most canonical classified
// candidate, falling back to the most recently built unclassified one.
func (c *PayloadCache) Get(slot phase0.Slot) *Payload {
	c.mu.RLock()
	defer c.mu.RUnlock()

	variants := c.payloads[slot]
	if len(variants) == 0 {
		return nil
	}

	byCandidate := make(map[chain.CandidateKey]*Payload, len(variants))

	var newestUnclassified *Payload

	for _, payload := range variants {
		if payload.Candidate != "" {
			byCandidate[payload.Candidate] = payload
			continue
		}

		if newestUnclassified == nil || payload.ReadyAt.After(newestUnclassified.ReadyAt) {
			newestUnclassified = payload
		}
	}

	for _, key := range candidatePriority {
		if payload, ok := byCandidate[key]; ok {
			return payload
		}
	}

	return newestUnclassified
}

// GetCandidate retrieves the slot's payload built for the given candidate key.
func (c *PayloadCache) GetCandidate(slot phase0.Slot, key chain.CandidateKey) *Payload {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, payload := range c.payloads[slot] {
		if payload.Candidate == key {
			return payload
		}
	}

	return nil
}

// GetVariant retrieves the slot's payload built on the given parent tuple.
func (c *PayloadCache) GetVariant(slot phase0.Slot, key beacon.AttrParentKey) *Payload {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.payloads[slot][key]
}

// GetSlotPayloads returns all payloads built for the slot (arbitrary order).
func (c *PayloadCache) GetSlotPayloads(slot phase0.Slot) []*Payload {
	c.mu.RLock()
	defer c.mu.RUnlock()

	variants := c.payloads[slot]
	result := make([]*Payload, 0, len(variants))

	for _, payload := range variants {
		result = append(result, payload)
	}

	return result
}

// GetByBlockHash retrieves a payload by its block hash.
func (c *PayloadCache) GetByBlockHash(blockHash phase0.Hash32) *Payload {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, variants := range c.payloads {
		for _, payload := range variants {
			if payload.BlockHash == blockHash {
				return payload
			}
		}
	}

	return nil
}

// Delete removes all payloads for the given slot.
func (c *PayloadCache) Delete(slot phase0.Slot) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.payloads, slot)
}

// GetAll returns all cached payloads.
func (c *PayloadCache) GetAll() []*Payload {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]*Payload, 0, len(c.payloads))

	for _, variants := range c.payloads {
		for _, payload := range variants {
			result = append(result, payload)
		}
	}

	return result
}

// Size returns the number of slots with cached payloads.
func (c *PayloadCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.payloads)
}

// evictOld removes the oldest slots beyond the retention limit.
// Must be called with lock held.
func (c *PayloadCache) evictOld() {
	for len(c.payloads) > c.maxSlots {
		var oldestSlot phase0.Slot

		first := true

		for slot := range c.payloads {
			if first || slot < oldestSlot {
				oldestSlot = slot
				first = false
			}
		}

		delete(c.payloads, oldestSlot)
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
