// Package gloas implements the Gloas-fork Builder API handlers and helpers.
// See https://github.com/ethereum/builder-specs/blob/epbs-spec-updates/specs/gloas/builder.md
package gloas

import (
	"errors"
	"fmt"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	gloastypes "github.com/ethpandaops/buildoor/pkg/builderapi/gloas/types"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// DomainRequestAuth is the DomainType used to sign SignedRequestAuth messages.
// Defined in builder-specs as DOMAIN_REQUEST_AUTH = DomainType('0x0B000001').
var DomainRequestAuth = phase0.DomainType{0x0B, 0x00, 0x00, 0x01}

var (
	// ErrNilSignedRequestAuth is returned when the SignedRequestAuth wrapper is nil.
	ErrNilSignedRequestAuth = errors.New("signed request auth is nil")
	// ErrNilRequestAuthMessage is returned when the inner RequestAuth message is nil.
	ErrNilRequestAuthMessage = errors.New("request auth message is nil")
	// ErrInvalidRequestAuthSignature is returned when BLS verification fails.
	ErrInvalidRequestAuthSignature = errors.New("invalid request auth signature")
)

// VerifyRequestAuth verifies the BLS signature on a SignedRequestAuth against
// the supplied validator public key.
//
// Per the Gloas builder-specs validator.md, the signing domain is
// compute_domain(DOMAIN_REQUEST_AUTH), which defaults to
// (GENESIS_FORK_VERSION, zero genesis_validators_root). Callers pass the
// chain's genesis fork version; the genesis_validators_root is always zero per
// spec.
//
// Returns nil on success, or one of the package's sentinel errors on failure.
func VerifyRequestAuth(
	signed *gloastypes.SignedRequestAuthV1,
	validatorPubkey phase0.BLSPubKey,
	genesisForkVersion phase0.Version,
) error {
	if signed == nil {
		return ErrNilSignedRequestAuth
	}
	if signed.Message == nil {
		return ErrNilRequestAuthMessage
	}

	msgRoot, err := signed.Message.HashTreeRoot()
	if err != nil {
		return fmt.Errorf("failed to compute request auth hash tree root: %w", err)
	}

	domain := signer.ComputeDomain(DomainRequestAuth, genesisForkVersion, phase0.Root{})
	signingRoot := signer.ComputeSigningRoot(msgRoot, domain)

	if !signer.VerifyBLSSignature(validatorPubkey, signingRoot[:], phase0.BLSSignature(signed.Signature)) {
		return ErrInvalidRequestAuthSignature
	}
	return nil
}
