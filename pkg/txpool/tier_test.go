package txpool

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

func TestTierFloors(t *testing.T) {
	floors := floorsFor(big.NewInt(1_000_000_000))

	require.True(t, floors.coldFee(big.NewInt(100_000_000)), "10% of the base fee is cold")
	require.False(t, floors.coldFee(big.NewInt(150_000_000)), "15% stays hot")
	require.False(t, floors.warmFee(big.NewInt(150_000_000)), "15% stays cold (hysteresis band)")
	require.True(t, floors.warmFee(big.NewInt(250_000_000)), "25% is loaded back")

	var none *tierFloors
	require.False(t, none.coldFee(big.NewInt(1)), "nothing is cold before the first head")
}

func TestColdPoolTxsLiveInTheStore(t *testing.T) {
	el := newFakeEL()
	pool := restartPool(t, t.TempDir())
	pool.elClient = el

	acc := newAccount(t)
	fund(el, acc, 0, 100)

	cheap, rawCheap := signTx(t, acc, txOpts{nonce: 0, feeCap: 100, tip: 1})
	_, rawNormal := signTx(t, acc, txOpts{nonce: 1})
	_, err := pool.Add(rawCheap)
	require.NoError(t, err)
	_, err = pool.Add(rawNormal)
	require.NoError(t, err)

	other := newAccount(t)
	cheapOther, rawCheapOther := signTx(t, other, txOpts{nonce: 0, feeCap: 100, tip: 1})
	_, err = pool.Add(rawCheapOther)
	require.NoError(t, err)

	ctx := context.Background()
	pool.retier(ctx, big.NewInt(1_000_000_000))

	require.True(t, pool.byHash[cheap.Hash()].Cold(), "far below the base fee: body leaves memory")
	require.Equal(t, 2, pool.Stats().Cold)
	require.Equal(t, "100", pool.byHash[cheap.Hash()].Summary().MaxFeePerGas, "listings keep the facts")
	require.Equal(t, uint64(2), pool.PendingNonce(acc.addr, 0), "cold nonces still count")

	loaded := pool.Get(cheap.Hash())
	require.NotNil(t, loaded.Tx, "a read loads the body from the store")
	require.Equal(t, cheap.Hash(), loaded.Tx.Hash())
	require.True(t, pool.byHash[cheap.Hash()].Cold(), "reading does not promote")

	// The selection treats a cold head like any fee below the base fee: the
	// sender's later nonces wait behind it.
	sel, err := pool.Select(ctx, &SelectParams{})
	require.NoError(t, err)
	require.Empty(t, sel.Selected)
	require.Equal(t, 3, sel.Skipped[SkipFeeBelowBase])

	// A replacement prices against the cold entry's facts.
	bumped, rawBumped := signTx(t, acc, txOpts{nonce: 0})
	_, err = pool.Add(rawBumped)
	require.NoError(t, err)
	require.False(t, pool.byHash[bumped.Hash()].Cold())

	sel, err = pool.Select(ctx, &SelectParams{})
	require.NoError(t, err)
	require.Len(t, sel.Selected, 2)

	// The base fee came down: the remaining cold transaction is loaded back.
	pool.retier(ctx, big.NewInt(300))
	require.False(t, pool.byHash[cheapOther.Hash()].Cold())
	require.Equal(t, cheapOther.Hash(), pool.byHash[cheapOther.Hash()].Tx.Hash())
	require.Zero(t, pool.Stats().Cold)
}

func TestColdNeedsAStore(t *testing.T) {
	pool, _, _ := newTestPool(t, newFakeEL())

	cheap, raw := signTx(t, newAccount(t), txOpts{nonce: 0, feeCap: 100, tip: 1})
	_, err := pool.Add(raw)
	require.NoError(t, err)

	pool.retier(context.Background(), big.NewInt(1_000_000_000))
	require.False(t, pool.byHash[cheap.Hash()].Cold(), "without a store the body has nowhere else to live")
}

func TestMirrorKeepsColdArrivalsWithoutBodies(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)
	pool.floors.Store(floorsFor(big.NewInt(1_000_000_000)))

	acc := newAccount(t)
	cheap, _ := signTx(t, acc, txOpts{nonce: 0, feeCap: 100, tip: 1})
	normal, _ := signTx(t, newAccount(t), txOpts{nonce: 0})
	el.pending = []*types.Transaction{cheap, normal}

	require.Equal(t, []common.Hash{normal.Hash()}, mirrorHashes(t, pool), "the fill never sees cold entries")

	stats := pool.ELMempoolStats()
	require.Equal(t, 2, stats.Txs)
	require.Equal(t, 1, stats.ColdTxs)
	require.Nil(t, pool.mirror.byHash[cheap.Hash()].tx.Tx, "a cold arrival is kept without its body")

	ctx := context.Background()

	// Back above the promotion floor: the body is refetched from the EL.
	el.mempool[cheap.Hash()] = cheap
	pool.mirror.retier(ctx, floorsFor(big.NewInt(300)))
	require.ElementsMatch(t, []common.Hash{cheap.Hash(), normal.Hash()}, mirrorHashes(t, pool))

	// Down again, and the EL dropped it meanwhile: it leaves on promotion.
	pool.mirror.retier(ctx, floorsFor(big.NewInt(1_000_000_000)))
	require.Equal(t, 1, pool.ELMempoolStats().ColdTxs)

	delete(el.mempool, cheap.Hash())
	pool.mirror.retier(ctx, floorsFor(big.NewInt(300)))
	require.Equal(t, 1, pool.ELMempoolStats().Txs)
}
