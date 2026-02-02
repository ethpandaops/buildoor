package validators

import (
	apiv1 "github.com/attestantio/go-eth2-client/api/v1"
	"github.com/attestantio/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/signer"
)

// VerifyRegistration verifies the BLS signature of a validator registration
// using DOMAIN_APPLICATION_BUILDER with zero parameters (for tests).
// For chain-specific verification (e.g. mev-boost registrations), use VerifyRegistrationWithDomain.
func VerifyRegistration(reg *apiv1.SignedValidatorRegistration) bool {
	var zero phase0.Version
	var zeroRoot phase0.Root
	return VerifyRegistrationWithDomain(reg, zero, zero, zeroRoot)
}

// VerifyRegistrationWithDomain verifies the BLS signature of a validator registration
// using DOMAIN_APPLICATION_BUILDER. Tries (0,0), (genesisForkVersion, 0), and (forkVersion, genesisValidatorsRoot)
// to match mev-boost-relay (genesis fork + zero root) and other client behaviors.
func VerifyRegistrationWithDomain(reg *apiv1.SignedValidatorRegistration, genesisForkVersion, forkVersion phase0.Version, genesisValidatorsRoot phase0.Root) bool {
	if reg == nil || reg.Message == nil {
		return false
	}

	messageRoot, err := reg.Message.HashTreeRoot()
	if err != nil {
		return false
	}

	var root phase0.Root
	copy(root[:], messageRoot[:])

	var zeroVersion phase0.Version
	var zeroRoot phase0.Root

	// 1) (0, 0) — some clients use this for DOMAIN_APPLICATION_BUILDER.
	domainZero := signer.ComputeDomain(signer.DomainApplicationBuilder, zeroVersion, zeroRoot)
	signingRootZero := signer.ComputeSigningRoot(root, domainZero)
	if signer.VerifyBLSSignature(reg.Message.Pubkey, signingRootZero[:], reg.Signature) {
		return true
	}

	// 2) (genesisForkVersion, 0) — mev-boost-relay DomainBuilder: ComputeDomain(DomainTypeAppBuilder, genesisForkVersion, zeroRoot).
	if genesisForkVersion != zeroVersion {
		domainRelay := signer.ComputeDomain(signer.DomainApplicationBuilder, genesisForkVersion, zeroRoot)
		signingRootRelay := signer.ComputeSigningRoot(root, domainRelay)
		if signer.VerifyBLSSignature(reg.Message.Pubkey, signingRootRelay[:], reg.Signature) {
			return true
		}
	}

	// 3) (forkVersion, genesisValidatorsRoot) — chain-specific domain.
	if forkVersion != zeroVersion || genesisValidatorsRoot != zeroRoot {
		domainChain := signer.ComputeDomain(signer.DomainApplicationBuilder, forkVersion, genesisValidatorsRoot)
		signingRootChain := signer.ComputeSigningRoot(root, domainChain)
		if signer.VerifyBLSSignature(reg.Message.Pubkey, signingRootChain[:], reg.Signature) {
			return true
		}
	}

	return false
}
