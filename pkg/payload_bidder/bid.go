package payload_bidder

import (
	"fmt"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/electra"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	dynssz "github.com/pk910/dynamic-ssz"

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
}

// BuildSignedBid constructs and signs a fork-agnostic SignedExecutionPayloadBid
// for the given payload. The fork is read from the payload; all payload-derived
// fields (parent hashes, block hash, randao, gas limit, commitments, execution
// requests root, slot) are filled here, and only the transport's economics +
// identity come in via params.
func BuildSignedBid(
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
	execRequestsRoot, err := hashGloasExecutionRequests(execRequests)
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

// gloasExecutionRequestsSigningView pins the five request collections to the
// bounded-list schema used by the Glamsterdam Gloas specification. The
// go-eth2-client v0.1.6 fork-agnostic view uses progressive lists, which is a
// different SSZ type and therefore commits a different root into the bid.
type gloasExecutionRequestsSigningView struct {
	Deposits        []*electra.DepositRequest       `ssz-max:"8192"`
	Withdrawals     []*electra.WithdrawalRequest    `ssz-max:"16"`
	Consolidations  []*electra.ConsolidationRequest `ssz-max:"2"`
	BuilderDeposits []*gloas.BuilderDepositRequest  `ssz-max:"256"`
	BuilderExits    []*gloas.BuilderExitRequest     `ssz-max:"16"`
}

func hashGloasExecutionRequests(requests *eth2all.ExecutionRequests) (phase0.Root, error) {
	view := &gloasExecutionRequestsSigningView{
		Deposits:        requests.Deposits,
		Withdrawals:     requests.Withdrawals,
		Consolidations:  requests.Consolidations,
		BuilderDeposits: requests.BuilderDeposits,
		BuilderExits:    requests.BuilderExits,
	}

	root, err := dynssz.GetGlobalDynSsz().HashTreeRoot(view)
	if err != nil {
		return phase0.Root{}, fmt.Errorf("failed to compute Gloas execution requests root: %w", err)
	}

	return phase0.Root(root), nil
}
