package spamoor

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/OffchainLabs/prysm/v7/beacon-chain/p2p"
	"github.com/OffchainLabs/prysm/v7/config/params"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	gossipHeartbeatInterval = 700 * time.Millisecond
	gossipFanoutTTL         = 60 * time.Second
	gossipHistoryLength     = 6
	gossipHistoryGossip     = 3
	gossipDLazy             = 6
	gossipValidateQueueSize = 256

	// executionPayloadBidTopicSuffix is the topic name body (Gloas spec).
	// Full topic: /eth2/<fork_digest>/execution_payload_bid/ssz_snappy
	executionPayloadBidTopicSuffix = "execution_payload_bid"
)

// computeForkDigest derives the 4-byte fork digest from a fork version and
// the genesis validators root. Standard pre/post-Gloas computation.
func computeForkDigest(forkVersion phase0.Version, gvr phase0.Root) ([4]byte, error) {
	forkData := &phase0.ForkData{
		CurrentVersion:        forkVersion,
		GenesisValidatorsRoot: gvr,
	}

	htr, err := forkData.HashTreeRoot()
	if err != nil {
		return [4]byte{}, fmt.Errorf("compute fork data root: %w", err)
	}

	var digest [4]byte
	copy(digest[:], htr[:4])

	return digest, nil
}

// executionPayloadBidTopic returns the gossipsub topic string for Gloas bids.
func executionPayloadBidTopic(forkDigest [4]byte) string {
	return fmt.Sprintf("/eth2/%s/%s/ssz_snappy",
		hex.EncodeToString(forkDigest[:]),
		executionPayloadBidTopicSuffix)
}

// newGossipSub creates a gossipsub router configured with eth2 parameters and
// a tunable mesh degree (D). genesisValRoot is used for the eth2 message-id
// function — peers will reject messages with mismatched IDs.
func newGossipSub(ctx context.Context, h host.Host, genesisValRoot []byte, d int) (*pubsub.PubSub, error) {
	gsParams := pubsub.DefaultGossipSubParams()
	gsParams.D = d
	gsParams.Dlo = max(d-2, 1)
	gsParams.Dhi = d + d/2
	gsParams.Dlazy = gossipDLazy
	gsParams.HeartbeatInterval = gossipHeartbeatInterval
	gsParams.FanoutTTL = gossipFanoutTTL
	gsParams.HistoryLength = gossipHistoryLength
	gsParams.HistoryGossip = gossipHistoryGossip

	opts := []pubsub.Option{
		pubsub.WithGossipSubParams(gsParams),
		pubsub.WithMessageIdFn(func(pmsg *pubsubpb.Message) string {
			return p2p.MsgID(genesisValRoot, pmsg)
		}),
		pubsub.WithMessageSignaturePolicy(pubsub.StrictNoSign),
		pubsub.WithMaxMessageSize(int(params.BeaconConfig().MaxPayloadSize)),
		pubsub.WithValidateQueueSize(gossipValidateQueueSize),
	}

	return pubsub.NewGossipSub(ctx, h, opts...)
}

// joinBidTopic joins (and subscribes to, to keep the mesh active) the
// execution_payload_bid gossipsub topic. We register a pass-through validator
// so incoming bids are accepted at the gossipsub layer — we don't validate
// them against beacon state, we just need mesh participation to publish.
func joinBidTopic(ps *pubsub.PubSub, forkDigest [4]byte) (*pubsub.Topic, *pubsub.Subscription, error) {
	topicStr := executionPayloadBidTopic(forkDigest)

	if err := ps.RegisterTopicValidator(topicStr,
		func(_ context.Context, _ peer.ID, _ *pubsub.Message) pubsub.ValidationResult {
			return pubsub.ValidationAccept
		}); err != nil {
		return nil, nil, fmt.Errorf("register bid topic validator: %w", err)
	}

	topic, err := ps.Join(topicStr)
	if err != nil {
		return nil, nil, fmt.Errorf("join bid topic: %w", err)
	}

	sub, err := topic.Subscribe()
	if err != nil {
		_ = topic.Close()
		return nil, nil, fmt.Errorf("subscribe to bid topic: %w", err)
	}

	return topic, sub, nil
}
