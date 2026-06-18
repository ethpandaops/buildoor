// Package fulu provides Fulu (builder-specs) bid building from Payload.
package fulu

import (
	"github.com/ethpandaops/go-eth2-client/spec/electra"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/holiman/uint256"

	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// BidSigner signs a BuilderBid and returns the signature.
type BidSigner interface {
	SignWithDomain(root phase0.Root, domain phase0.Domain) (phase0.BLSSignature, error)
}

// BuildSignedBuilderBid builds a Fulu SignedBuilderBid from a Payload and the proposer's pubkey,
// and signs it with the builder's BLS key using DOMAIN_APPLICATION_BUILDER with the provided genesis fork version
// and genesis validators root (matches mev-boost-relay behavior).
// subsidyGwei is added to the bid value so the proposer sees a higher bid (e.g. for testing).
func BuildSignedBuilderBid(
	event *payload_builder.Payload,
	proposerPubkey phase0.BLSPubKey,
	blsSigner BidSigner,
	subsidyGwei uint64,
	genesisForkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
) (*SignedBuilderBid, error) {
	if event == nil || event.ExecutionPayload == nil {
		return nil, nil
	}

	header, err := ExecutionPayloadHeaderFromBeacon(event.ExecutionPayload)
	if err != nil {
		return nil, err
	}

	// Fulu Builder API wire format uses electra.ExecutionRequests; builder
	// deposit/exit requests (Gloas+) do not exist in this spec version.
	var execRequests *electra.ExecutionRequests
	if event.ExecutionRequests != nil {
		execRequests = &electra.ExecutionRequests{
			Deposits:       event.ExecutionRequests.Deposits,
			Withdrawals:    event.ExecutionRequests.Withdrawals,
			Consolidations: event.ExecutionRequests.Consolidations,
		}
	} else {
		execRequests = &electra.ExecutionRequests{}
	}

	value, overflow := uint256.FromBig(event.BlockValue)
	if overflow || value == nil {
		value = new(uint256.Int)
	}
	if subsidyGwei > 0 {
		subsidyWei := subsidyGwei * 1_000_000_000
		value.Add(value, new(uint256.Int).SetUint64(subsidyWei))
	}

	bid := &BuilderBid{
		Header:            header,
		ExecutionRequests: execRequests,
		Value:             value,
		Pubkey:            proposerPubkey,
	}
	if event.BlobsBundle != nil {
		bid.BlobKZGCommitments = event.BlobsBundle.Commitments
	}

	bidRoot, err := bid.HashTreeRoot()
	if err != nil {
		return nil, err
	}

	var root phase0.Root
	copy(root[:], bidRoot[:])

	domain := signer.ComputeDomain(
		signer.DomainApplicationBuilder,
		genesisForkVersion,
		genesisValidatorsRoot,
	)

	sig, err := blsSigner.SignWithDomain(root, domain)
	if err != nil {
		return nil, err
	}

	return &SignedBuilderBid{
		Message:   bid,
		Signature: sig,
	}, nil
}
