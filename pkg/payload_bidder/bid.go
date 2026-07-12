package payload_bidder

import (
	"context"
	"fmt"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	dynssz "github.com/pk910/dynamic-ssz"

	"github.com/ethpandaops/buildoor/pkg/jqtransform"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// BidParams are the policy-decided inputs a transport supplies for a bid. The
// transport owns the economics (value, execution payment) and identity (builder
// index, fee recipient); every other bid field is derived from the payload.
type BidParams struct {
	BuilderIndex     uint64
	FeeRecipient     bellatrix.ExecutionAddress
	Value            phase0.Gwei
	ExecutionPayment phase0.Gwei

	// Transform, when set, is an operator-supplied jq expression applied to the
	// bid MESSAGE (JSON) before signing; the modified message is then signed,
	// so the resulting bid is validly signed but customized.
	Transform string
}

// BuildSignedBid constructs and signs a fork-agnostic SignedExecutionPayloadBid
// for the given payload. The fork is read from the payload; all payload-derived
// fields (parent hashes, block hash, randao, gas limit, commitments, execution
// requests root, slot) are filled here, and only the transport's economics +
// identity come in via params.
func BuildSignedBid(
	ctx context.Context,
	p *payload_builder.Payload,
	params BidParams,
	s *Signer,
	forkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
) (*eth2all.SignedExecutionPayloadBid, error) {
	execRequests := p.ExecutionRequests
	if execRequests == nil {
		execRequests = &eth2all.ExecutionRequests{Version: p.ExecutionPayload.Version}
	}

	// dynssz so preset-dependent list limits resolve from the global spec.
	execRequestsRoot, err := dynssz.GetGlobalDynSsz().HashTreeRoot(execRequests)
	if err != nil {
		return nil, fmt.Errorf("failed to compute execution requests root: %w", err)
	}

	commitments := []deneb.KZGCommitment{}
	if p.BlobsBundle != nil {
		commitments = p.BlobsBundle.Commitments
	}

	bid := &eth2all.ExecutionPayloadBid{
		Version:               p.ExecutionPayload.Version,
		ParentBlockHash:       p.Attributes.ParentBlockHash,
		ParentBlockRoot:       p.Attributes.ParentBlockRoot,
		BlockHash:             p.BlockHash,
		PrevRandao:            p.Attributes.PrevRandao,
		FeeRecipient:          params.FeeRecipient,
		GasLimit:              p.ExecutionPayload.GasLimit,
		BuilderIndex:          gloas.BuilderIndex(params.BuilderIndex),
		Slot:                  p.Attributes.ProposalSlot,
		Value:                 params.Value,
		ExecutionPayment:      params.ExecutionPayment,
		BlobKZGCommitments:    commitments,
		ExecutionRequestsRoot: phase0.Root(execRequestsRoot),
		InclusionListBits:     []byte{0xff, 0xff}, // TODO: set a proper inclusion-list bitfield
	}

	// Apply the operator's jq transform to the bid message before signing, so
	// the signature covers the customized message.
	if params.Transform != "" {
		transformed := &eth2all.ExecutionPayloadBid{Version: bid.Version}
		if err := jqtransform.ApplyTyped(ctx, params.Transform, bid, transformed); err != nil {
			return nil, fmt.Errorf("bid transform failed: %w", err)
		}

		bid = transformed
	}

	sig, err := s.SignBid(bid, forkVersion, genesisValidatorsRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to sign bid: %w", err)
	}

	return &eth2all.SignedExecutionPayloadBid{
		Version:   bid.Version,
		Message:   bid,
		Signature: sig,
	}, nil
}
