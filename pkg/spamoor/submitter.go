package spamoor

import (
	"context"
	"errors"
	"fmt"

	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/golang/snappy"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

// gossipSubmitter publishes signed bids on the execution_payload_bid topic.
// Encoding is SSZ + snappy block compression, matching the Gloas p2p spec.
type gossipSubmitter struct {
	topic *pubsub.Topic
}

// newGossipSubmitter wraps a joined gossipsub topic as a BidSubmitter.
func newGossipSubmitter(topic *pubsub.Topic) *gossipSubmitter {
	return &gossipSubmitter{topic: topic}
}

// Submit SSZ-encodes the bid, snappy-compresses it (block format), and
// gossips it to the bid topic mesh.
func (g *gossipSubmitter) Submit(ctx context.Context, signedBid *gloas.SignedExecutionPayloadBid) error {
	if g.topic == nil {
		return errors.New("gossip submitter: topic not initialized")
	}

	if signedBid == nil {
		return errors.New("gossip submitter: nil signed bid")
	}

	sszBytes, err := signedBid.MarshalSSZ()
	if err != nil {
		return fmt.Errorf("marshal ssz: %w", err)
	}

	// Gossipsub expects snappy block format (not framed/streaming).
	compressed := snappy.Encode(nil, sszBytes)

	if err := g.topic.Publish(ctx, compressed); err != nil {
		return fmt.Errorf("publish: %w", err)
	}

	return nil
}
