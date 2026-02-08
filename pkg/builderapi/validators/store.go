// Package validators provides types and storage for Builder API validator registrations.
package validators

import (
	"sync"

	apiv1 "github.com/attestantio/go-eth2-client/api/v1"
	"github.com/attestantio/go-eth2-client/spec/phase0"
)

// Store holds validator registrations by pubkey (in-memory).
type Store struct {
	mu   sync.RWMutex
	byPK map[phase0.BLSPubKey]*apiv1.SignedValidatorRegistration
}

// NewStore creates a new in-memory validator registration store.
func NewStore() *Store {
	return &Store{
		byPK: make(map[phase0.BLSPubKey]*apiv1.SignedValidatorRegistration),
	}
}

// Put stores a signed validator registration by its message pubkey.
// Replaces any existing registration for the same pubkey.
func (s *Store) Put(reg *apiv1.SignedValidatorRegistration) {
	if reg == nil || reg.Message == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byPK[reg.Message.Pubkey] = reg
}

// Get returns the registration for the given pubkey, or nil.
func (s *Store) Get(pubkey phase0.BLSPubKey) *apiv1.SignedValidatorRegistration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byPK[pubkey]
}

// Len returns the number of stored registrations.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byPK)
}

// List returns a copy of all stored validator registrations (unordered).
func (s *Store) List() []*apiv1.SignedValidatorRegistration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*apiv1.SignedValidatorRegistration, 0, len(s.byPK))
	for _, reg := range s.byPK {
		out = append(out, reg)
	}
	return out
}
