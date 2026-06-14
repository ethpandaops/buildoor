package builderapi

import (
	"maps"
	"sync"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

// BuilderPreferencesStore holds the latest per-validator builder preferences
// submitted via the submitBuilderPreferences API. It keeps only the most recent
// max_execution_payment for each validator pubkey (a later submission overwrites
// an earlier one).
//
// Per the Gloas builder-specs, if no preferences have been submitted for a
// validator, the builder MUST treat its max_execution_payment as 0; GetOrDefault
// encodes that rule.
type BuilderPreferencesStore struct {
	mu    sync.RWMutex
	prefs map[phase0.BLSPubKey]phase0.Gwei
}

// NewBuilderPreferencesStore creates an empty BuilderPreferencesStore.
func NewBuilderPreferencesStore() *BuilderPreferencesStore {
	return &BuilderPreferencesStore{
		prefs: make(map[phase0.BLSPubKey]phase0.Gwei),
	}
}

// Set records the latest max_execution_payment for a validator, overwriting any
// previously stored value.
func (s *BuilderPreferencesStore) Set(pubkey phase0.BLSPubKey, maxExecutionPayment phase0.Gwei) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prefs[pubkey] = maxExecutionPayment
}

// Get returns the stored max_execution_payment for a validator and whether a
// preference was found.
func (s *BuilderPreferencesStore) Get(pubkey phase0.BLSPubKey) (phase0.Gwei, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.prefs[pubkey]
	return v, ok
}

// GetOrDefault returns the stored max_execution_payment for a validator, or 0 if
// none has been submitted — the spec-mandated default that disallows execution
// layer payments.
func (s *BuilderPreferencesStore) GetOrDefault(pubkey phase0.BLSPubKey) phase0.Gwei {
	s.mu.RLock()
	defer s.mu.RUnlock()
	prefs, ok := s.prefs[pubkey]
	if !ok {
		return 0
	}
	return prefs
}

// GetAll returns a snapshot copy of all stored builder preferences, keyed by
// validator pubkey.
func (s *BuilderPreferencesStore) GetAll() map[phase0.BLSPubKey]phase0.Gwei {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[phase0.BLSPubKey]phase0.Gwei, len(s.prefs))
	maps.Copy(out, s.prefs)
	return out
}
