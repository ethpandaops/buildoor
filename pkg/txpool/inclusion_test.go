package txpool

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func selectWithList(t *testing.T, pool *Pool, list [][]byte, fill []*PooledTx) *Selection {
	t.Helper()

	sel, err := pool.Select(context.Background(), &SelectParams{
		ParentHash:    common.Hash{0xaa},
		InclusionList: list,
		Fill:          fill,
	})
	require.NoError(t, err)

	return sel
}

func hashesOf(sel *Selection) []common.Hash {
	out := make([]common.Hash, len(sel.Ordered))
	for i, tx := range sel.Ordered {
		out[i] = tx.Hash
	}

	return out
}

func TestInclusionListGoesFirst(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	pooled, listed := newAccount(t), newAccount(t)
	fund(el, pooled, 0, 100)
	fund(el, listed, 0, 100)

	poolTx, raw := signTx(t, pooled, txOpts{nonce: 0})
	_, err := pool.Add(raw)
	require.NoError(t, err)

	listTx, listRaw := signTx(t, listed, txOpts{nonce: 0})

	sel := selectWithList(t, pool, [][]byte{listRaw}, nil)
	require.Equal(t, []common.Hash{listTx.Hash(), poolTx.Hash()}, hashesOf(sel))
	require.Equal(t, listRaw, sel.Txs[0])
	require.Equal(t, 1, sel.InclusionListTxs)
	require.Equal(t, uint64(2*21000), sel.GasSum, "the list's gas counts")
}

func TestInclusionListSkipsWhatCannotExecute(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	stale, cheap, poor := newAccount(t), newAccount(t), newAccount(t)
	fund(el, stale, 5, 100)
	fund(el, cheap, 0, 100)
	fund(el, poor, 0, 0)

	_, staleRaw := signTx(t, stale, txOpts{nonce: 1})
	_, cheapRaw := signTx(t, cheap, txOpts{nonce: 0, feeCap: 1_000, tip: 1})
	_, poorRaw := signTx(t, poor, txOpts{nonce: 0})

	sel := selectWithList(t, pool, [][]byte{staleRaw, cheapRaw, poorRaw}, nil)
	require.Empty(t, sel.Txs)
	require.Equal(t, map[string]int{
		SkipNonceTooLow:         1,
		SkipFeeBelowBase:        1,
		SkipInsufficientBalance: 1,
	}, sel.InclusionListSkipped)
}

func TestInclusionListDisplacesPoolTransactions(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	same, rival := newAccount(t), newAccount(t)
	fund(el, same, 0, 100)
	fund(el, rival, 0, 100)

	// The same transaction in the pool and the list goes in once.
	sameTx, sameRaw := signTx(t, same, txOpts{nonce: 0})
	_, err := pool.Add(sameRaw)
	require.NoError(t, err)

	// Another transaction at the nonce of a list transaction gives way.
	_, rivalPoolRaw := signTx(t, rival, txOpts{nonce: 0, tip: 1_500_000_000, feeCap: 3_000_000_000})
	_, err = pool.Add(rivalPoolRaw)
	require.NoError(t, err)

	rivalListTx, rivalListRaw := signTx(t, rival, txOpts{nonce: 0})

	sel := selectWithList(t, pool, [][]byte{sameRaw, rivalListRaw}, nil)
	require.Equal(t, []common.Hash{sameTx.Hash(), rivalListTx.Hash()}, hashesOf(sel))
	require.Equal(t, 1, sel.Skipped[SkipInInclusionList])
	require.Equal(t, 1, sel.Skipped[SkipNonceTaken])
	require.Equal(t, 2, pool.Stats().Pending, "a displaced pool transaction is not stale")
}

func TestInclusionListSenderContinuesInThePool(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	acc := newAccount(t)
	fund(el, acc, 0, 100)

	listTx, listRaw := signTx(t, acc, txOpts{nonce: 0})
	next, raw := signTx(t, acc, txOpts{nonce: 1})
	_, err := pool.Add(raw)
	require.NoError(t, err)

	sel := selectWithList(t, pool, [][]byte{listRaw}, nil)
	require.Equal(t, []common.Hash{listTx.Hash(), next.Hash()}, hashesOf(sel))
}

func TestInclusionListGapFilledByThePool(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	acc := newAccount(t)
	fund(el, acc, 0, 100)

	first, raw := signTx(t, acc, txOpts{nonce: 0})
	_, err := pool.Add(raw)
	require.NoError(t, err)

	// Nonce 1 only executes after the pool's nonce 0: placed after it.
	listTx, listRaw := signTx(t, acc, txOpts{nonce: 1})

	sel := selectWithList(t, pool, [][]byte{listRaw}, nil)
	require.Equal(t, []common.Hash{first.Hash(), listTx.Hash()}, hashesOf(sel))
	require.Equal(t, 1, sel.InclusionListTxs)

	// Without the pool's nonce 0 the gap stays.
	pool.Clear()

	sel = selectWithList(t, pool, [][]byte{listRaw}, nil)
	require.Empty(t, sel.Txs)
	require.Equal(t, 1, sel.InclusionListSkipped[SkipNonceGap])
}

func TestFillSkipsInclusionListTransactions(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	acc := newAccount(t)
	fund(el, acc, 0, 100)

	listTx, listRaw := signTx(t, acc, txOpts{nonce: 0})
	later, _ := signTx(t, acc, txOpts{nonce: 1})

	sel := selectWithList(t, pool, [][]byte{listRaw}, fillOf(t, listTx, later))
	require.Equal(t, []common.Hash{listTx.Hash(), later.Hash()}, hashesOf(sel),
		"the fill continues after the list's nonce")
	require.Equal(t, 1, sel.FillSkipped[SkipInInclusionList])
}

func TestInclusionListSkipsBlobTransactions(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	acc := newAccount(t)
	fund(el, acc, 0, 100)

	blobTx := signBlobTx(t, acc, 0, 1_000_000_000, 1)

	raw, err := blobTx.WithoutBlobTxSidecar().MarshalBinary()
	require.NoError(t, err)

	sel := selectWithList(t, pool, [][]byte{raw}, nil)
	require.Empty(t, sel.Txs)
	require.Equal(t, 1, sel.InclusionListSkipped[SkipBlobInList])
}
