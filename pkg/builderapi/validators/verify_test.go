package validators

import (
	"encoding/json"
	"testing"
	"time"

	apiv1 "github.com/attestantio/go-eth2-client/api/v1"
	"github.com/attestantio/go-eth2-client/spec/bellatrix"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/signer"
)

// TestVerifyRegistration_BuilderSpecsExample verifies that the official builder-specs
// example (which uses a placeholder signature) fails BLS verification.
func TestVerifyRegistration_BuilderSpecsExample(t *testing.T) {
	var regs []*apiv1.SignedValidatorRegistration
	require.NoError(t, json.Unmarshal(BuilderSpecsExampleJSON, &regs))
	require.Len(t, regs, 1, "builder-specs example has one registration")

	reg := regs[0]
	require.NotNil(t, reg.Message, "example must have message")
	// Example uses placeholder signature; verification must fail.
	require.False(t, VerifyRegistration(reg), "builder-specs example has invalid/placeholder signature and must not verify")
}

// TestVerifyRegistration_ValidSignature verifies that a validator registration
// signed with DOMAIN_APPLICATION_BUILDER passes BLS verification.
func TestVerifyRegistration_ValidSignature(t *testing.T) {
	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)

	var feeRecipient bellatrix.ExecutionAddress
	for i := range feeRecipient {
		feeRecipient[i] = byte(i + 1)
	}
	msg := &apiv1.ValidatorRegistration{
		FeeRecipient: feeRecipient,
		GasLimit:     30_000_000,
		Timestamp:    time.Unix(12345, 0),
		Pubkey:       blsSigner.PublicKey(),
	}

	messageRoot, err := msg.HashTreeRoot()
	require.NoError(t, err)
	var root phase0.Root
	copy(root[:], messageRoot[:])

	var zeroVersion phase0.Version
	var zeroRoot phase0.Root
	domain := signer.ComputeDomain(signer.DomainApplicationBuilder, zeroVersion, zeroRoot)
	signingRoot := signer.ComputeSigningRoot(root, domain)
	sig, err := blsSigner.Sign(signingRoot[:])
	require.NoError(t, err)

	reg := &apiv1.SignedValidatorRegistration{
		Message:   msg,
		Signature: sig,
	}

	require.True(t, VerifyRegistration(reg), "valid BLS signature must verify")
}

// TestVerifyRegistration_InvalidSignature verifies that a tampered or wrong
// signature fails BLS verification.
func TestVerifyRegistration_InvalidSignature(t *testing.T) {
	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)

	var feeRecipient bellatrix.ExecutionAddress
	for i := range feeRecipient {
		feeRecipient[i] = byte(i)
	}
	msg := &apiv1.ValidatorRegistration{
		FeeRecipient: feeRecipient,
		GasLimit:     100,
		Timestamp:    time.Unix(100, 0),
		Pubkey:       blsSigner.PublicKey(),
	}

	// Valid signature
	messageRoot, err := msg.HashTreeRoot()
	require.NoError(t, err)
	var root phase0.Root
	copy(root[:], messageRoot[:])
	var zeroVersion phase0.Version
	var zeroRoot phase0.Root
	domain := signer.ComputeDomain(signer.DomainApplicationBuilder, zeroVersion, zeroRoot)
	signingRoot := signer.ComputeSigningRoot(root, domain)
	sig, err := blsSigner.Sign(signingRoot[:])
	require.NoError(t, err)

	reg := &apiv1.SignedValidatorRegistration{Message: msg, Signature: sig}
	require.True(t, VerifyRegistration(reg), "sanity: valid reg must verify")

	// Tamper with one byte of the signature
	tamperedSig := sig
	tamperedSig[0] ^= 0xff
	regBad := &apiv1.SignedValidatorRegistration{Message: msg, Signature: tamperedSig}
	require.False(t, VerifyRegistration(regBad), "tampered signature must not verify")
}

// TestVerifyRegistration_TamperedMessage verifies that changing the message
// after signing causes verification to fail.
func TestVerifyRegistration_TamperedMessage(t *testing.T) {
	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)

	msg := &apiv1.ValidatorRegistration{
		FeeRecipient: [20]byte{1, 2, 3},
		GasLimit:     30_000_000,
		Timestamp:    time.Unix(100, 0),
		Pubkey:       blsSigner.PublicKey(),
	}
	messageRoot, err := msg.HashTreeRoot()
	require.NoError(t, err)
	var root phase0.Root
	copy(root[:], messageRoot[:])
	var zeroVersion phase0.Version
	var zeroRoot phase0.Root
	domain := signer.ComputeDomain(signer.DomainApplicationBuilder, zeroVersion, zeroRoot)
	signingRoot := signer.ComputeSigningRoot(root, domain)
	sig, err := blsSigner.Sign(signingRoot[:])
	require.NoError(t, err)

	reg := &apiv1.SignedValidatorRegistration{Message: msg, Signature: sig}
	require.True(t, VerifyRegistration(reg), "sanity: valid reg must verify")

	// Change gas limit (message changed, signature no longer valid)
	msgTampered := &apiv1.ValidatorRegistration{
		FeeRecipient: msg.FeeRecipient,
		GasLimit:     msg.GasLimit + 1,
		Timestamp:    msg.Timestamp,
		Pubkey:       msg.Pubkey,
	}
	regTampered := &apiv1.SignedValidatorRegistration{Message: msgTampered, Signature: sig}
	require.False(t, VerifyRegistration(regTampered), "signature over different message must not verify")
}

// TestVerifyRegistration_NilInputs verifies that nil or incomplete inputs
// are rejected.
func TestVerifyRegistration_NilInputs(t *testing.T) {
	require.False(t, VerifyRegistration(nil))

	regNoMessage := &apiv1.SignedValidatorRegistration{
		Message:   nil,
		Signature: phase0.BLSSignature{},
	}
	require.False(t, VerifyRegistration(regNoMessage))
}
