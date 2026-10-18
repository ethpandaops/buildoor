package rpc

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/memstore"
)

// KeysNamespace is the kv_store namespace persisting the ingress API keys.
const KeysNamespace = "rpc_api_keys"

// keySecretPrefix marks buildoor ingress keys, so a leaked one is recognizable.
const keySecretPrefix = "bdr_"

// ErrKeyNotFound is returned for operations on an unknown key id.
var ErrKeyNotFound = errors.New("api key not found")

// APIKey is one ingress API key. Only the SHA-256 of the secret is kept; the
// secret itself is shown once, at creation.
type APIKey struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Hash   string `json:"hash"`
	Prefix string `json:"prefix"` // first characters of the secret, for recognition
	// RateLimit overrides txpool.rpc_key_rate for this key, in calls per
	// second (0 = unlimited); nil follows the default.
	RateLimit *uint64   `json:"rate_limit,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

// KeyCodec stores API keys as JSON in the kv_store.
type KeyCodec struct{}

var _ db.KVCodec[string, *APIKey] = KeyCodec{}

// EncodeKey implements db.KVCodec.
func (KeyCodec) EncodeKey(id string) string { return id }

// DecodeKey implements db.KVCodec.
func (KeyCodec) DecodeKey(key string) (string, error) { return key, nil }

// EncodeValue implements db.KVCodec.
func (KeyCodec) EncodeValue(key *APIKey) ([]byte, error) { return json.Marshal(key) }

// DecodeValue implements db.KVCodec.
func (KeyCodec) DecodeValue(raw []byte) (*APIKey, error) {
	key := &APIKey{}
	if err := json.Unmarshal(raw, key); err != nil {
		return nil, fmt.Errorf("invalid api key record: %w", err)
	}

	return key, nil
}

// KeyStore manages the ingress API keys on a (persisted) memstore.
type KeyStore struct {
	store *memstore.Store[string, *APIKey]
}

// NewKeyStore wraps the store holding the keys by id.
func NewKeyStore(store *memstore.Store[string, *APIKey]) *KeyStore {
	return &KeyStore{store: store}
}

// List returns the keys, oldest first.
func (k *KeyStore) List() []*APIKey {
	keys := k.store.Values()
	sort.Slice(keys, func(i, j int) bool { return keys[i].CreatedAt.Before(keys[j].CreatedAt) })

	return keys
}

// Create generates a key and returns it with its secret (never stored).
func (k *KeyStore) Create(label string, rateLimit *uint64) (*APIKey, string, error) {
	id, err := randomHex(6)
	if err != nil {
		return nil, "", err
	}

	body, err := randomHex(24)
	if err != nil {
		return nil, "", err
	}

	secret := keySecretPrefix + body

	key := &APIKey{
		ID:        id,
		Label:     strings.TrimSpace(label),
		Hash:      hashSecret(secret),
		Prefix:    secret[:len(keySecretPrefix)+6],
		RateLimit: rateLimit,
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
	}

	k.store.Put(id, key)

	return key, secret, nil
}

// KeyUpdate is a partial key update; nil fields stay unchanged.
type KeyUpdate struct {
	Label   *string
	Enabled *bool
	// RateLimit sets the override; ClearRateLimit returns to the default.
	RateLimit      *uint64
	ClearRateLimit bool
}

// Update applies a partial update and returns the stored key.
func (k *KeyStore) Update(id string, update KeyUpdate) (*APIKey, error) {
	current, ok := k.store.Get(id)
	if !ok {
		return nil, ErrKeyNotFound
	}

	next := *current

	if update.Label != nil {
		next.Label = strings.TrimSpace(*update.Label)
	}

	if update.Enabled != nil {
		next.Enabled = *update.Enabled
	}

	if update.ClearRateLimit {
		next.RateLimit = nil
	} else if update.RateLimit != nil {
		limit := *update.RateLimit
		next.RateLimit = &limit
	}

	k.store.Put(id, &next)

	return &next, nil
}

// Delete removes a key.
func (k *KeyStore) Delete(id string) error {
	if !k.store.Has(id) {
		return ErrKeyNotFound
	}

	k.store.Delete(id)

	return nil
}

// Lookup returns the key whose secret this is, or nil.
func (k *KeyStore) Lookup(secret string) *APIKey {
	if !strings.HasPrefix(secret, keySecretPrefix) {
		return nil
	}

	hash := []byte(hashSecret(secret))

	for _, key := range k.store.Values() {
		if subtle.ConstantTimeCompare(hash, []byte(key.Hash)) == 1 {
			return key
		}
	}

	return nil
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))

	return hex.EncodeToString(sum[:])
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}

	return hex.EncodeToString(buf), nil
}
