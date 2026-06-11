package signer

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEIP2333Vectors verifies the core EIP-2333 derivation against the canonical
// test case 0 from the EIP, which provides the seed directly (independent of BIP-39).
// https://eips.ethereum.org/EIPS/eip-2333#test-case-0
func TestEIP2333Vectors(t *testing.T) {
	seed, err := hex.DecodeString(
		"c55257c360c07c72029aebc1b53c05ed0362ada38ead3e3e9efa3708e53495531f09a6987599d18264c1e1c92f2cf141630c7a3c4ab7c81b2f001698e7463b04",
	)
	require.NoError(t, err)

	wantMaster, ok := new(big.Int).SetString(
		"6083874454709270928345386274498605044986640685124978867557563392430687146096", 10,
	)
	require.True(t, ok)

	wantChild, ok := new(big.Int).SetString(
		"20397789859736650942317412262472558107875392172444076792671091975210932703118", 10,
	)
	require.True(t, ok)

	master := deriveMasterSK(seed)
	require.Equal(t, 0, master.Cmp(wantMaster), "master SK mismatch")

	child := deriveChildSK(master, 0)
	require.Equal(t, 0, child.Cmp(wantChild), "child SK (index 0) mismatch")
}

// TestDeriveBLSPrivkeyHex verifies the full mnemonic -> signing key path is
// deterministic, returns a well-formed key, and round-trips through NewBLSSigner.
func TestDeriveBLSPrivkeyHex(t *testing.T) {
	const mnemonic = "abandon abandon abandon abandon abandon abandon " +
		"abandon abandon abandon abandon abandon about"

	key0, err := DeriveBLSPrivkeyHex(mnemonic, 0)
	require.NoError(t, err)
	require.Len(t, key0, 64, "key must be 32 bytes hex-encoded")

	// Deterministic for the same index.
	key0again, err := DeriveBLSPrivkeyHex(mnemonic, 0)
	require.NoError(t, err)
	require.Equal(t, key0, key0again)

	// Different index yields a different key.
	key1, err := DeriveBLSPrivkeyHex(mnemonic, 1)
	require.NoError(t, err)
	require.NotEqual(t, key0, key1)

	// Derived key is a usable BLS signer.
	s0, err := NewBLSSigner(key0)
	require.NoError(t, err)

	s1, err := NewBLSSigner(key1)
	require.NoError(t, err)
	require.NotEqual(t, s0.PublicKey(), s1.PublicKey())
}

func TestDeriveBLSPrivkeyHexInvalidMnemonic(t *testing.T) {
	_, err := DeriveBLSPrivkeyHex("not a valid mnemonic phrase at all", 0)
	require.Error(t, err)
}
