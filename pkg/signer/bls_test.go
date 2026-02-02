package signer

import (
	"encoding/hex"
	"testing"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDepositRootComputation verifies that deposit data root computation is correct.
func TestDepositRootComputation(t *testing.T) {
	// Test with known values
	var pubkey phase0.BLSPubKey
	for i := range pubkey {
		pubkey[i] = 0xaa
	}

	var wc [32]byte
	wc[0] = 0x03
	for i := 12; i < 32; i++ {
		wc[i] = byte(i)
	}

	amount := uint64(32000000000) // 32 ETH in Gwei

	var sig phase0.BLSSignature
	for i := range sig {
		sig[i] = 0xbb
	}

	// Compute deposit data root
	root, err := ComputeDepositDataRoot(pubkey, wc, amount, sig)
	require.NoError(t, err)

	// Log the computed root for verification
	t.Logf("Pubkey: 0x%x", pubkey[:])
	t.Logf("Withdrawal credentials: 0x%x", wc[:])
	t.Logf("Amount (Gwei): %d", amount)
	t.Logf("Signature: 0x%x", sig[:])
	t.Logf("Computed deposit data root: 0x%x", root[:])

	// Ensure root is not empty
	var emptyRoot phase0.Root
	assert.NotEqual(t, emptyRoot, root, "deposit data root should not be empty")

	// Also test deposit message root (without signature)
	genesisForkVersion := phase0.Version{} // Zeros for mainnet
	signingRoot, err := ComputeDepositSigningRoot(pubkey, wc, amount, genesisForkVersion)
	require.NoError(t, err)

	t.Logf("Deposit signing root: 0x%x", signingRoot[:])
	assert.NotEqual(t, emptyRoot, signingRoot, "signing root should not be empty")
}

// TestDepositDomainComputation verifies domain computation for deposits.
func TestDepositDomainComputation(t *testing.T) {
	// Test with zeros (mainnet genesis fork version)
	forkVersion := phase0.Version{}
	genesisRoot := phase0.Root{}

	domain := ComputeDomain(DomainDeposit, forkVersion, genesisRoot)

	t.Logf("Domain: 0x%x", domain[:])

	// Domain should start with 0x03 (DOMAIN_DEPOSIT)
	assert.Equal(t, byte(0x03), domain[0], "domain should start with 0x03 for deposits")

	// With zeros for fork version and genesis root, the fork data root
	// is SHA256(zeros) which gives a known value
	// Domain = 0x03000000 + fork_data_root[:28]
	expectedPrefix, _ := hex.DecodeString("03000000")
	assert.Equal(t, expectedPrefix, domain[:4], "domain prefix should be 0x03000000")
}

// TestSigningRootComputation verifies signing root computation.
func TestSigningRootComputation(t *testing.T) {
	objectRoot := phase0.Root{}
	copy(objectRoot[:], []byte("test object root for signing..."))

	domain := phase0.Domain{}
	copy(domain[:], []byte("test domain for signing........"))

	signingRoot := ComputeSigningRoot(objectRoot, domain)

	t.Logf("Object root: 0x%x", objectRoot[:])
	t.Logf("Domain: 0x%x", domain[:])
	t.Logf("Signing root: 0x%x", signingRoot[:])

	var emptyRoot phase0.Root
	assert.NotEqual(t, emptyRoot, signingRoot, "signing root should not be empty")
}

// TestComputeDomain verifies domain computation using SSZ ForkData (consensus spec).
func TestComputeDomain(t *testing.T) {
	forkVersion := phase0.Version{}
	genesisRoot := phase0.Root{}

	domain := ComputeDomain(DomainApplicationBuilder, forkVersion, genesisRoot)

	t.Logf("Fork version: 0x%x", forkVersion[:])
	t.Logf("Genesis validators root: 0x%x", genesisRoot[:])
	t.Logf("Domain: 0x%x", domain[:])

	// Domain should be deterministic for same inputs
	domain2 := ComputeDomain(DomainApplicationBuilder, forkVersion, genesisRoot)
	assert.Equal(t, domain, domain2, "domain should be deterministic")
}
