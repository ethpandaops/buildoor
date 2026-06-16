package epbs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/electra"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	dynssz "github.com/pk910/dynamic-ssz"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/builderapi/fulu"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// BidCreator handles creation and submission of execution payload bids.
type BidCreator struct {
	signer       *Signer
	clClient     *beacon.Client
	chainSvc     chain.Service
	builderIndex uint64
	log          logrus.FieldLogger
}

// NewBidCreator creates a new bid creator.
func NewBidCreator(
	signer *Signer,
	clClient *beacon.Client,
	chainSvc chain.Service,
	builderIndex uint64,
	log logrus.FieldLogger,
) *BidCreator {
	return &BidCreator{
		signer:       signer,
		clClient:     clClient,
		chainSvc:     chainSvc,
		builderIndex: builderIndex,
		log:          log.WithField("component", "bid-creator"),
	}
}

// CreateAndSubmitBid creates and submits a bid for the given payload.
func (c *BidCreator) CreateAndSubmitBid(
	ctx context.Context,
	payload *builder.PayloadReadyEvent,
	bidValue uint64,
) error {
	// Convert fee recipient to execution address
	var feeRecipient bellatrix.ExecutionAddress

	copy(feeRecipient[:], payload.FeeRecipient[:])

	// Compute the execution requests root over the typed Electra requests.
	// Empty requests root is the HTR of an empty *electra.ExecutionRequests.
	execRequests := payload.ExecutionRequests
	if execRequests == nil {
		execRequests = &electra.ExecutionRequests{}
	}
	// Use dynssz so preset-dependent list limits resolve from the global spec
	// (matches the node's computation on non-mainnet presets).
	execRequestsRoot, err := dynssz.GetGlobalDynSsz().HashTreeRoot(execRequests)
	if err != nil {
		return fmt.Errorf("failed to compute execution requests root: %w", err)
	}

	// Build the execution payload bid
	bid := &gloas.ExecutionPayloadBid{
		ParentBlockHash:       payload.Attributes.ParentBlockHash,
		ParentBlockRoot:       payload.Attributes.ParentBlockRoot,
		BlockHash:             payload.BlockHash,
		PrevRandao:            payload.Attributes.PrevRandao,
		FeeRecipient:          feeRecipient,
		GasLimit:              payload.ExecutionPayload.GasLimit,
		BuilderIndex:          gloas.BuilderIndex(c.builderIndex),
		Slot:                  payload.Attributes.ProposalSlot,
		Value:                 phase0.Gwei(bidValue),
		ExecutionPayment:      0,
		BlobKZGCommitments:    []deneb.KZGCommitment{},
		ExecutionRequestsRoot: execRequestsRoot,
	}

	c.log.Info("Created execution payload bid")

	if commitments := fulu.CommitmentsToDeneb(payload.BlobsBundle); commitments != nil {
		bid.BlobKZGCommitments = commitments
	}

	c.log.Info("Populated bid with blobs")

	c.log.Info("Signing bid before submitting")
	// Sign the bid using proper domain.
	// Prysm verifies using st.Fork().CurrentVersion — we must use the Gloas fork version.
	forkVersion, err := c.chainSvc.GetForkVersion()
	if err != nil {
		return fmt.Errorf("failed to get Gloas fork version: %w", err)
	}

	signature, err := c.signer.SignExecutionPayloadBid(
		bid,
		forkVersion,
		c.chainSvc.GetGenesis().GenesisValidatorsRoot,
	)
	if err != nil {
		return fmt.Errorf("failed to sign bid: %w", err)
	}

	c.log.Info("Signed bid successfully!")

	// Create signed bid
	signedBid := &gloas.SignedExecutionPayloadBid{
		Message:   bid,
		Signature: signature,
	}

	// Marshal to JSON for submission
	signedBidJSON, err := json.Marshal(signedBid)
	if err != nil {
		return fmt.Errorf("failed to marshal signed bid: %w", err)
	}

	logger := c.log.WithFields(logrus.Fields{
		"slot":              payload.Attributes.ProposalSlot,
		"value":             bidValue,
		"block_hash":        fmt.Sprintf("%x", payload.BlockHash[:8]),
		"builder_index":     c.builderIndex,
		"fee_recipient":     payload.FeeRecipient.Hex(),
		"gas_limit":         payload.ExecutionPayload.GasLimit,
		"parent_block_hash": fmt.Sprintf("%x", payload.Attributes.ParentBlockHash[:8]),
		"parent_block_root": fmt.Sprintf("%x", payload.Attributes.ParentBlockRoot[:8]),
	})

	logger.Info("Submitting bid")

	// Submit bid
	if err := c.clClient.SubmitExecutionPayloadBid(ctx, signedBidJSON); err != nil {
		return fmt.Errorf("failed to submit bid: %w", err)
	}

	logger.Info("Bid submitted")

	return nil
}

// SetBuilderIndex updates the builder index.
func (c *BidCreator) SetBuilderIndex(index uint64) {
	c.builderIndex = index
}

// GetBuilderIndex returns the current builder index.
func (c *BidCreator) GetBuilderIndex() uint64 {
	return c.builderIndex
}
