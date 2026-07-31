package p2p_bidder

import (
	"context"
	"fmt"
	"time"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder_keys"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// bidSubmitter gossips a signed execution payload bid to the beacon node
// (implemented by *beacon.Client; interface for testability).
type bidSubmitter interface {
	SubmitExecutionPayloadBid(ctx context.Context, bid *eth2all.SignedExecutionPayloadBid) error
}

// BidCreator builds ePBS bids via the shared payload_bidder and gossips them
// over p2p. It owns the p2p transport and the (caller-computed) bid economics;
// the bid construction and signing live in payload_bidder. The builder identity
// comes in per bid: which of the managed keys signs is a scheduler decision.
type BidCreator struct {
	clClient bidSubmitter
	chainSvc chain.Service
	log      logrus.FieldLogger
}

// NewBidCreator creates a new bid creator.
func NewBidCreator(
	clClient bidSubmitter,
	chainSvc chain.Service,
	log logrus.FieldLogger,
) *BidCreator {
	return &BidCreator{
		clClient: clClient,
		chainSvc: chainSvc,
		log:      log.WithField("component", "bid-creator"),
	}
}

// CreateAndSubmitBid builds, signs, and gossips a bid for the given payload at
// the supplied value, from the given builder key. The competitive bid value is
// decided by the scheduler; the ePBS p2p path takes no execution payment. The
// constructed signed bid is returned even when the network submission fails so
// callers can record the exact object that was built; it is nil only when
// construction itself failed.
func (c *BidCreator) CreateAndSubmitBid(
	ctx context.Context,
	key *builder_keys.Key,
	payload *payload_builder.Payload,
	bidValue uint64,
	bidTransform string,
) (*eth2all.SignedExecutionPayloadBid, error) {
	builderIndex, registered := key.BuilderIndex()
	if !registered {
		return nil, fmt.Errorf("builder key %s is not registered on chain", key)
	}

	var feeRecipient bellatrix.ExecutionAddress

	copy(feeRecipient[:], payload.FeeRecipient[:])

	// Sign for the bid's target slot, not the current fork: a bid built during
	// the last pre-Gloas slot for the first Gloas slot must use the Gloas fork
	// version.
	targetSlot := payload.Attributes.ProposalSlot
	targetFork := c.chainSvc.ActiveForkAtEpoch(c.chainSvc.GetEpochOfSlot(targetSlot))

	forkVersion, err := c.chainSvc.GetChainSpec().GetForkVersion(targetFork)
	if err != nil {
		return nil, fmt.Errorf("failed to get fork version for slot %d: %w", targetSlot, err)
	}

	signedBid, err := payload_bidder.BuildSignedBid(ctx, payload, payload_bidder.BidParams{
		BuilderIndex:     builderIndex,
		FeeRecipient:     feeRecipient,
		Value:            phase0.Gwei(bidValue),
		ExecutionPayment: 0,
		Transform:        bidTransform,
	}, payload_bidder.NewSigner(key.BLSSigner()), forkVersion,
		c.chainSvc.GetGenesis().GenesisValidatorsRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to build signed bid: %w", err)
	}

	logger := c.log.WithFields(logrus.Fields{
		"slot":              payload.Attributes.ProposalSlot,
		"value":             bidValue,
		"block_hash":        fmt.Sprintf("%x", payload.BlockHash[:8]),
		"key":               key.String(),
		"builder_index":     builderIndex,
		"fee_recipient":     payload.FeeRecipient.Hex(),
		"gas_limit":         payload.ExecutionPayload.GasLimit,
		"parent_block_hash": fmt.Sprintf("%x", payload.Attributes.ParentBlockHash[:8]),
		"parent_block_root": fmt.Sprintf("%x", payload.Attributes.ParentBlockRoot[:8]),
	})

	if err := c.clClient.SubmitExecutionPayloadBid(ctx, signedBid); err != nil {
		return signedBid, fmt.Errorf("failed to submit bid: %w", err)
	}

	payload.AddBid(payload_builder.BidRecord{
		Transport: payload_builder.BidTransportP2P,
		Value:     phase0.Gwei(bidValue),
		At:        time.Now(),
	})

	logger.Info("Bid submitted")

	return signedBid, nil
}
