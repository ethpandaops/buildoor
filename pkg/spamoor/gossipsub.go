package spamoor

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/golang/snappy"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/sirupsen/logrus"
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

	// gossipMaxPayloadSize is MAX_PAYLOAD_SIZE from the eth2 p2p spec (10 MiB).
	// Used as the upper bound on snappy-decompressed gossip message bodies.
	gossipMaxPayloadSize = 10 * 1024 * 1024
)

// Eth2 gossip MessageID domain bytes (consensus-specs).
var (
	messageDomainValidSnappy   = [4]byte{1, 0, 0, 0}
	messageDomainInvalidSnappy = [4]byte{0, 0, 0, 0}
)

// gossipMessageID computes the post-Altair gossipsub message-id per the eth2
// p2p spec, independent of Prysm's global params table:
//
//	SHA256(MESSAGE_DOMAIN_VALID_SNAPPY || u64_le(len(topic)) || topic || snappy_decompress(data))[:20]
//
// or, if snappy decompression fails / exceeds the size cap, swap the domain
// for MESSAGE_DOMAIN_INVALID_SNAPPY and hash the raw bytes instead.
//
// This intentionally does NOT route through Prysm's p2p.MsgID, which looks up
// the gossip topic's fork digest in params.BeaconConfig().networkSchedule.
// Buildoor never loads the devnet chain config into that singleton, so
// every published message would otherwise collapse onto the literal
// "invalid" sentinel id and get deduped by libp2p-pubsub's seen-cache.
func gossipMessageID(pmsg *pubsubpb.Message) string {
	topic := ""
	if pmsg.Topic != nil {
		topic = *pmsg.Topic
	}

	var topicLenBytes [8]byte
	binary.LittleEndian.PutUint64(topicLenBytes[:], uint64(len(topic)))

	domain := messageDomainValidSnappy
	payload, err := snappyDecodeBounded(pmsg.Data, gossipMaxPayloadSize)
	if err != nil {
		domain = messageDomainInvalidSnappy
		payload = pmsg.Data
	}

	h := sha256.New()
	h.Write(domain[:])
	h.Write(topicLenBytes[:])
	h.Write([]byte(topic))
	h.Write(payload)
	sum := h.Sum(nil)
	return string(sum[:20])
}

// snappyDecodeBounded decodes a snappy-compressed payload, rejecting any
// claimed decompressed size larger than maxSize before allocating.
func snappyDecodeBounded(data []byte, maxSize int) ([]byte, error) {
	size, err := snappy.DecodedLen(data)
	if err != nil {
		return nil, err
	}
	if size > maxSize {
		return nil, fmt.Errorf("snappy decompressed size %d exceeds max %d", size, maxSize)
	}
	return snappy.Decode(nil, data)
}

// computeForkDigest derives the 4-byte fork digest for a Fulu+ fork.
// It first computes the standard ForkData HashTreeRoot, then XORs in the
// BPO hash: SHA256(bpoEpoch_le64 || maxBlobsPerBlock_le64)[:4].
// bpoEpoch is the epoch at which the active blob parameters took effect;
// maxBlobsPerBlock is the corresponding blob limit. Both come from the last
// BLOB_SCHEDULE entry at or before the fork epoch.
func computeForkDigest(forkVersion phase0.Version, gvr phase0.Root, bpoEpoch, maxBlobsPerBlock uint64) ([4]byte, error) {
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

	var hb [16]byte
	binary.LittleEndian.PutUint64(hb[:8], bpoEpoch)
	binary.LittleEndian.PutUint64(hb[8:], maxBlobsPerBlock)
	bpoHash := sha256.Sum256(hb[:])
	for i := range 4 {
		digest[i] ^= bpoHash[i]
	}

	return digest, nil
}

// executionPayloadBidTopic returns the gossipsub topic string for Gloas bids.
func executionPayloadBidTopic(forkDigest [4]byte) string {
	return fmt.Sprintf("/eth2/%s/%s/ssz_snappy",
		hex.EncodeToString(forkDigest[:]),
		executionPayloadBidTopicSuffix)
}

// newGossipSub creates a gossipsub router configured with eth2 parameters and
// a tunable mesh degree (D). A non-nil log attaches a raw tracer that logs
// every gossipsub event at debug level.
func newGossipSub(ctx context.Context, h host.Host, d int, log logrus.FieldLogger) (*pubsub.PubSub, error) {
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
		pubsub.WithMessageIdFn(gossipMessageID),
		pubsub.WithMessageSignaturePolicy(pubsub.StrictNoSign),
		pubsub.WithNoAuthor(),
		pubsub.WithMaxMessageSize(gossipMaxPayloadSize),
		pubsub.WithValidateQueueSize(gossipValidateQueueSize),
	}

	if log != nil {
		opts = append(opts, pubsub.WithRawTracer(newLoggingRawTracer(log)))
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
