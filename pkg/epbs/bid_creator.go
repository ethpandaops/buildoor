package epbs

import (
	"context"
	"fmt"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// BidCreator builds ePBS bids via the shared payload_bidder and gossips them
// over p2p. It owns the p2p transport and the (caller-computed) bid economics;
// the bid construction and signing live in payload_bidder.
type BidCreator struct {
	signer       *payload_bidder.Signer
	clClient     *beacon.Client
	chainSvc     chain.Service
	builderIndex uint64
	log          logrus.FieldLogger
}

// NewBidCreator creates a new bid creator.
func NewBidCreator(
	signer *payload_bidder.Signer,
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

// CreateAndSubmitBid builds, signs, and gossips a bid for the given payload at
// the supplied value. The competitive bid value is decided by the scheduler;
// the ePBS p2p path takes no execution payment.
func (c *BidCreator) CreateAndSubmitBid(
	ctx context.Context,
	payload *payload_builder.Payload,
	bidValue uint64,
) error {
	var feeRecipient bellatrix.ExecutionAddress

	copy(feeRecipient[:], payload.FeeRecipient[:])

	forkVersion, err := c.chainSvc.GetForkVersion()
	if err != nil {
		return fmt.Errorf("failed to get Gloas fork version: %w", err)
	}

	signedBid, err := payload_bidder.BuildSignedBid(payload, payload_bidder.BidParams{
		BuilderIndex:     c.builderIndex,
		FeeRecipient:     feeRecipient,
		Value:            phase0.Gwei(bidValue),
		ExecutionPayment: 0,
	}, c.signer, forkVersion, c.chainSvc.GetGenesis().GenesisValidatorsRoot)
	if err != nil {
		return fmt.Errorf("failed to build signed bid: %w", err)
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

	if err := c.clClient.SubmitExecutionPayloadBid(ctx, signedBid); err != nil {
		return fmt.Errorf("failed to submit bid: %w", err)
	}

	payload.AddBid(payload_builder.BidRecord{
		Transport: payload_builder.BidTransportP2P,
		Value:     phase0.Gwei(bidValue),
		At:        time.Now(),
	})

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
