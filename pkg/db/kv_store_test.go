package db

import (
	"errors"
	"io"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// toyCodec is a trivial KVCodec[string, string] for tests.
type toyCodec struct{}

func (toyCodec) EncodeKey(key string) string { return key }

func (toyCodec) DecodeKey(key string) (string, error) { return key, nil }

func (toyCodec) EncodeValue(value string) ([]byte, error) { return []byte(value), nil }

func (toyCodec) DecodeValue(value []byte) (string, error) { return string(value), nil }

// pickyCodec wraps toyCodec but fails to decode values equal to "poison".
type pickyCodec struct {
	toyCodec
}

func (pickyCodec) DecodeValue(value []byte) (string, error) {
	if string(value) == "poison" {
		return "", errors.New("undecodable value")
	}

	return string(value), nil
}

func TestKVPersistenceRoundTrip(t *testing.T) {
	d := testDB(t)
	p := NewKVPersistence[string, string](d, "ns1", toyCodec{})

	// Upsert two entries in one batch.
	require.NoError(t, p.PersistBatch(map[string]string{"a": "1", "b": "2"}, nil))

	entries, err := p.Load()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"a": "1", "b": "2"}, entries)

	// Update one and delete the other in a single batch.
	require.NoError(t, p.PersistBatch(map[string]string{"a": "updated"}, []string{"b"}))

	entries, err = p.Load()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"a": "updated"}, entries)

	// Empty batches are a no-op.
	require.NoError(t, p.PersistBatch(map[string]string{}, nil))
}

func TestKVPersistenceNamespaceIsolation(t *testing.T) {
	d := testDB(t)
	ns1 := NewKVPersistence[string, string](d, "ns1", toyCodec{})
	ns2 := NewKVPersistence[string, string](d, "ns2", toyCodec{})

	// The same key holds different values per namespace.
	require.NoError(t, ns1.PersistBatch(map[string]string{"key": "one"}, nil))
	require.NoError(t, ns2.PersistBatch(map[string]string{"key": "two"}, nil))

	entries1, err := ns1.Load()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"key": "one"}, entries1)

	entries2, err := ns2.Load()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"key": "two"}, entries2)

	// Deleting in one namespace leaves the other untouched.
	require.NoError(t, ns1.PersistBatch(nil, []string{"key"}))

	entries1, err = ns1.Load()
	require.NoError(t, err)
	require.Empty(t, entries1)

	entries2, err = ns2.Load()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"key": "two"}, entries2)
}

func TestKVPersistenceSkipsUndecodableRows(t *testing.T) {
	d := testDB(t)

	writer := NewKVPersistence[string, string](d, "ns", toyCodec{})
	require.NoError(t, writer.PersistBatch(map[string]string{
		"good": "value",
		"bad":  "poison",
	}, nil))

	reader := NewKVPersistence[string, string](d, "ns", pickyCodec{})

	entries, err := reader.Load()
	require.NoError(t, err)
	require.Equal(t, map[string]string{"good": "value"}, entries)
}

func TestKVPersistenceDisabledNoOps(t *testing.T) {
	log := logrus.New()
	log.SetOutput(io.Discard)

	d := NewDatabase(&Config{File: ""}, log)
	require.NoError(t, d.Init())
	require.False(t, d.Enabled())

	p := NewKVPersistence[string, string](d, "ns", toyCodec{})

	// Writes are dropped, reads are empty — never panic.
	require.NoError(t, p.PersistBatch(map[string]string{"key": "value"}, []string{"other"}))

	entries, err := p.Load()
	require.NoError(t, err)
	require.NotNil(t, entries)
	require.Empty(t, entries)
}
