package gloas_test

import (
	"strings"
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/builderapi/gloas"
	gloastypes "github.com/ethpandaops/buildoor/pkg/builderapi/gloas/types"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

const (
	validatorPrivkeyHex = "1111111111111111111111111111111111111111111111111111111111111111"
	otherPrivkeyHex     = "2222222222222222222222222222222222222222222222222222222222222222"

	testBuilderURL  = "https://builder.example.com"
	otherBuilderURL = "https://other-builder.example.com"
)

// signRequestAuth builds and BLS-signs a RequestAuth with the given signer using
// DomainRequestAuth at the supplied genesis fork version (GVR=zero per spec).
func signRequestAuth(
	t *testing.T,
	s *signer.BLSSigner,
	builderURL []byte,
	slot phase0.Slot,
	genesisForkVersion phase0.Version,
) *gloastypes.SignedRequestAuthV1 {
	t.Helper()

	msg := &gloastypes.RequestAuthV1{
		Data: builderURL,
		Slot: slot,
	}

	root, err := msg.HashTreeRoot()
	require.NoError(t, err)

	domain := signer.ComputeDomain(gloas.DomainRequestAuth, genesisForkVersion, phase0.Root{})

	sig, err := s.SignWithDomain(root, domain)
	require.NoError(t, err)

	return &gloastypes.SignedRequestAuthV1{
		Message:   msg,
		Signature: sig,
	}
}

func TestDomainRequestAuthValue(t *testing.T) {
	// Spec: DOMAIN_REQUEST_AUTH = DomainType('0x0B000001')
	require.Equal(t, phase0.DomainType{0x0B, 0x00, 0x00, 0x01}, gloas.DomainRequestAuth)
}

func TestRequestAuth_SSZRoundTripAndSign(t *testing.T) {
	validator, err := signer.NewBLSSigner(validatorPrivkeyHex)
	require.NoError(t, err)

	genesisForkVersion := phase0.Version{}

	msg := &gloastypes.RequestAuthV1{
		Data: []byte(testBuilderURL),
		Slot: 1234,
	}

	// Marshal to SSZ.
	encoded, err := msg.MarshalSSZ()
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	// Unmarshal from SSZ into a fresh value.
	decoded := &gloastypes.RequestAuthV1{}
	require.NoError(t, decoded.UnmarshalSSZ(encoded))

	// The decoded value must match the original.
	require.Equal(t, msg.Data, decoded.Data)
	require.Equal(t, msg.Slot, decoded.Slot)

	// Hash tree roots must match across the round trip.
	origRoot, err := msg.HashTreeRoot()
	require.NoError(t, err)
	decodedRoot, err := decoded.HashTreeRoot()
	require.NoError(t, err)
	require.Equal(t, origRoot, decodedRoot)

	// Sign the decoded message and verify the signature is valid.
	domain := signer.ComputeDomain(gloas.DomainRequestAuth, genesisForkVersion, phase0.Root{})
	sig, err := validator.SignWithDomain(decodedRoot, domain)
	require.NoError(t, err)

	signed := &gloastypes.SignedRequestAuthV1{
		Message:   decoded,
		Signature: sig,
	}
	require.NoError(t, gloas.VerifyRequestAuth(signed, validator.PublicKey(), genesisForkVersion))
}

func TestVerifyRequestAuth_RoundTrip(t *testing.T) {
	validator, err := signer.NewBLSSigner(validatorPrivkeyHex)
	require.NoError(t, err)

	genesisForkVersion := phase0.Version{} // mainnet-style zero version

	signed := signRequestAuth(t, validator, []byte(testBuilderURL), 1234, genesisForkVersion)

	require.NoError(t, gloas.VerifyRequestAuth(signed, validator.PublicKey(), genesisForkVersion))
}

func TestVerifyRequestAuth_WrongValidatorPubkey(t *testing.T) {
	validator, err := signer.NewBLSSigner(validatorPrivkeyHex)
	require.NoError(t, err)

	other, err := signer.NewBLSSigner(otherPrivkeyHex)
	require.NoError(t, err)

	genesisForkVersion := phase0.Version{}

	signed := signRequestAuth(t, validator, []byte(testBuilderURL), 42, genesisForkVersion)

	err = gloas.VerifyRequestAuth(signed, other.PublicKey(), genesisForkVersion)
	require.ErrorIs(t, err, gloas.ErrInvalidRequestAuthSignature)
}

func TestVerifyRequestAuth_TamperedSlot(t *testing.T) {
	validator, err := signer.NewBLSSigner(validatorPrivkeyHex)
	require.NoError(t, err)

	genesisForkVersion := phase0.Version{}

	signed := signRequestAuth(t, validator, []byte(testBuilderURL), 100, genesisForkVersion)
	signed.Message.Slot = 101 // tamper

	err = gloas.VerifyRequestAuth(signed, validator.PublicKey(), genesisForkVersion)
	require.ErrorIs(t, err, gloas.ErrInvalidRequestAuthSignature)
}

func TestVerifyRequestAuth_TamperedBuilderURL(t *testing.T) {
	validator, err := signer.NewBLSSigner(validatorPrivkeyHex)
	require.NoError(t, err)

	genesisForkVersion := phase0.Version{}

	signed := signRequestAuth(t, validator, []byte(testBuilderURL), 7, genesisForkVersion)
	signed.Message.Data = []byte(otherBuilderURL) // tamper

	err = gloas.VerifyRequestAuth(signed, validator.PublicKey(), genesisForkVersion)
	require.ErrorIs(t, err, gloas.ErrInvalidRequestAuthSignature)
}

func TestVerifyRequestAuth_WrongGenesisForkVersion(t *testing.T) {
	validator, err := signer.NewBLSSigner(validatorPrivkeyHex)
	require.NoError(t, err)

	signingFork := phase0.Version{0x00, 0x00, 0x00, 0x00}
	verifyFork := phase0.Version{0x90, 0x00, 0x00, 0x69} // e.g. Sepolia-style

	signed := signRequestAuth(t, validator, []byte(testBuilderURL), 7, signingFork)

	err = gloas.VerifyRequestAuth(signed, validator.PublicKey(), verifyFork)
	require.ErrorIs(t, err, gloas.ErrInvalidRequestAuthSignature)
}

func TestVerifyRequestAuth_NilSigned(t *testing.T) {
	validator, err := signer.NewBLSSigner(validatorPrivkeyHex)
	require.NoError(t, err)

	err = gloas.VerifyRequestAuth(nil, validator.PublicKey(), phase0.Version{})
	require.ErrorIs(t, err, gloas.ErrNilSignedRequestAuth)
}

func TestVerifyRequestAuth_NilMessage(t *testing.T) {
	validator, err := signer.NewBLSSigner(validatorPrivkeyHex)
	require.NoError(t, err)

	signed := &gloastypes.SignedRequestAuthV1{
		Message:   nil,
		Signature: phase0.BLSSignature{},
	}

	err = gloas.VerifyRequestAuth(signed, validator.PublicKey(), phase0.Version{})
	require.ErrorIs(t, err, gloas.ErrNilRequestAuthMessage)
}

func TestVerifyRequestAuth_GarbageSignature(t *testing.T) {
	validator, err := signer.NewBLSSigner(validatorPrivkeyHex)
	require.NoError(t, err)

	signed := &gloastypes.SignedRequestAuthV1{
		Message: &gloastypes.RequestAuthV1{
			Data: []byte(testBuilderURL),
			Slot: 1,
		},
		// All-zero signature is not a valid BLS signature.
		Signature: phase0.BLSSignature{},
	}

	err = gloas.VerifyRequestAuth(signed, validator.PublicKey(), phase0.Version{})
	require.ErrorIs(t, err, gloas.ErrInvalidRequestAuthSignature)

	// Sanity check: the error message hasn't drifted (loosely).
	require.True(t, strings.Contains(err.Error(), "signature"))
}
