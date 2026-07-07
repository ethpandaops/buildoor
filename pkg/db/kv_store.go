package db

import (
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/memstore"
)

// KVCodec translates a store's key/value types to their persisted form.
// Implementations live with the flavor owner (the module that manages the
// data); values are opaque blobs (typically SSZ-encoded).
type KVCodec[K comparable, V any] interface {
	EncodeKey(key K) string
	DecodeKey(key string) (K, error)
	EncodeValue(value V) ([]byte, error)
	DecodeValue(value []byte) (V, error)
}

// KVPersistence is the single generic memstore.Persistence implementation
// over the namespaced kv_store table: Load reads a namespace, PersistBatch
// applies upserts and deletes in a single transaction. Honors the disabled
// no-op mode (Load returns empty, PersistBatch returns nil).
type KVPersistence[K comparable, V any] struct {
	db        *Database
	namespace string
	codec     KVCodec[K, V]
}

var _ memstore.Persistence[string, []byte] = (*KVPersistence[string, []byte])(nil)

// NewKVPersistence creates a persistence adapter for one kv_store namespace.
func NewKVPersistence[K comparable, V any](d *Database, namespace string,
	codec KVCodec[K, V]) *KVPersistence[K, V] {
	return &KVPersistence[K, V]{
		db:        d,
		namespace: namespace,
		codec:     codec,
	}
}

// kvRow is a raw kv_store row.
type kvRow struct {
	Key   string `db:"key"`
	Value []byte `db:"value"`
}

// Load returns all decodable entries of the namespace. Undecodable rows are
// skipped with a debug log (best-effort cache data). Returns an empty map
// when the database is disabled.
func (p *KVPersistence[K, V]) Load() (map[K]V, error) {
	if !p.db.Enabled() {
		return map[K]V{}, nil
	}

	rows := []kvRow{}

	err := p.db.readerDB.Select(&rows, `
		SELECT key, value
		FROM kv_store
		WHERE namespace = $1`, p.namespace)
	if err != nil {
		return nil, fmt.Errorf("error loading kv_store namespace %q: %w", p.namespace, err)
	}

	entries := make(map[K]V, len(rows))

	for _, row := range rows {
		key, err := p.codec.DecodeKey(row.Key)
		if err != nil {
			p.db.logger.WithError(err).WithFields(logrus.Fields{
				"namespace": p.namespace,
				"key":       row.Key,
			}).Debug("skipping kv_store row with undecodable key")

			continue
		}

		value, err := p.codec.DecodeValue(row.Value)
		if err != nil {
			p.db.logger.WithError(err).WithFields(logrus.Fields{
				"namespace": p.namespace,
				"key":       row.Key,
			}).Debug("skipping kv_store row with undecodable value")

			continue
		}

		entries[key] = value
	}

	return entries, nil
}

// PersistBatch upserts and deletes the given entries in a single transaction.
// Values that fail to encode are skipped with a warning (retrying them can
// never succeed). No-op when the database is disabled.
func (p *KVPersistence[K, V]) PersistBatch(upserts map[K]V, deletes []K) error {
	if !p.db.Enabled() {
		return nil
	}

	if len(upserts) == 0 && len(deletes) == 0 {
		return nil
	}

	type kvUpsert struct {
		key   string
		value []byte
	}

	// Encode outside the transaction; a permanently unencodable value must
	// not fail (and endlessly retry) the whole batch.
	encoded := make([]kvUpsert, 0, len(upserts))

	for key, value := range upserts {
		encodedKey := p.codec.EncodeKey(key)

		encodedValue, err := p.codec.EncodeValue(value)
		if err != nil {
			p.db.logger.WithError(err).WithFields(logrus.Fields{
				"namespace": p.namespace,
				"key":       encodedKey,
			}).Warn("skipping unencodable kv_store value")

			continue
		}

		encoded = append(encoded, kvUpsert{key: encodedKey, value: encodedValue})
	}

	now := time.Now().Unix()

	return p.db.RunDBTransaction(func(tx *sqlx.Tx) error {
		for _, upsert := range encoded {
			_, err := tx.Exec(`
				INSERT INTO kv_store (namespace, key, value, updated_at)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (namespace, key) DO UPDATE SET
					value = excluded.value,
					updated_at = excluded.updated_at`,
				p.namespace, upsert.key, upsert.value, now)
			if err != nil {
				return fmt.Errorf("error upserting kv_store entry %q/%q: %w",
					p.namespace, upsert.key, err)
			}
		}

		for _, key := range deletes {
			encodedKey := p.codec.EncodeKey(key)

			_, err := tx.Exec(`
				DELETE FROM kv_store
				WHERE namespace = $1 AND key = $2`, p.namespace, encodedKey)
			if err != nil {
				return fmt.Errorf("error deleting kv_store entry %q/%q: %w",
					p.namespace, encodedKey, err)
			}
		}

		return nil
	})
}
