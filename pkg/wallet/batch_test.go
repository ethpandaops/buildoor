package wallet

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func batchRequests(count int) []TxRequest {
	requests := make([]TxRequest, count)
	for i := range requests {
		requests[i] = TxRequest{
			To:       common.HexToAddress("0x000000000000000000000000000000000000dEaD"),
			Value:    big.NewInt(int64(i + 1)),
			Data:     []byte{byte(i)},
			GasLimit: 21000,
		}
	}

	return requests
}

// A batch signs consecutive nonces from one read, so the transactions can be in
// flight together instead of a block apart.
func TestSendBatchAndConfirmUsesConsecutiveNonces(t *testing.T) {
	backend := newFakeBackend()
	w := newTestWallet(t, backend)

	backend.pendingNonce = 7

	results := w.SendBatchAndConfirm(context.Background(), batchRequests(3), time.Second)
	require.Len(t, results, 3)

	for i, result := range results {
		require.NoError(t, result.Err, "request %d", i)
		require.NotNil(t, result.Receipt)
		require.Equal(t, i, result.Index)
	}

	require.Equal(t, []uint64{7, 8, 9}, backend.sentNonces())
}

// A nonce taken by another instance sharing the funding key must not fail the
// whole batch: the displaced transaction is resubmitted while the rest stand.
func TestSendBatchAndConfirmResubmitsDisplacedTransaction(t *testing.T) {
	backend := newFakeBackend()
	w := newTestWallet(t, backend)

	backend.pendingNonce = 4
	// The second transaction of the batch never lands and its slot is consumed
	// by a foreign transaction, so the account nonce moves past it.
	backend.dropNonce = 5

	results := w.SendBatchAndConfirm(context.Background(), batchRequests(3), 2*time.Second)
	require.Len(t, results, 3)

	for i, result := range results {
		require.NoError(t, result.Err, "request %d", i)
		require.NotNil(t, result.Receipt)
	}

	// The displaced request was retried on a fresh nonce above the batch.
	nonces := backend.sentNonces()
	require.Equal(t, []uint64{4, 5, 6}, nonces[:3])
	require.Greater(t, nonces[3], uint64(6))
}

// Requests beyond the batch size are sent in consecutive chunks rather than one
// long run of pre-signed nonces.
func TestSendBatchAndConfirmChunksLargeBatches(t *testing.T) {
	backend := newFakeBackend()
	w := newTestWallet(t, backend)

	count := MaxBatchSize + 3

	results := w.SendBatchAndConfirm(context.Background(), batchRequests(count), time.Second)
	require.Len(t, results, count)

	for i, result := range results {
		require.NoError(t, result.Err, "request %d", i)
	}

	require.Len(t, backend.sentNonces(), count)
}

func TestSendBatchAndConfirmEmpty(t *testing.T) {
	w := newTestWallet(t, newFakeBackend())

	require.Nil(t, w.SendBatchAndConfirm(context.Background(), nil, time.Second))
}

func TestChunkIndices(t *testing.T) {
	require.Equal(t, [][]int{{1, 2}, {3, 4}, {5}}, chunkIndices([]int{1, 2, 3, 4, 5}, 2))
	require.Equal(t, [][]int{{1, 2, 3}}, chunkIndices([]int{1, 2, 3}, 10))
	require.Empty(t, chunkIndices(nil, 4))
}
