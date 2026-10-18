package txpool

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
)

func mirrorHashes(t *testing.T, pool *Pool) []common.Hash {
	t.Helper()

	pending, err := pool.ELMempool(context.Background())
	require.NoError(t, err)

	out := make([]common.Hash, 0, len(pending))
	for _, tx := range pending {
		out = append(out, tx.Hash)
	}

	return out
}

func TestELMirrorFollowsTheELMempool(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)
	mirror := pool.mirror

	acc := newAccount(t)
	tx0, _ := signTx(t, acc, txOpts{nonce: 0})
	tx1, _ := signTx(t, acc, txOpts{nonce: 1})
	el.pending = []*types.Transaction{tx1, tx0}

	// Activation reads txpool_content once; senders come recovered, nonces
	// in order.
	pending, err := pool.ELMempool(context.Background())
	require.NoError(t, err)
	require.Len(t, pending, 2)
	require.Equal(t, []common.Hash{tx0.Hash(), tx1.Hash()}, []common.Hash{pending[0].Hash, pending[1].Hash})
	require.Equal(t, acc.addr, pending[0].Sender)
	require.Equal(t, 1, el.filters)

	// Arrivals come through the filter, blob transactions included (geth's
	// txpool_content does not list them).
	blobTx := signBlobTx(t, acc, 2, 1_000_000_000, 1)
	el.mempool[blobTx.Hash()] = blobTx
	el.arrivals = []common.Hash{blobTx.Hash()}

	mirror.poll(context.Background())
	require.Equal(t, []common.Hash{tx0.Hash(), tx1.Hash(), blobTx.Hash()}, mirrorHashes(t, pool))

	// A replacement at the same nonce takes the old one's place.
	tx1b, _ := signTx(t, acc, txOpts{nonce: 1, tip: 1_500_000_000, feeCap: 3_000_000_000})
	el.mempool[tx1b.Hash()] = tx1b
	el.arrivals = []common.Hash{tx1b.Hash()}

	mirror.poll(context.Background())
	require.Equal(t, []common.Hash{tx0.Hash(), tx1b.Hash(), blobTx.Hash()}, mirrorHashes(t, pool))

	// An included block removes its transactions and their senders' lower
	// nonces.
	block := common.Hash{0xb1}
	el.blocks[block] = &execution.BlockTxList{Number: 101, TxHashes: []common.Hash{tx1b.Hash()}}

	mirror.noteBlock(context.Background(), block)
	require.Equal(t, []common.Hash{blobTx.Hash()}, mirrorHashes(t, pool))

	// The selection reports stale copies.
	mirror.dropStale([]common.Hash{blobTx.Hash()})
	require.Empty(t, mirrorHashes(t, pool))
}

func TestELMirrorResync(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)
	mirror := pool.mirror

	acc := newAccount(t)
	listed, _ := signTx(t, acc, txOpts{nonce: 0})
	evicted, _ := signTx(t, newAccount(t), txOpts{nonce: 0})
	blobTx := signBlobTx(t, newAccount(t), 0, 1_000_000_000, 1)

	el.pending = []*types.Transaction{listed, evicted}
	require.Len(t, mirrorHashes(t, pool), 2)

	el.mempool[blobTx.Hash()] = blobTx
	el.arrivals = []common.Hash{blobTx.Hash()}
	mirror.poll(context.Background())

	// The EL dropped one listed transaction: the resync forgets it, but keeps
	// the blob transaction it cannot confirm.
	el.pending = []*types.Transaction{listed}
	require.NoError(t, mirror.resync(context.Background()))
	require.ElementsMatch(t, []common.Hash{listed.Hash(), blobTx.Hash()}, mirrorHashes(t, pool))

	// Unconfirmed blob transactions expire by age.
	mirror.mu.Lock()
	mirror.byHash[blobTx.Hash()].seenAt = time.Now().Add(-mirrorBlobMaxAge - time.Minute)
	mirror.mu.Unlock()

	require.NoError(t, mirror.resync(context.Background()))
	require.Equal(t, []common.Hash{listed.Hash()}, mirrorHashes(t, pool))
}

func TestSelectFillSkipsBelowBaseFeeWithoutStateReads(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	cheap := newAccount(t) // unfunded: a state read would find nothing
	cheapTx, _ := signTx(t, cheap, txOpts{nonce: 0, feeCap: 1, tip: 1})

	rich := newAccount(t)
	fund(el, rich, 0, 100)
	richTx, _ := signTx(t, rich, txOpts{nonce: 0})

	sel, err := pool.Select(context.Background(), &SelectParams{
		ParentHash: common.Hash{0xaa},
		GasLimit:   30_000_000,
		Fill:       fillOf(t, cheapTx, richTx),
	})
	require.NoError(t, err)
	require.Len(t, sel.Fill, 1)
	require.Equal(t, richTx.Hash(), sel.Fill[0].Hash)
	require.Equal(t, 1, sel.FillSkipped[SkipFeeBelowBase])
	require.NotContains(t, el.stateReads, cheap.addr, "no state read for a sender priced out")
}
