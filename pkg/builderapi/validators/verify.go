package validators

import (
	apiv1 "github.com/attestantio/go-eth2-client/api/v1"
	"github.com/attestantio/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/signer"
)

// VerifyRegistration verifies the BLS signature of a validator registration
// using DOMAIN_APPLICATION_BUILDER with zero fork version and genesis validators root.
// For chain-specific verification (e.g. mev-boost registrations), use VerifyRegistrationWithDomain.
func VerifyRegistration(reg *apiv1.SignedValidatorRegistration) bool {
	var zeroVersion phase0.Version
	var zeroRoot phase0.Root
	return VerifyRegistrationWithDomain(reg, zeroVersion, zeroRoot)
}

// VerifyRegistrationWithDomain verifies the BLS signature of a validator registration
// using DOMAIN_APPLICATION_BUILDER with the given fork version and genesis validators root.
// Consensus clients (used by mev-boost) sign with the chain's fork version and genesis root,
// so pass the chain's values from the beacon node for mainnet/testnet registrations.
func VerifyRegistrationWithDomain(reg *apiv1.SignedValidatorRegistration, forkVersion phase0.Version, genesisValidatorsRoot phase0.Root) bool {
	if reg == nil || reg.Message == nil {
		return false
	}

	messageRoot, err := reg.Message.HashTreeRoot()
	if err != nil {
		return false
	}

	var root phase0.Root
	copy(root[:], messageRoot[:])

	domain := signer.ComputeDomain(signer.DomainApplicationBuilder, forkVersion, genesisValidatorsRoot)
	signingRoot := signer.ComputeSigningRoot(root, domain)

	return signer.VerifyBLSSignature(reg.Message.Pubkey, signingRoot[:], reg.Signature)
}
