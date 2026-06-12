// Package validators provides types and storage for Builder API validator registrations.
package validators

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/db"
)

// Store holds validator registrations by pubkey (in-memory), optionally
// write-through persisted to a state-db so they survive restarts.
type Store struct {
	mu      sync.RWMutex
	byPK    map[phase0.BLSPubKey]*apiv1.SignedValidatorRegistration
	stateDB *db.Database
	log     logrus.FieldLogger
}

// NewStore creates a new in-memory validator registration store.
func NewStore() *Store {
	return &Store{
		byPK: make(map[phase0.BLSPubKey]*apiv1.SignedValidatorRegistration),
	}
}

// SetStateDB attaches an optional state-db for write-through persistence and
// loads any previously-persisted registrations into memory.
func (s *Store) SetStateDB(stateDB *db.Database, log logrus.FieldLogger) {
	s.mu.Lock()
	s.stateDB = stateDB
	s.log = log.WithField("component", "validator-store")
	s.mu.Unlock()

	if err := s.loadFromDB(); err != nil {
		s.log.WithError(err).Warn("failed to load validator registrations from state-db")
	}
}

// loadFromDB rehydrates registrations from the state-db.
func (s *Store) loadFromDB() error {
	if !s.stateDB.Enabled() {
		return nil
	}

	regs, err := s.stateDB.GetValidatorRegistrations()
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	loaded := 0

	for _, r := range regs {
		var reg apiv1.SignedValidatorRegistration
		if err := json.Unmarshal([]byte(r.Raw), &reg); err != nil {
			continue
		}

		if reg.Message == nil {
			continue
		}

		s.byPK[reg.Message.Pubkey] = &reg
		loaded++
	}

	if loaded > 0 {
		s.log.WithField("count", loaded).Info("loaded validator registrations from state-db")
	}

	return nil
}

// Put stores a signed validator registration by its message pubkey.
// Replaces any existing registration for the same pubkey, and write-through
// persists it when a state-db is attached.
func (s *Store) Put(reg *apiv1.SignedValidatorRegistration) {
	if reg == nil || reg.Message == nil {
		return
	}

	s.mu.Lock()
	s.byPK[reg.Message.Pubkey] = reg
	stateDB := s.stateDB
	log := s.log
	s.mu.Unlock()

	if stateDB == nil {
		return
	}

	raw, err := json.Marshal(reg)
	if err != nil {
		return
	}

	if err := stateDB.PutValidatorRegistration(db.ValidatorRegistration{
		Pubkey:       fmt.Sprintf("%#x", reg.Message.Pubkey),
		FeeRecipient: fmt.Sprintf("%#x", reg.Message.FeeRecipient),
		GasLimit:     reg.Message.GasLimit,
		Timestamp:    reg.Message.Timestamp.Unix(),
		Raw:          string(raw),
		UpdatedAt:    time.Now().UnixMilli(),
	}); err != nil && log != nil {
		log.WithError(err).Debug("failed to persist validator registration")
	}
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
