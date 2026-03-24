package proposerpreferences

import (
	"github.com/attestantio/go-eth2-client/spec/gloas"
	"github.com/attestantio/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/signer"
)

// VerifySignature verifies the BLS signature on a signed proposer preferences message.
// Returns true if the signature is valid.
func VerifySignature(signed *gloas.SignedProposerPreferences, pubkey phase0.BLSPubKey, domain phase0.Domain) bool {
	if signed == nil || signed.Message == nil {
		return false
	}

	messageRoot, err := signed.Message.HashTreeRoot()
	if err != nil {
		return false
	}

	var root phase0.Root
	copy(root[:], messageRoot[:])

	signingRoot := signer.ComputeSigningRoot(root, domain)

	return signer.VerifyBLSSignature(pubkey, signingRoot[:], signed.Signature)
}
