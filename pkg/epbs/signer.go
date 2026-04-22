package epbs

import (
	"fmt"

	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/signer"
)

// Domain types for ePBS operations.
var (
	// DomainBeaconBuilder is the domain for signing execution payload bids and execution payload envelopes.
	DomainBeaconBuilder = phase0.DomainType{0x0B, 0x00, 0x00, 0x00}
)

// Signer wraps the BLS signer for ePBS-specific signing operations.
type Signer struct {
	blsSigner *signer.BLSSigner
}

// NewSigner creates a new ePBS signer.
func NewSigner(blsSigner *signer.BLSSigner) *Signer {
	return &Signer{
		blsSigner: blsSigner,
	}
}

// SignExecutionPayloadBid signs an execution payload bid.
// forkVersion must be the current fork version (e.g. Gloas fork version) to match
// Prysm's verification which uses st.Fork().CurrentVersion.
func (s *Signer) SignExecutionPayloadBid(
	bid *gloas.ExecutionPayloadBid,
	forkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
) (phase0.BLSSignature, error) {
	// Compute hash tree root of the bid
	bidRoot, err := bid.HashTreeRoot()
	if err != nil {
		return phase0.BLSSignature{}, fmt.Errorf("failed to compute bid hash tree root: %w", err)
	}

	var root phase0.Root

	copy(root[:], bidRoot[:])

	domain := signer.ComputeDomain(DomainBeaconBuilder, forkVersion, genesisValidatorsRoot)

	return s.blsSigner.SignWithDomain(root, domain)
}

// SignExecutionPayloadEnvelope signs an execution payload envelope.
// forkVersion must be the current fork version to match Prysm's verification.
func (s *Signer) SignExecutionPayloadEnvelope(
	envelope *gloas.ExecutionPayloadEnvelope,
	forkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
) (phase0.BLSSignature, error) {
	// Compute hash tree root of the envelope
	envelopeRoot, err := envelope.HashTreeRoot()
	if err != nil {
		return phase0.BLSSignature{}, fmt.Errorf("failed to compute envelope hash tree root: %w", err)
	}

	var root phase0.Root

	copy(root[:], envelopeRoot[:])

	domain := signer.ComputeDomain(DomainBeaconBuilder, forkVersion, genesisValidatorsRoot)

	return s.blsSigner.SignWithDomain(root, domain)
}
