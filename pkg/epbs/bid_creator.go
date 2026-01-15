package epbs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/attestantio/go-eth2-client/spec/bellatrix"
	"github.com/attestantio/go-eth2-client/spec/gloas"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// BidCreator handles creation and submission of execution payload bids.
type BidCreator struct {
	signer       *Signer
	clClient     *beacon.Client
	genesis      *beacon.Genesis
	builderIndex uint64
	log          logrus.FieldLogger
}

// NewBidCreator creates a new bid creator.
func NewBidCreator(
	signer *Signer,
	clClient *beacon.Client,
	genesis *beacon.Genesis,
	builderIndex uint64,
	log logrus.FieldLogger,
) *BidCreator {
	return &BidCreator{
		signer:       signer,
		clClient:     clClient,
		genesis:      genesis,
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

	// Build the execution payload bid
	bid := &gloas.ExecutionPayloadBid{
		ParentBlockHash:        payload.ParentBlockHash,
		ParentBlockRoot:        payload.ParentBlockRoot,
		BlockHash:              payload.BlockHash,
		PrevRandao:             payload.PrevRandao,
		FeeRecipient:           feeRecipient,
		GasLimit:               payload.GasLimit,
		BuilderIndex:           gloas.BuilderIndex(c.builderIndex),
		Slot:                   payload.Slot,
		Value:                  phase0.Gwei(bidValue),
		ExecutionPayment:       phase0.Gwei(bidValue), // Same as value for now
		BlobKZGCommitmentsRoot: phase0.Root{},         // Empty for no blobs
	}

	// Sign the bid using proper domain
	signature, err := c.signer.SignExecutionPayloadBid(
		bid,
		c.genesis.GenesisValidatorsRoot,
	)
	if err != nil {
		return fmt.Errorf("failed to sign bid: %w", err)
	}

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
		"slot":          payload.Slot,
		"value":         bidValue,
		"block_hash":    fmt.Sprintf("%x", payload.BlockHash[:8]),
		"builder_index": c.builderIndex,
		"fee_recipient": payload.FeeRecipient.Hex(),
		"gas_limit":     payload.GasLimit,
	})

	logger.Debug("Submitting bid")

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
