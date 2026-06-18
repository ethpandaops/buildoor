// Package payload_bidder holds the shared, transport-independent mechanics for
// post-Gloas builder participation: constructing and signing execution payload
// bids and the corresponding execution payload envelope (reveal). Both the ePBS
// p2p submitter and the HTTP Builder API build on it; each supplies its own
// economics (bid value, execution payment) while the construction, hash-tree-
// root (always via dynamic-ssz so preset-dependent sizes resolve), signing
// domain, and fork handling live here.
//
// Everything is fork-agnostic: bids and envelopes use the go-eth2-client
// spec/all union types and read the active fork from the payload, so adding a
// future fork is confined to the spec/all view tables.
package payload_bidder

import (
	"fmt"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	dynssz "github.com/pk910/dynamic-ssz"

	"github.com/ethpandaops/buildoor/pkg/signer"
)

// DomainBeaconBuilder is DOMAIN_BEACON_BUILDER — the signing domain for
// execution payload bids and execution payload envelopes.
var DomainBeaconBuilder = phase0.DomainType{0x0B, 0x00, 0x00, 0x00}

// Signer signs execution payload bids and envelopes with the builder's BLS key.
type Signer struct {
	blsSigner *signer.BLSSigner
}

// NewSigner creates a new payload bidder signer.
func NewSigner(blsSigner *signer.BLSSigner) *Signer {
	return &Signer{blsSigner: blsSigner}
}

// SignBid signs an execution payload bid. forkVersion must be the fork version
// the consensus client verifies against (the Gloas fork version).
func (s *Signer) SignBid(
	bid *eth2all.ExecutionPayloadBid,
	forkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
) (phase0.BLSSignature, error) {
	return s.sign(bid, forkVersion, genesisValidatorsRoot)
}

// SignEnvelope signs an execution payload envelope.
func (s *Signer) SignEnvelope(
	envelope *eth2all.ExecutionPayloadEnvelope,
	forkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
) (phase0.BLSSignature, error) {
	return s.sign(envelope, forkVersion, genesisValidatorsRoot)
}

// sign computes the dynssz hash-tree-root of msg (so preset-dependent list
// limits resolve from the global spec rather than the static mainnet preset)
// and signs it under DomainBeaconBuilder.
func (s *Signer) sign(
	msg any,
	forkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
) (phase0.BLSSignature, error) {
	msgRoot, err := dynssz.GetGlobalDynSsz().HashTreeRoot(msg)
	if err != nil {
		return phase0.BLSSignature{}, fmt.Errorf("failed to compute hash tree root: %w", err)
	}

	var root phase0.Root

	copy(root[:], msgRoot[:])

	domain := signer.ComputeDomain(DomainBeaconBuilder, forkVersion, genesisValidatorsRoot)

	return s.blsSigner.SignWithDomain(root, domain)
}
