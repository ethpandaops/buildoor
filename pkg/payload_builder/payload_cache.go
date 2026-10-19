package payload_builder

import (
	"sync"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

const (
	// DefaultCacheSize is the number of slots to keep in the cache.
	DefaultCacheSize = 100
	// blobRetentionSlots is how long a cached payload keeps its blobs and
	// proofs: they serve unblinding and the reveal, both due within the
	// payload's slot (a reorg re-binding a reveal follows within a slot or
	// two). The commitments stay with the payload.
	blobRetentionSlots = 8
)

// candidatePriority orders candidate keys from most to least canonical; it
// decides which of a slot's selected payloads Get returns as the primary one.
var candidatePriority = []chain.CandidateKey{
	chain.CandidateParentFull,
	chain.CandidateParentEmpty,
	chain.CandidateGrandparentFull,
	chain.CandidateGrandparentEmpty,
}

// PayloadCache holds every payload the builder built, once, keyed by its
// block hash — the engine build's and every testing run's alike, whether
// selected or not: a bid made on any of them must unblind, reveal and be
// recognised when included.
//
// Apart from the payloads it keeps each build target's selection: the block
// hash of the payload that feeds the target's bids and reveals now. The
// lookups by slot, candidate or parent tuple resolve a target's selection;
// GetByBlockHash finds any payload.
type PayloadCache struct {
	mu       sync.RWMutex
	payloads map[phase0.Hash32]*Payload
	// slots indexes the payloads by slot (eviction, blob release).
	slots map[phase0.Slot][]phase0.Hash32
	// selected maps a slot's build targets (parent tuple) to the block hash
	// of their selected payload.
	selected map[phase0.Slot]map[beacon.AttrParentKey]phase0.Hash32
	maxSlots int
	// blobsReleasedBefore: the payloads of the slots below it have released
	// their blobs.
	blobsReleasedBefore phase0.Slot
}

// NewPayloadCache creates a new payload cache with the specified maximum slots.
func NewPayloadCache(maxSlots int) *PayloadCache {
	if maxSlots <= 0 {
		maxSlots = DefaultCacheSize
	}

	return &PayloadCache{
		payloads: make(map[phase0.Hash32]*Payload, maxSlots*2),
		slots:    make(map[phase0.Slot][]phase0.Hash32, maxSlots),
		selected: make(map[phase0.Slot]map[beacon.AttrParentKey]phase0.Hash32, maxSlots),
		maxSlots: maxSlots,
	}
}

// Store adds a built payload and evicts old slots to maintain the size limit.
// Storing a payload does not select it.
func (c *PayloadCache) Store(payload *Payload) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.storeLocked(payload)
}

func (c *PayloadCache) storeLocked(payload *Payload) {
	if _, ok := c.payloads[payload.BlockHash]; ok {
		return
	}

	slot := payload.Attributes.ProposalSlot

	c.payloads[payload.BlockHash] = payload
	c.slots[slot] = append(c.slots[slot], payload.BlockHash)
	c.evictOld()

	if slot > blobRetentionSlots {
		c.releaseBlobsBefore(slot - blobRetentionSlots)
	}
}

// Select makes the payload its target's selection (storing it if needed).
func (c *PayloadCache) Select(payload *Payload) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.storeLocked(payload)

	slot := payload.Attributes.ProposalSlot

	targets := c.selected[slot]
	if targets == nil {
		targets = make(map[beacon.AttrParentKey]phase0.Hash32, 2)
		c.selected[slot] = targets
	}

	targets[beacon.AttrParentKeyOf(payload.Attributes)] = payload.BlockHash
}

// Withdraw drops the given payloads of a slot, and the target selections
// pointing at them: a superseded target's payloads must never be bid.
func (c *PayloadCache) Withdraw(slot phase0.Slot, hashes []phase0.Hash32) {
	c.mu.Lock()
	defer c.mu.Unlock()

	withdrawn := make(map[phase0.Hash32]bool, len(hashes))

	for _, hash := range hashes {
		if payload := c.payloads[hash]; payload != nil && payload.Attributes.ProposalSlot == slot {
			withdrawn[hash] = true
			delete(c.payloads, hash)
		}
	}

	if len(withdrawn) == 0 {
		return
	}

	kept := c.slots[slot][:0]
	for _, hash := range c.slots[slot] {
		if !withdrawn[hash] {
			kept = append(kept, hash)
		}
	}

	if len(kept) == 0 {
		delete(c.slots, slot)
	} else {
		c.slots[slot] = kept
	}

	for tuple, hash := range c.selected[slot] {
		if withdrawn[hash] {
			delete(c.selected[slot], tuple)
		}
	}

	if len(c.selected[slot]) == 0 {
		delete(c.selected, slot)
	}
}

// releaseBlobsBefore drops the blobs and proofs of the payloads of the slots
// below cutoff. Must hold mu.
func (c *PayloadCache) releaseBlobsBefore(cutoff phase0.Slot) {
	if cutoff <= c.blobsReleasedBefore {
		return
	}

	for slot, hashes := range c.slots {
		if slot >= cutoff || slot < c.blobsReleasedBefore {
			continue
		}

		for _, hash := range hashes {
			c.payloads[hash].BlobsBundle.release()
		}
	}

	c.blobsReleasedBefore = cutoff
}

// selectedLocked returns the slot's selected payloads by target. Must hold mu.
func (c *PayloadCache) selectedLocked(slot phase0.Slot) map[beacon.AttrParentKey]*Payload {
	targets := c.selected[slot]
	out := make(map[beacon.AttrParentKey]*Payload, len(targets))

	for tuple, hash := range targets {
		if payload := c.payloads[hash]; payload != nil {
			out[tuple] = payload
		}
	}

	return out
}

// Get returns the slot's primary selected payload: the most canonical
// classified target's, falling back to the most recently built unclassified
// one.
func (c *PayloadCache) Get(slot phase0.Slot) *Payload {
	c.mu.RLock()
	defer c.mu.RUnlock()

	selected := c.selectedLocked(slot)
	if len(selected) == 0 {
		return nil
	}

	byCandidate := make(map[chain.CandidateKey]*Payload, len(selected))

	var newestUnclassified *Payload

	for _, payload := range selected {
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

// GetCandidate returns the selected payload of the slot's target for the
// given candidate key.
func (c *PayloadCache) GetCandidate(slot phase0.Slot, key chain.CandidateKey) *Payload {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, payload := range c.selectedLocked(slot) {
		if payload.Candidate == key {
			return payload
		}
	}

	return nil
}

// GetVariant returns the selected payload of the slot's target on the given
// parent tuple.
func (c *PayloadCache) GetVariant(slot phase0.Slot, key beacon.AttrParentKey) *Payload {
	c.mu.RLock()
	defer c.mu.RUnlock()

	hash, ok := c.selected[slot][key]
	if !ok {
		return nil
	}

	return c.payloads[hash]
}

// GetSlotPayloads returns the selected payload of each of the slot's targets
// (arbitrary order).
func (c *PayloadCache) GetSlotPayloads(slot phase0.Slot) []*Payload {
	c.mu.RLock()
	defer c.mu.RUnlock()

	selected := c.selectedLocked(slot)
	result := make([]*Payload, 0, len(selected))

	for _, payload := range selected {
		result = append(result, payload)
	}

	return result
}

// GetByBlockHash returns any payload we built, selected or not.
func (c *PayloadCache) GetByBlockHash(blockHash phase0.Hash32) *Payload {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.payloads[blockHash]
}

// evictOld removes the oldest slots beyond the retention limit.
// Must be called with lock held.
func (c *PayloadCache) evictOld() {
	for len(c.slots) > c.maxSlots {
		var oldestSlot phase0.Slot

		first := true

		for slot := range c.slots {
			if first || slot < oldestSlot {
				oldestSlot = slot
				first = false
			}
		}

		c.deleteSlotLocked(oldestSlot)
	}
}

// deleteSlotLocked drops a slot's payloads and selections. Must hold mu.
func (c *PayloadCache) deleteSlotLocked(slot phase0.Slot) {
	for _, hash := range c.slots[slot] {
		delete(c.payloads, hash)
	}

	delete(c.slots, slot)
	delete(c.selected, slot)
}

// Cleanup removes payloads older than the given slot.
func (c *PayloadCache) Cleanup(olderThan phase0.Slot) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for slot := range c.slots {
		if slot < olderThan {
			c.deleteSlotLocked(slot)
		}
	}
}
