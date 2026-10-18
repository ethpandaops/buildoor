package txpool

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

func TestFeeDistribution(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	// EL mempool: two transactions at 2 gwei, one at 20 gwei.
	cheapA, _ := signTx(t, newAccount(t), txOpts{nonce: 0, feeCap: 2_000_000_000, tip: 1})
	cheapB, _ := signTx(t, newAccount(t), txOpts{nonce: 0, feeCap: 2_000_000_000, tip: 1, gas: 50_000})
	rich, _ := signTx(t, newAccount(t), txOpts{nonce: 0, feeCap: 20_000_000_000, tip: 1})

	for _, tx := range []*types.Transaction{cheapA, cheapB, rich} {
		pool.mirror.insert(tx, time.Now())
	}

	// buildoor's own: one at 20 gwei, one also known to the EL mempool.
	_, privateRaw := signTx(t, newAccount(t), txOpts{nonce: 0, feeCap: 20_000_000_000, tip: 1})
	_, err := pool.Add(privateRaw)
	require.NoError(t, err)

	_, err = pool.Add(mustRaw(t, cheapA))
	require.NoError(t, err)

	dist, err := pool.FeeDistribution(context.Background(), common.Hash{0xaa}, true)
	require.NoError(t, err)
	require.True(t, dist.Private)
	require.Equal(t, uint64(30_000_000), dist.GasLimit)

	byIndex := func(fee int64) FeeBucket {
		edge := feeBucketEdge(feeBucketIndex(bigInt(fee))).String()
		for _, bucket := range dist.Buckets {
			if bucket.MinFee == edge {
				return bucket
			}
		}

		t.Fatalf("no bucket for %d", fee)

		return FeeBucket{}
	}

	low := byIndex(2_000_000_000)
	require.Equal(t, 1, low.ELTxs, "a transaction in both pools counts as private")
	require.Equal(t, uint64(50_000), low.ELGas)
	require.Equal(t, 1, low.PrivateTxs)

	high := byIndex(20_000_000_000)
	require.Equal(t, 1, high.ELTxs)
	require.Equal(t, 1, high.PrivateTxs)

	// Readers without a login see the EL mempool only.
	public, err := pool.FeeDistribution(context.Background(), common.Hash{0xaa}, false)
	require.NoError(t, err)
	require.False(t, public.Private)

	for _, bucket := range public.Buckets {
		require.Zero(t, bucket.PrivateTxs)
	}
}

func mustRaw(t *testing.T, tx *types.Transaction) []byte {
	t.Helper()

	raw, err := tx.MarshalBinary()
	require.NoError(t, err)

	return raw
}

func bigInt(v int64) *big.Int { return big.NewInt(v) }
