package signer

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

const testEntryKey = "3f2b8e1c9d4a6f70b5c8e2a1d7943f6058ac2be91d3f5074a6b8c2e1d9f30475"

func TestDeriveInternalKeyIndexZeroIsEntryKey(t *testing.T) {
	derived, err := DeriveInternalKey(testEntryKey, 0)
	require.NoError(t, err)
	require.Equal(t, testEntryKey, derived)

	// The 0x prefix is accepted and normalised away.
	prefixed, err := DeriveInternalKey("0x"+testEntryKey, 0)
	require.NoError(t, err)
	require.Equal(t, testEntryKey, prefixed)
}

func TestDeriveInternalKeyIsDeterministicAndDistinct(t *testing.T) {
	seen := map[string]uint64{testEntryKey: 0}

	for index := uint64(1); index <= 8; index++ {
		derived, err := DeriveInternalKey(testEntryKey, index)
		require.NoError(t, err)
		require.Len(t, derived, 64)

		again, err := DeriveInternalKey(testEntryKey, index)
		require.NoError(t, err)
		require.Equal(t, derived, again, "derivation must be deterministic")

		previous, collides := seen[derived]
		require.False(t, collides, "index %d collides with index %d", index, previous)

		seen[derived] = index
	}
}

// Internal keys sit one node below the entry key, so they can never be reached
// by another builder walking the mnemonic account index — the property that
// makes the key set safe to use in shared test setups.
func TestDeriveInternalKeyDoesNotCollideWithNeighbourAccounts(t *testing.T) {
	const mnemonic = "test test test test test test test test test test test junk"

	ours, err := DeriveBLSPrivkeyHex(mnemonic, 7)
	require.NoError(t, err)

	neighbours := map[string]uint64{}

	for account := range uint64(24) {
		key, err := DeriveBLSPrivkeyHex(mnemonic, account)
		require.NoError(t, err)

		neighbours[key] = account
	}

	for index := uint64(1); index <= 16; index++ {
		derived, err := DeriveInternalKey(ours, index)
		require.NoError(t, err)

		account, collides := neighbours[derived]
		require.False(t, collides, "internal key %d collides with account %d", index, account)
	}
}

func TestDeriveInternalKeyRejectsBadInput(t *testing.T) {
	_, err := DeriveInternalKey("not-hex", 1)
	require.Error(t, err)

	_, err = DeriveInternalKey("aabb", 1)
	require.ErrorContains(t, err, "32 bytes")

	_, err = DeriveInternalKey(testEntryKey, math.MaxUint32+1)
	require.ErrorContains(t, err, "exceeds maximum")
}

func TestResolveEntryPrivkey(t *testing.T) {
	const mnemonic = "test test test test test test test test test test test junk"

	raw, err := ResolveEntryPrivkey(testEntryKey, "", 0)
	require.NoError(t, err)
	require.Equal(t, testEntryKey, raw)

	fromMnemonic, err := ResolveEntryPrivkey("", mnemonic, 3)
	require.NoError(t, err)

	expected, err := DeriveBLSPrivkeyHex(mnemonic, 3)
	require.NoError(t, err)
	require.Equal(t, expected, fromMnemonic)
}
