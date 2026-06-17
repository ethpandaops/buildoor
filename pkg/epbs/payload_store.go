package epbs

import (
	"sync"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// PayloadStore retains built payloads until they are revealed, keyed by proposal
// slot. It holds the canonical *payload_builder.Payload by reference — the heavy
// payload is never copied.
type PayloadStore struct {
	payloads map[phase0.Slot]*payload_builder.Payload
	mu       sync.RWMutex
}

// NewPayloadStore creates a new payload store.
func NewPayloadStore() *PayloadStore {
	return &PayloadStore{
		payloads: make(map[phase0.Slot]*payload_builder.Payload, 16),
	}
}

// Store retains a built payload, keyed by its proposal slot.
func (s *PayloadStore) Store(p *payload_builder.Payload) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.payloads[p.Attributes.ProposalSlot] = p
}

// Get retrieves a stored payload for a slot.
func (s *PayloadStore) Get(slot phase0.Slot) *payload_builder.Payload {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.payloads[slot]
}

// GetByBlockHash retrieves a stored payload by its execution block hash.
func (s *PayloadStore) GetByBlockHash(blockHash phase0.Hash32) *payload_builder.Payload {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, p := range s.payloads {
		if p.BlockHash == blockHash {
			return p
		}
	}

	return nil
}

// Delete removes a payload for a slot.
func (s *PayloadStore) Delete(slot phase0.Slot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.payloads, slot)
}

// Cleanup removes payloads for slots older than the given slot.
func (s *PayloadStore) Cleanup(olderThan phase0.Slot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for slot := range s.payloads {
		if slot < olderThan {
			delete(s.payloads, slot)
		}
	}
}
