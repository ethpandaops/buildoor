package epbs

import (
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuilderPreferencesStore(t *testing.T) {
	store := NewBuilderPreferencesStore()

	var pk1, pk2 phase0.BLSPubKey
	pk1[0] = 1
	pk2[0] = 2

	// Absent → GetOrDefault is 0, Get reports not found.
	assert.Equal(t, phase0.Gwei(0), store.GetOrDefault(pk1))
	_, ok := store.Get(pk1)
	assert.False(t, ok)

	// Set then read back.
	store.Set(pk1, 100)
	got, ok := store.Get(pk1)
	require.True(t, ok)
	assert.Equal(t, phase0.Gwei(100), got)
	assert.Equal(t, phase0.Gwei(100), store.GetOrDefault(pk1))

	// Overwrite keeps only the latest value.
	store.Set(pk1, 250)
	got, _ = store.Get(pk1)
	assert.Equal(t, phase0.Gwei(250), got)

	store.Set(pk2, 7)

	// GetAll returns a snapshot of all entries.
	all := store.GetAll()
	require.Len(t, all, 2)
	assert.Equal(t, phase0.Gwei(250), all[pk1])
	assert.Equal(t, phase0.Gwei(7), all[pk2])

	// The snapshot is a copy — mutating it must not affect the store.
	all[pk1] = 999
	got, _ = store.Get(pk1)
	assert.Equal(t, phase0.Gwei(250), got)
}

// TestPreferencesCodecRoundTrip pins the persisted key/value encoding of
// builder preferences.
func TestPreferencesCodecRoundTrip(t *testing.T) {
	codec := PreferencesCodec{}

	var pubkey phase0.BLSPubKey
	for i := range pubkey {
		pubkey[i] = byte(i)
	}

	// Key round trip (0x-hex pubkey).
	key := codec.EncodeKey(pubkey)
	assert.Regexp(t, "^0x[0-9a-f]{96}$", key)

	decodedKey, err := codec.DecodeKey(key)
	require.NoError(t, err)
	assert.Equal(t, pubkey, decodedKey)

	// Value round trip (8-byte little-endian uint64).
	value, err := codec.EncodeValue(phase0.Gwei(5_000_000_000))
	require.NoError(t, err)
	require.Len(t, value, 8)

	decoded, err := codec.DecodeValue(value)
	require.NoError(t, err)
	assert.Equal(t, phase0.Gwei(5_000_000_000), decoded)
}

// TestPreferencesCodecRejectsInvalid pins garbage rejection.
func TestPreferencesCodecRejectsInvalid(t *testing.T) {
	codec := PreferencesCodec{}

	_, err := codec.DecodeKey("not-hex")
	assert.Error(t, err, "non-hex key must not decode")

	_, err = codec.DecodeKey("0xabcd")
	assert.Error(t, err, "short key must not decode")

	_, err = codec.DecodeValue([]byte{1, 2, 3})
	assert.Error(t, err, "short value must not decode")
}
