package epbs

import (
	"encoding/json"
	"sync"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/ethereum/go-ethereum/common"
)

// BuiltPayload represents an execution payload that we've built and can reveal.
type BuiltPayload struct {
	Slot             phase0.Slot
	BlockHash        phase0.Hash32
	ParentBlockHash  phase0.Hash32
	ParentBlockRoot  phase0.Root
	ExecutionPayload json.RawMessage // Raw execution payload from engine API
	BlobsBundle      json.RawMessage // Raw blobs bundle if present
	BidValue         uint64
	FeeRecipient     common.Address
	Timestamp        uint64
	PrevRandao       phase0.Root
	GasLimit         uint64
}

// PayloadStore stores built execution payloads for later reveal.
type PayloadStore struct {
	payloads map[phase0.Slot]*BuiltPayload
	mu       sync.RWMutex
}

// NewPayloadStore creates a new payload store.
func NewPayloadStore() *PayloadStore {
	return &PayloadStore{
		payloads: make(map[phase0.Slot]*BuiltPayload, 16),
	}
}

// Store stores a built payload.
func (s *PayloadStore) Store(payload *BuiltPayload) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.payloads[payload.Slot] = payload
}

// Get retrieves a stored payload for a slot.
func (s *PayloadStore) Get(slot phase0.Slot) *BuiltPayload {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.payloads[slot]
}

// Delete removes a payload for a slot.
func (s *PayloadStore) Delete(slot phase0.Slot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.payloads, slot)
}

// Cleanup removes payloads older than the given slot.
func (s *PayloadStore) Cleanup(olderThan phase0.Slot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for slot := range s.payloads {
		if slot < olderThan {
			delete(s.payloads, slot)
		}
	}
}

// GetByBlockHash retrieves a stored payload by block hash.
func (s *PayloadStore) GetByBlockHash(blockHash phase0.Hash32) *BuiltPayload {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, payload := range s.payloads {
		if payload.BlockHash == blockHash {
			return payload
		}
	}

	return nil
}
