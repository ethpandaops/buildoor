package wallet

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/sirupsen/logrus"
)

// MaxBatchSize bounds how many transactions one batch submits at a time. Larger
// requests are split into consecutive chunks: a long run of pre-signed nonces is
// more exposed to another instance taking one of them, and every displaced
// nonce stalls the ones behind it until it is refilled.
const MaxBatchSize = 10

// TxRequest is one transaction of a batch submission.
type TxRequest struct {
	To       common.Address
	Value    *big.Int
	Data     []byte
	GasLimit uint64
}

// TxResult is the outcome of one batch request. Index is the request's position
// in the input slice, so results stay matchable regardless of completion order.
type TxResult struct {
	Index   int
	Receipt *types.Receipt
	Err     error
}

// batchTx is one in-flight transaction of a batch round.
type batchTx struct {
	index int // position in the caller's request slice
	tx    *types.Transaction
	// sendErr, if any, only matters as a tie-breaker once node state shows the
	// transaction never entered the pool.
	sendErr error
}

// SendBatchAndConfirm submits several transactions from this wallet at once and
// confirms each independently. It returns one result per request, in request
// order; a failure of one request never fails the others.
//
// Batching matters because every lifecycle transaction is serialized on the same
// funding key: depositing a fleet of builder keys one confirmed transaction at a
// time takes a block per key. The batch signs consecutive nonces in one go and
// sends them together, so they land in the same block or two.
//
// Each transaction is still resolved on its own, because sharing the funding key
// with other buildoor instances means any single nonce can be taken by a foreign
// transaction. A displaced transaction is rebuilt with a fresh nonce and retried
// in a later round, while the ones that landed are left alone.
func (w *Wallet) SendBatchAndConfirm(
	ctx context.Context, requests []TxRequest, timeout time.Duration,
) []TxResult {
	if len(requests) == 0 {
		return nil
	}

	w.txMu.Lock()
	defer w.txMu.Unlock()

	results := make([]TxResult, len(requests))
	for i := range results {
		results[i] = TxResult{Index: i}
	}

	pending := make([]int, 0, len(requests))
	for i := range requests {
		pending = append(pending, i)
	}

	for attempt := 1; attempt <= w.maxAttempts && len(pending) > 0; attempt++ {
		retry := make([]int, 0, len(pending))

		for _, chunk := range chunkIndices(pending, MaxBatchSize) {
			retry = append(retry, w.runBatchRound(ctx, requests, chunk, results, timeout)...)
		}

		pending = retry

		if len(pending) > 0 {
			w.log.WithFields(logrus.Fields{
				"pending": len(pending),
				"attempt": attempt,
			}).Warn("Retrying batch transactions whose nonce slot was taken")

			if err := sleepCtx(ctx, w.conflictBackoff); err != nil {
				break
			}
		}
	}

	// Anything still unresolved exhausted its attempts.
	for _, index := range pending {
		results[index].Err = fmt.Errorf("transaction failed after %d attempts", w.maxAttempts)
	}

	return results
}

// runBatchRound signs and sends one chunk with consecutive nonces, resolves every
// transaction concurrently, writes terminal outcomes into results and returns the
// request indices that must be retried with a fresh nonce.
func (w *Wallet) runBatchRound(
	ctx context.Context,
	requests []TxRequest,
	chunk []int,
	results []TxResult,
	timeout time.Duration,
) []int {
	baseNonce, err := w.nextNonce(ctx)
	if err != nil {
		for _, index := range chunk {
			results[index].Err = fmt.Errorf("failed to read nonce: %w", err)
		}

		return nil
	}

	sent := make([]batchTx, 0, len(chunk))

	for offset, index := range chunk {
		req := requests[index]

		//nolint:gosec // offset is bounded by MaxBatchSize
		tx, err := w.buildAndSignWithNonce(ctx, req, baseNonce+uint64(offset))
		if err != nil {
			results[index].Err = err
			continue
		}

		sendErr := w.rpcClient.SendTransaction(ctx, tx)
		if sendErr == nil {
			w.log.WithFields(logrus.Fields{
				"hash":  tx.Hash().Hex(),
				"nonce": tx.Nonce(),
				"to":    req.To.Hex(),
				"value": req.Value.String(),
			}).Info("Transaction sent")
		}

		sent = append(sent, batchTx{index: index, tx: tx, sendErr: sendErr})
	}

	outcomes := make([]txOutcome, len(sent))
	receipts := make([]*types.Receipt, len(sent))
	errs := make([]error, len(sent))

	var wg sync.WaitGroup

	for i := range sent {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			entry := sent[i]
			receipts[i], outcomes[i], errs[i] = w.resolve(
				ctx, entry.tx.Hash(), entry.tx.Nonce(), entry.sendErr, timeout)
		}(i)
	}

	wg.Wait()

	retry := make([]int, 0, len(sent))

	for i, entry := range sent {
		switch outcomes[i] {
		case outcomeIncluded:
			results[entry.index].Receipt = receipts[i]
		case outcomeReverted:
			results[entry.index].Receipt = receipts[i]
			results[entry.index].Err = errs[i]
		case outcomeRetry:
			retry = append(retry, entry.index)
		case outcomeFailed, outcomePending:
			results[entry.index].Err = fmt.Errorf(
				"send transaction (nonce %d): %w", entry.tx.Nonce(), errs[i])
		}
	}

	return retry
}

// buildAndSignWithNonce builds and signs a batch request at an explicit nonce.
func (w *Wallet) buildAndSignWithNonce(
	ctx context.Context, req TxRequest, nonce uint64,
) (*types.Transaction, error) {
	tx, err := w.buildTransactionWithNonce(ctx, req.To, req.Value, req.Data, req.GasLimit, nonce)
	if err != nil {
		return nil, err
	}

	return w.SignTransaction(tx)
}

// chunkIndices splits a slice into consecutive chunks of at most size entries.
func chunkIndices(indices []int, size int) [][]int {
	chunks := make([][]int, 0, (len(indices)+size-1)/size)

	for start := 0; start < len(indices); start += size {
		end := min(start+size, len(indices))
		chunks = append(chunks, indices[start:end])
	}

	return chunks
}
