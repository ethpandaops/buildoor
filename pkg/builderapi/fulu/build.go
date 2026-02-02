// Package fulu provides Fulu (builder-specs) bid building from PayloadReadyEvent.
package fulu

import (
	"github.com/attestantio/go-eth2-client/spec/deneb"
	"github.com/attestantio/go-eth2-client/spec/electra"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/holiman/uint256"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// BidSigner signs a BuilderBid and returns the signature.
type BidSigner interface {
	SignWithDomain(root phase0.Root, domain phase0.Domain) (phase0.BLSSignature, error)
}

// BuildSignedBuilderBid builds a Fulu SignedBuilderBid from a PayloadReadyEvent and the proposer's pubkey,
// and signs it with the builder's BLS key (DOMAIN_APPLICATION_BUILDER, zero fork version and genesis root).
func BuildSignedBuilderBid(
	event *builder.PayloadReadyEvent,
	proposerPubkey phase0.BLSPubKey,
	blsSigner BidSigner,
) (*SignedBuilderBid, error) {
	if event == nil || event.Payload == nil {
		return nil, nil
	}

	header, err := ExecutionPayloadHeaderFromEngine(event.Payload)
	if err != nil {
		return nil, err
	}

	commitments := make([]deneb.KZGCommitment, 0)
	if event.BlobsBundle != nil && len(event.BlobsBundle.Commitments) > 0 {
		for _, c := range event.BlobsBundle.Commitments {
			var k deneb.KZGCommitment
			copy(k[:], c[:])
			commitments = append(commitments, k)
		}
	}

	var execRequests *electra.ExecutionRequests
	// event.ExecutionRequests is engine.ExecutionRequests ([]hexutil.Bytes); we don't convert to electra.ExecutionRequests here.
	// Use nil for now so the bid is valid. Full conversion would require parsing deposit/withdrawal/consolidation requests.

	value := new(uint256.Int)
	value.SetUint64(event.BlockValue)

	bid := &BuilderBid{
		Header:             header,
		BlobKZGCommitments: commitments,
		ExecutionRequests:  execRequests,
		Value:              value,
		Pubkey:             proposerPubkey,
	}

	bidRoot, err := bid.HashTreeRoot()
	if err != nil {
		return nil, err
	}

	var root phase0.Root
	copy(root[:], bidRoot[:])

	domain := signer.ComputeDomain(
		signer.DomainApplicationBuilder,
		phase0.Version{},
		phase0.Root{},
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
