package validators

import (
	apiv1 "github.com/attestantio/go-eth2-client/api/v1"
	"github.com/attestantio/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/signer"
)

// VerifyRegistration verifies the BLS signature of a validator registration
// using DOMAIN_APPLICATION_BUILDER. Uses zero fork version and genesis validators root
// as per common builder API practice.
func VerifyRegistration(reg *apiv1.SignedValidatorRegistration) bool {
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
	domain := signer.ComputeDomain(signer.DomainApplicationBuilder, zeroVersion, zeroRoot)
	signingRoot := signer.ComputeSigningRoot(root, domain)

	return signer.VerifyBLSSignature(reg.Message.Pubkey, signingRoot[:], reg.Signature)
}
