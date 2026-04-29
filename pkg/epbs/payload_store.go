package epbs

import (
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
)

// BuiltPayload represents an execution payload that we've built and can reveal.
// Payload, BlobsBundle, and ExecutionRequests are stored typed; marshal to JSON only when submitting to beacon.
type BuiltPayload struct {
	Slot              phase0.Slot
	Variant           builder.PayloadVariant
	BlockHash         phase0.Hash32
	ParentBlockHash   phase0.Hash32
	ParentBlockRoot   phase0.Root
	ExecutionPayload  *engine.ExecutionPayload // Typed execution payload
	BlobsBundle       *engine.BlobsBundle      // Typed blobs bundle if present
	ExecutionRequests engine.ExecutionRequests // Typed execution requests (Electra/Fulu)
	BidValue          uint64
	FeeRecipient      common.Address
	Timestamp         uint64
	PrevRandao        phase0.Root
	GasLimit          uint64
}

type storeKey struct {
	slot    phase0.Slot
	variant builder.PayloadVariant
}

// PayloadStore stores built execution payloads for later reveal, keyed by
// (slot, variant) so the FULL and EMPTY variants for the same slot coexist.
type PayloadStore struct {
	payloads map[storeKey]*BuiltPayload
	mu       sync.RWMutex
}

// NewPayloadStore creates a new payload store.
func NewPayloadStore() *PayloadStore {
	return &PayloadStore{
		payloads: make(map[storeKey]*BuiltPayload, 16),
	}
}

// Store stores a built payload.
func (s *PayloadStore) Store(payload *BuiltPayload) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.payloads[storeKey{slot: payload.Slot, variant: payload.Variant}] = payload
}

// Get retrieves a stored payload for a slot. When both variants are present,
// the FULL variant is returned by preference. Used by callers that don't
// care which variant they get (e.g. fallback paths).
func (s *PayloadStore) Get(slot phase0.Slot) *BuiltPayload {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if p, ok := s.payloads[storeKey{slot: slot, variant: builder.PayloadVariantFull}]; ok {
		return p
	}
	if p, ok := s.payloads[storeKey{slot: slot, variant: builder.PayloadVariantEmpty}]; ok {
		return p
	}

	return nil
}

// GetByVariant retrieves the stored payload for a specific (slot, variant) pair.
func (s *PayloadStore) GetByVariant(slot phase0.Slot, variant builder.PayloadVariant) *BuiltPayload {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.payloads[storeKey{slot: slot, variant: variant}]
}

// GetAllForSlot returns every variant stored for the given slot.
func (s *PayloadStore) GetAllForSlot(slot phase0.Slot) []*BuiltPayload {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*BuiltPayload, 0, 2)
	for k, p := range s.payloads {
		if k.slot == slot {
			out = append(out, p)
		}
	}

	return out
}

// Delete removes all variants for a slot.
func (s *PayloadStore) Delete(slot phase0.Slot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for k := range s.payloads {
		if k.slot == slot {
			delete(s.payloads, k)
		}
	}
}

// Cleanup removes payloads older than the given slot.
func (s *PayloadStore) Cleanup(olderThan phase0.Slot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for k := range s.payloads {
		if k.slot < olderThan {
			delete(s.payloads, k)
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
