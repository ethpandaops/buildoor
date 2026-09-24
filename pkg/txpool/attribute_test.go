package txpool

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttribute(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	a := newAccount(t)
	b := newAccount(t)

	var submitted []*PooledTx

	for _, item := range []struct {
		acc   account
		nonce uint64
	}{{a, 0}, {b, 0}, {a, 1}} {
		tx, raw := signTx(t, item.acc, txOpts{nonce: item.nonce})
		_, err := pool.Add(raw)
		require.NoError(t, err)

		submitted = append(submitted, pool.Get(tx.Hash()))
	}

	attr := Attribute(errors.New("nonce too low: address "+a.addr.Hex()+", tx: 0 state: 1"), submitted)
	require.Equal(t, "nonce_too_low", attr.Reason)
	kept, dropped := attr.Apply(submitted)
	require.Len(t, dropped, 2, "every tx of the sender goes")
	require.Len(t, kept, 1)
	require.Equal(t, b.addr, kept[0].Sender)

	attr = Attribute(errors.New("invalid transaction 1: rlp: oops"), submitted)
	require.Equal(t, 1, attr.Index)
	kept, dropped = attr.Apply(submitted)
	require.Len(t, dropped, 1)
	require.Len(t, kept, 2)

	attr = Attribute(errors.New("insufficient funds for gas * price + value: address "+b.addr.Hex()), submitted)
	require.Equal(t, "insufficient_funds", attr.Reason)

	attr = Attribute(errors.New("gas limit reached"), submitted)
	require.Equal(t, 2, attr.Index)

	attr = Attribute(errors.New("something new"), submitted)
	require.True(t, attr.Bisect)
	kept, dropped = attr.Apply(submitted)
	require.Len(t, kept, 2)
	require.Len(t, dropped, 1)
}

func TestStrikeDropsAfterMax(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)
	acc := newAccount(t)

	tx, raw := signTx(t, acc, txOpts{nonce: 0})
	_, err := pool.Add(raw)
	require.NoError(t, err)

	require.False(t, pool.Strike(tx.Hash(), 2))
	require.Equal(t, 1, pool.Get(tx.Hash()).Strikes())
	require.True(t, pool.Strike(tx.Hash(), 2))
	require.Nil(t, pool.Get(tx.Hash()))
	require.Equal(t, uint64(1), pool.Stats().EvictedStrikes)

	// 0 never drops.
	_, raw = signTx(t, acc, txOpts{nonce: 1})
	hash, err := pool.Add(raw)
	require.NoError(t, err)

	for range 5 {
		require.False(t, pool.Strike(hash, 0))
	}

	require.NotNil(t, pool.Get(hash))
}
