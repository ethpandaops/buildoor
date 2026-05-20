package spamoor

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"testing"
	"time"

	gcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/golang/snappy"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sampleSignedBid builds a minimal but well-formed SignedExecutionPayloadBid
// suitable for round-trip encoding tests.
func sampleSignedBid(t *testing.T) *gloas.SignedExecutionPayloadBid {
	t.Helper()

	bid := &gloas.ExecutionPayloadBid{
		ParentBlockHash:       phase0.Hash32{0x11},
		ParentBlockRoot:       phase0.Root{0x22},
		BlockHash:             phase0.Hash32{0x33},
		FeeRecipient:          bellatrix.ExecutionAddress{0x44},
		GasLimit:              30_000_000,
		BuilderIndex:          gloas.BuilderIndex(7),
		Slot:                  phase0.Slot(123),
		Value:                 phase0.Gwei(1_000_000),
		ExecutionPayment:      0,
		PrevRandao:            phase0.Root{0x55},
		BlobKZGCommitments:    []deneb.KZGCommitment{},
		ExecutionRequestsRoot: [32]byte{0x66},
	}

	return &gloas.SignedExecutionPayloadBid{
		Message:   bid,
		Signature: phase0.BLSSignature{0x77},
	}
}

func newTestHost(t *testing.T) host.Host {
	t.Helper()

	priv, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	require.NoError(t, err)

	h, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	)
	require.NoError(t, err)

	t.Cleanup(func() { _ = h.Close() })

	return h
}

// TestGossipSubmitter_PublishRoundTrip verifies the GossipSubmitter happy path:
// SSZ marshal + snappy compress + publish, then a second host on the same mesh
// receives and decodes the bid back to the original fields.
func TestGossipSubmitter_PublishRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hA := newTestHost(t)
	hB := newTestHost(t)

	// Connect the two hosts so gossipsub can form a mesh.
	require.NoError(t, hA.Connect(ctx, peer.AddrInfo{ID: hB.ID(), Addrs: hB.Addrs()}))

	gvr := make([]byte, 32)

	psA, err := newGossipSub(ctx, hA, gvr, 8)
	require.NoError(t, err)

	psB, err := newGossipSub(ctx, hB, gvr, 8)
	require.NoError(t, err)

	digest := [4]byte{0xaa, 0xbb, 0xcc, 0xdd}

	topicA, _, err := joinBidTopic(psA, digest)
	require.NoError(t, err)

	_, subB, err := joinBidTopic(psB, digest)
	require.NoError(t, err)

	// Allow heartbeats to populate the mesh.
	time.Sleep(2 * gossipHeartbeatInterval)

	signed := sampleSignedBid(t)

	submitter := newGossipSubmitter(topicA)
	require.NoError(t, submitter.Submit(ctx, signed))

	// Receive on hB.
	recvCtx, recvCancel := context.WithTimeout(ctx, 5*time.Second)
	defer recvCancel()

	msg, err := subB.Next(recvCtx)
	require.NoError(t, err, "hB did not receive bid within timeout")

	// Wire encoding: snappy-compressed SSZ.
	decompressed, err := snappy.Decode(nil, msg.Data)
	require.NoError(t, err)

	var got gloas.SignedExecutionPayloadBid
	require.NoError(t, got.UnmarshalSSZ(decompressed))

	assert.Equal(t, signed.Message.Slot, got.Message.Slot)
	assert.Equal(t, signed.Message.BlockHash, got.Message.BlockHash)
	assert.Equal(t, signed.Message.Value, got.Message.Value)
	assert.Equal(t, signed.Signature, got.Signature)
}

// TestGossipSubmitter_NilSafety covers the trivial guard rails.
func TestGossipSubmitter_NilSafety(t *testing.T) {
	g := newGossipSubmitter(nil)
	require.Error(t, g.Submit(context.Background(), sampleSignedBid(t)))

	// We can't easily build a real topic without a host; just exercise nil bid.
	hA := newTestHost(t)
	gvr := make([]byte, 32)
	ps, err := newGossipSub(context.Background(), hA, gvr, 8)
	require.NoError(t, err)
	topic, _, err := joinBidTopic(ps, [4]byte{1, 2, 3, 4})
	require.NoError(t, err)

	g2 := newGossipSubmitter(topic)
	require.Error(t, g2.Submit(context.Background(), nil))
}

// TestComputeForkDigest_Stable verifies the same inputs produce the same
// 4-byte digest (catches accidental changes to the derivation).
func TestComputeForkDigest_Stable(t *testing.T) {
	v := phase0.Version{0x01, 0x02, 0x03, 0x04}

	var gvr phase0.Root
	for i := range gvr {
		gvr[i] = byte(i)
	}

	a, err := computeForkDigest(v, gvr)
	require.NoError(t, err)

	b, err := computeForkDigest(v, gvr)
	require.NoError(t, err)

	assert.Equal(t, a, b)
	assert.NotEqual(t, [4]byte{}, a)
}

// TestExecutionPayloadBidTopic verifies the topic string format.
func TestExecutionPayloadBidTopic(t *testing.T) {
	digest := [4]byte{0xde, 0xad, 0xbe, 0xef}
	got := executionPayloadBidTopic(digest)
	assert.Equal(t, "/eth2/deadbeef/execution_payload_bid/ssz_snappy", got)
}

// TestLoadOrGenerateKey covers ephemeral generation.
func TestLoadOrGenerateKey(t *testing.T) {
	priv, err := loadOrGenerateKey("")
	require.NoError(t, err)
	assert.NotNil(t, priv)

	// Sanity: roundtrip through go-ethereum.
	raw := gcrypto.FromECDSA(priv)
	roundtrip, err := gcrypto.ToECDSA(raw)
	require.NoError(t, err)
	assert.Equal(t, priv.D.Bytes(), roundtrip.D.Bytes())
}

var _ = (*ecdsa.PrivateKey)(nil) // keep ecdsa import if test bodies change
var _ = logrus.New                // keep import for future tests
