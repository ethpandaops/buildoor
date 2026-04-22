// Package fulu provides Fulu (builder-specs) bid building from PayloadReadyEvent.
package fulu

import (
	"fmt"

	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/holiman/uint256"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// BidSigner signs a BuilderBid and returns the signature.
type BidSigner interface {
	SignWithDomain(root phase0.Root, domain phase0.Domain) (phase0.BLSSignature, error)
}

// BuildSignedBuilderBid builds a Fulu SignedBuilderBid from a PayloadReadyEvent and the proposer's pubkey,
// and signs it with the builder's BLS key using DOMAIN_APPLICATION_BUILDER with the provided genesis fork version
// and genesis validators root (matches mev-boost-relay behavior).
// subsidyGwei is added to the bid value so the proposer sees a higher bid (e.g. for testing).
func BuildSignedBuilderBid(
	event *builder.PayloadReadyEvent,
	proposerPubkey phase0.BLSPubKey,
	blsSigner BidSigner,
	subsidyGwei uint64,
	genesisForkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
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
		for i, c := range event.BlobsBundle.Commitments {
			if len(c) != 48 {
				return nil, fmt.Errorf("commitment %d: expected 48 bytes, got %d", i, len(c))
			}
			var k deneb.KZGCommitment
			copy(k[:], c)
			commitments = append(commitments, k)
		}
	}

	execRequests, err := ParseExecutionRequests(event.ExecutionRequests)
	if err != nil {
		return nil, fmt.Errorf("failed to parse execution requests: %w", err)
	}

	value := new(uint256.Int)
	value.SetUint64(event.BlockValue)
	if subsidyGwei > 0 {
		value.Add(value, new(uint256.Int).SetUint64(subsidyGwei))
	}

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
