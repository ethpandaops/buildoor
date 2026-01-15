package epbs

import (
	"fmt"

	"github.com/attestantio/go-eth2-client/spec/gloas"
	"github.com/attestantio/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/signer"
)

// Domain types for ePBS operations.
var (
	// DomainExecutionPayloadBidSigning is the domain for signing execution payload bids.
	DomainExecutionPayloadBidSigning = phase0.DomainType{0x0B, 0x00, 0x00, 0x00}

	// DomainExecutionPayloadEnvelope is the domain for signing payload envelopes.
	DomainExecutionPayloadEnvelope = phase0.DomainType{0x0C, 0x00, 0x00, 0x00}
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
func (s *Signer) SignExecutionPayloadBid(
	bid *gloas.ExecutionPayloadBid,
	genesisValidatorsRoot phase0.Root,
) (phase0.BLSSignature, error) {
	// Compute hash tree root of the bid
	bidRoot, err := bid.HashTreeRoot()
	if err != nil {
		return phase0.BLSSignature{}, fmt.Errorf("failed to compute bid hash tree root: %w", err)
	}

	var root phase0.Root

	copy(root[:], bidRoot[:])

	// Compute domain for execution payload bid signing
	// Using current fork version (empty for now, should be fetched from chain)
	domain := signer.ComputeDomain(DomainExecutionPayloadBidSigning, phase0.Version{}, genesisValidatorsRoot)

	return s.blsSigner.SignWithDomain(root, domain)
}

// SignExecutionPayloadEnvelope signs an execution payload envelope.
func (s *Signer) SignExecutionPayloadEnvelope(
	envelope *gloas.ExecutionPayloadEnvelope,
	genesisValidatorsRoot phase0.Root,
) (phase0.BLSSignature, error) {
	// Compute hash tree root of the envelope
	envelopeRoot, err := envelope.HashTreeRoot()
	if err != nil {
		return phase0.BLSSignature{}, fmt.Errorf("failed to compute envelope hash tree root: %w", err)
	}

	var root phase0.Root

	copy(root[:], envelopeRoot[:])

	// Compute domain for execution payload envelope signing
	domain := signer.ComputeDomain(DomainExecutionPayloadEnvelope, phase0.Version{}, genesisValidatorsRoot)

	return s.blsSigner.SignWithDomain(root, domain)
}
