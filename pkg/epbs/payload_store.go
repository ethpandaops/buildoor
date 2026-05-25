package epbs

import (
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
)

// BuiltPayload represents an execution payload that we've built and can reveal.
// Payload, BlobsBundle, and ExecutionRequests are stored typed; marshal to JSON only when submitting to beacon.
type BuiltPayload struct {
	Slot              phase0.Slot
	BlockHash         phase0.Hash32
	ParentBlockHash   phase0.Hash32
	ParentBlockRoot   phase0.Root
	ExecutionPayload  *engine.ExecutionPayload // Typed execution payload
	BlobsBundle       *engine.BlobsBundle      // Typed blobs bundle if present
	ExecutionRequests engine.ExecutionRequests // Typed execution requests (Electra/Fulu)
	BidValue          *big.Int
	FeeRecipient      common.Address
	Timestamp         uint64
	PrevRandao        phase0.Root
	GasLimit          uint64
}

// PayloadStore stores built execution payloads for later reveal, keyed by block hash.
// Multiple payloads per slot are supported (primary + fallback builds).
type PayloadStore struct {
	payloads map[phase0.Hash32]*BuiltPayload // blockHash → payload
	mu       sync.RWMutex
}

// NewPayloadStore creates a new payload store.
func NewPayloadStore() *PayloadStore {
	return &PayloadStore{
		payloads: make(map[phase0.Hash32]*BuiltPayload, 32),
	}
}

// Store stores a built payload keyed by its block hash.
func (s *PayloadStore) Store(payload *BuiltPayload) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.payloads[payload.BlockHash] = payload
}

// GetByBlockHash retrieves a payload by its exact EL block hash.
// This is the primary lookup path used during reveal: the accepted bid's block_hash
// identifies which payload to reveal.
func (s *PayloadStore) GetByBlockHash(blockHash phase0.Hash32) *BuiltPayload {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.payloads[blockHash]
}

// Get returns any one payload for the given slot (most recently stored).
// Prefer GetByBlockHash when the accepted bid's block_hash is known.
func (s *PayloadStore) Get(slot phase0.Slot) *BuiltPayload {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var latest *BuiltPayload
	for _, p := range s.payloads {
		if p.Slot == slot {
			if latest == nil || p.Timestamp > latest.Timestamp {
				latest = p
			}
		}
	}

	return latest
}

// GetAllForSlot returns all payloads built for a slot (primary + fallback).
func (s *PayloadStore) GetAllForSlot(slot phase0.Slot) []*BuiltPayload {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*BuiltPayload
	for _, p := range s.payloads {
		if p.Slot == slot {
			result = append(result, p)
		}
	}

	return result
}

// Delete removes all payloads for the given slot.
func (s *PayloadStore) Delete(slot phase0.Slot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for hash, p := range s.payloads {
		if p.Slot == slot {
			delete(s.payloads, hash)
		}
	}
}

// Cleanup removes payloads older than the given slot.
func (s *PayloadStore) Cleanup(olderThan phase0.Slot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for hash, p := range s.payloads {
		if p.Slot < olderThan {
			delete(s.payloads, hash)
		}
	}
}
