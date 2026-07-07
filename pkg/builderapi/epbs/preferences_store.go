package epbs

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/memstore"
)

// PreferencesNamespace is the kv_store namespace holding the per-validator
// builder preferences (max_execution_payment).
const PreferencesNamespace = "builder_preferences"

// BuilderPreferencesStore holds the latest per-validator builder preferences
// submitted via the submitBuilderPreferences API. It keeps only the most recent
// max_execution_payment for each validator pubkey (a later submission overwrites
// an earlier one) and optionally persists them to the state-db so they survive
// restarts.
//
// Per the Gloas builder-specs, if no preferences have been submitted for a
// validator, the builder MUST treat its max_execution_payment as 0; GetOrDefault
// encodes that rule.
type BuilderPreferencesStore struct {
	store *memstore.Store[phase0.BLSPubKey, phase0.Gwei]
}

// NewBuilderPreferencesStore creates an empty BuilderPreferencesStore.
func NewBuilderPreferencesStore() *BuilderPreferencesStore {
	return &BuilderPreferencesStore{
		store: memstore.New[phase0.BLSPubKey, phase0.Gwei](),
	}
}

// SetPersistence attaches the optional state-db so preferences survive
// restarts: previously persisted entries are loaded and future changes are
// flushed (buffered) into the store's kv_store namespace. Call Stop before the
// state-db closes.
func (s *BuilderPreferencesStore) SetPersistence(ctx context.Context, stateDB *db.Database,
	log logrus.FieldLogger) {
	if stateDB == nil {
		return
	}

	s.store.SetPersistence(ctx,
		db.NewKVPersistence(stateDB, PreferencesNamespace, PreferencesCodec{}),
		log.WithField("component", "builder-preferences-store"))
}

// Stop flushes pending changes and stops the persistence flush loop. No-op
// when no persistence is attached.
func (s *BuilderPreferencesStore) Stop() {
	s.store.Stop()
}

// Set records the latest max_execution_payment for a validator, overwriting any
// previously stored value.
func (s *BuilderPreferencesStore) Set(pubkey phase0.BLSPubKey, maxExecutionPayment phase0.Gwei) {
	s.store.Put(pubkey, maxExecutionPayment)
}

// Get returns the stored max_execution_payment for a validator and whether a
// preference was found.
func (s *BuilderPreferencesStore) Get(pubkey phase0.BLSPubKey) (phase0.Gwei, bool) {
	return s.store.Get(pubkey)
}

// GetOrDefault returns the stored max_execution_payment for a validator, or 0 if
// none has been submitted — the spec-mandated default that disallows execution
// layer payments.
func (s *BuilderPreferencesStore) GetOrDefault(pubkey phase0.BLSPubKey) phase0.Gwei {
	prefs, ok := s.store.Get(pubkey)
	if !ok {
		return 0
	}

	return prefs
}

// GetAll returns a snapshot copy of all stored builder preferences, keyed by
// validator pubkey.
func (s *BuilderPreferencesStore) GetAll() map[phase0.BLSPubKey]phase0.Gwei {
	return s.store.Entries()
}

// PreferencesCodec translates the builder preference store's entries to their
// persisted form: 0x-hex pubkey keys, 8-byte little-endian uint64 values.
type PreferencesCodec struct{}

var _ db.KVCodec[phase0.BLSPubKey, phase0.Gwei] = PreferencesCodec{}

// EncodeKey encodes a validator pubkey as its 0x-prefixed hex string form.
func (PreferencesCodec) EncodeKey(pubkey phase0.BLSPubKey) string {
	return "0x" + hex.EncodeToString(pubkey[:])
}

// DecodeKey parses a 0x-prefixed hex pubkey string.
func (PreferencesCodec) DecodeKey(key string) (phase0.BLSPubKey, error) {
	var pubkey phase0.BLSPubKey

	raw, err := hex.DecodeString(strings.TrimPrefix(key, "0x"))
	if err != nil {
		return pubkey, fmt.Errorf("invalid builder preference pubkey key %q: %w", key, err)
	}

	if len(raw) != len(pubkey) {
		return pubkey, fmt.Errorf("invalid builder preference pubkey key %q: got %d bytes, want %d",
			key, len(raw), len(pubkey))
	}

	copy(pubkey[:], raw)

	return pubkey, nil
}

// EncodeValue encodes a max_execution_payment as 8 little-endian bytes.
func (PreferencesCodec) EncodeValue(maxExecutionPayment phase0.Gwei) ([]byte, error) {
	value := make([]byte, 8)
	binary.LittleEndian.PutUint64(value, uint64(maxExecutionPayment))

	return value, nil
}

// DecodeValue decodes an 8-byte little-endian max_execution_payment.
func (PreferencesCodec) DecodeValue(value []byte) (phase0.Gwei, error) {
	if len(value) != 8 {
		return 0, fmt.Errorf("invalid builder preference value: got %d bytes, want 8", len(value))
	}

	return phase0.Gwei(binary.LittleEndian.Uint64(value)), nil
}
