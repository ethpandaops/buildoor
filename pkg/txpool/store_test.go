package txpool

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// restartPool opens the store in dir for a fresh pool and restores it, as a
// buildoor restart does.
func restartPool(t *testing.T, dir string) *Pool {
	t.Helper()

	pool, _, _ := newTestPool(t, newFakeEL())

	store, err := OpenPebbleStore(dir, logrus.New())
	require.NoError(t, err)

	pool.UseStore(store)
	require.NoError(t, pool.restore())

	return pool
}

func TestStoreSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	pool := restartPool(t, dir)

	acc := newAccount(t)
	_, raw0 := signTx(t, acc, txOpts{nonce: 0})
	_, raw1 := signTx(t, acc, txOpts{nonce: 1})
	_, raw1b := signTx(t, acc, txOpts{nonce: 1, tip: 2_000_000_000, feeCap: 4_000_000_000})
	_, rawOther := signTx(t, newAccount(t), txOpts{nonce: 0})

	hash0, err := pool.AddFrom(raw0, "key:alice")
	require.NoError(t, err)

	_, err = pool.Add(raw1)
	require.NoError(t, err)

	hash1b, err := pool.Add(raw1b) // replaces nonce 1
	require.NoError(t, err)

	hashOther, err := pool.Add(rawOther)
	require.NoError(t, err)
	require.True(t, pool.Remove(hashOther))

	seq0 := pool.Get(hash0).Seq
	require.NoError(t, pool.Stop())

	restored := restartPool(t, dir)
	require.ElementsMatch(t, []common.Hash{hash0, hash1b}, poolHashes(restored))
	require.Equal(t, "key:alice", restored.Get(hash0).Submitter, "the submitter keeps its read access")
	require.Equal(t, seq0, restored.Get(hash0).Seq)
	require.Equal(t, acc.addr, restored.Get(hash1b).Sender)

	// Admissions continue after the restored sequence numbers.
	_, rawNext := signTx(t, acc, txOpts{nonce: 2})
	hashNext, err := restored.Add(rawNext)
	require.NoError(t, err)
	require.Greater(t, restored.Get(hashNext).Seq, restored.Get(hash1b).Seq)

	restored.Clear()
	require.NoError(t, restored.Stop())

	require.Empty(t, poolHashes(restartPool(t, dir)), "a cleared pool stays empty")
}

func poolHashes(pool *Pool) []common.Hash {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	out := make([]common.Hash, 0, len(pool.byHash))
	for hash := range pool.byHash {
		out = append(out, hash)
	}

	return out
}
