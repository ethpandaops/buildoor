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
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/builderapi/fulu"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// BidCreator handles creation and submission of execution payload bids.
type BidCreator struct {
	signer       *Signer
	clClient     *beacon.Client
	genesis      *beacon.Genesis
	chainSpec    *beacon.ChainSpec
	builderIndex uint64
	log          logrus.FieldLogger
}

// NewBidCreator creates a new bid creator.
func NewBidCreator(
	signer *Signer,
	clClient *beacon.Client,
	genesis *beacon.Genesis,
	chainSpec *beacon.ChainSpec,
	builderIndex uint64,
	log logrus.FieldLogger,
) *BidCreator {
	return &BidCreator{
		signer:       signer,
		clClient:     clClient,
		genesis:      genesis,
		chainSpec:    chainSpec,
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
	execRequests := &electra.ExecutionRequests{}
	if len(payload.ExecutionRequests) > 0 {
		parsed, err := fulu.ParseExecutionRequests(payload.ExecutionRequests)
		if err != nil {
			return fmt.Errorf("failed to parse execution requests: %w", err)
		}
		execRequests = parsed
	}
	execRequestsRoot, err := execRequests.HashTreeRoot()
	if err != nil {
		return fmt.Errorf("failed to compute execution requests root: %w", err)
	}

	// Build the execution payload bid
	bid := &gloas.ExecutionPayloadBid{
		ParentBlockHash:       payload.ParentBlockHash,
		ParentBlockRoot:       payload.ParentBlockRoot,
		BlockHash:             payload.BlockHash,
		PrevRandao:            payload.PrevRandao,
		FeeRecipient:          feeRecipient,
		GasLimit:              payload.GasLimit,
		BuilderIndex:          gloas.BuilderIndex(c.builderIndex),
		Slot:                  payload.Slot,
		Value:                 phase0.Gwei(bidValue),
		ExecutionPayment:      0, // Same as value for now
		BlobKZGCommitments:    []deneb.KZGCommitment{},
		ExecutionRequestsRoot: execRequestsRoot,
	}

	c.log.Info("Created execution payload bid")

	if payload.BlobsBundle != nil {
		bid.BlobKZGCommitments = make([]deneb.KZGCommitment, len(payload.BlobsBundle.Commitments))
		for i, c := range payload.BlobsBundle.Commitments {
			copy(bid.BlobKZGCommitments[i][:], c)
		}
	}

	c.log.Info("Populated bid with blobs")

	c.log.Info("Signing bid before submitting")
	// Sign the bid using proper domain.
	// Prysm verifies using st.Fork().CurrentVersion — we must use the Gloas fork version.
	var forkVersion phase0.Version
	if c.chainSpec.GloasForkVersion != nil {
		forkVersion = *c.chainSpec.GloasForkVersion
	}

	signature, err := c.signer.SignExecutionPayloadBid(
		bid,
		forkVersion,
		c.genesis.GenesisValidatorsRoot,
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
		"slot":              payload.Slot,
		"value":             bidValue,
		"block_hash":        fmt.Sprintf("%x", payload.BlockHash[:8]),
		"builder_index":     c.builderIndex,
		"fee_recipient":     payload.FeeRecipient.Hex(),
		"gas_limit":         payload.GasLimit,
		"parent_block_hash": fmt.Sprintf("%x", payload.ParentBlockHash[:8]),
		"parent_block_root": fmt.Sprintf("%x", payload.ParentBlockRoot[:8]),
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
