package payload_bidder

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/signer"
)

// testSigner builds a deterministic BLS signer for bid/envelope tests.
func testSigner(t *testing.T) *Signer {
	t.Helper()

	bls, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)

	return NewSigner(bls)
}

func TestBuildSignedBidTransform(t *testing.T) {
	s := testSigner(t)
	genesis := phase0.Root{}

	blockHash := phase0.Hash32{0xaa}
	payload := newTestPayload(7, blockHash, big.NewInt(1_000_000_000_000))

	t.Run("no transform leaves the message untouched", func(t *testing.T) {
		bid, err := BuildSignedBid(context.Background(), payload, BidParams{
			Value: 100,
		}, s, phase0.Version{}, genesis)
		require.NoError(t, err)
		assert.Equal(t, blockHash, bid.Message.BlockHash)
	})

	t.Run("transform rewrites a message field then re-signs", func(t *testing.T) {
		bid, err := BuildSignedBid(context.Background(), payload, BidParams{
			Value:     100,
			Transform: `.gas_limit = "60000000"`,
		}, s, phase0.Version{}, genesis)
		require.NoError(t, err)

		assert.Equal(t, uint64(60000000), uint64(bid.Message.GasLimit),
			"gas_limit override must land on the signed message")
		assert.NotEmpty(t, bid.Signature, "the modified message must be re-signed")
	})

	t.Run("invalid transform fails bid construction", func(t *testing.T) {
		_, err := BuildSignedBid(context.Background(), payload, BidParams{
			Value:     100,
			Transform: `.gas_limit |`,
		}, s, phase0.Version{}, genesis)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bid transform failed")
	})

	t.Run("runtime error fails bid construction", func(t *testing.T) {
		_, err := BuildSignedBid(context.Background(), payload, BidParams{
			Value:     100,
			Transform: `.gas_limit.nope`,
		}, s, phase0.Version{}, genesis)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bid transform failed")
	})
}

func TestBuildSignedEnvelopeTransform(t *testing.T) {
	s := testSigner(t)
	genesis := phase0.Root{}

	payload := newTestPayload(7, phase0.Hash32{0xbb}, big.NewInt(1))

	t.Run("transform rewrites the builder index then re-signs", func(t *testing.T) {
		env, _, _, err := BuildSignedEnvelope(context.Background(), payload, RevealContext{
			BuilderIndex: 3,
			Transform:    `.builder_index = "99"`,
		}, s, phase0.Version{}, genesis)
		require.NoError(t, err)
		assert.Equal(t, uint64(99), uint64(env.Message.BuilderIndex))
		assert.NotEmpty(t, env.Signature)
	})

	t.Run("invalid transform fails envelope construction", func(t *testing.T) {
		_, _, _, err := BuildSignedEnvelope(context.Background(), payload, RevealContext{
			Transform: `.builder_index |`,
		}, s, phase0.Version{}, genesis)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "envelope transform failed")
	})
}
