package execution

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

// txLookupBatchSize bounds how many transactions one JSON-RPC batch request
// fetches.
const txLookupBatchSize = 100

// NewPendingTransactionFilter installs a filter reporting the hashes of the
// transactions entering the EL mempool (eth_newPendingTransactionFilter). It
// covers blob transactions too, which txpool_content leaves out on geth.
func (c *Client) NewPendingTransactionFilter(ctx context.Context) (string, error) {
	var id string
	if err := c.rpcClient.CallContext(ctx, &id, "eth_newPendingTransactionFilter"); err != nil {
		return "", fmt.Errorf("failed to install the pending transaction filter: %w", err)
	}

	return id, nil
}

// PendingTransactionFilterChanges returns the transaction hashes the filter
// saw since the last poll. An error means the filter is gone (expired after
// the EL's idle timeout, or the EL restarted).
func (c *Client) PendingTransactionFilterChanges(ctx context.Context, id string) ([]common.Hash, error) {
	var hashes []common.Hash
	if err := c.rpcClient.CallContext(ctx, &hashes, "eth_getFilterChanges", id); err != nil {
		return nil, fmt.Errorf("failed to poll the pending transaction filter: %w", err)
	}

	return hashes, nil
}

// UninstallFilter removes a filter.
func (c *Client) UninstallFilter(ctx context.Context, id string) error {
	var removed bool
	if err := c.rpcClient.CallContext(ctx, &removed, "eth_uninstallFilter", id); err != nil {
		return fmt.Errorf("failed to uninstall filter: %w", err)
	}

	return nil
}

// TransactionsByHash fetches transactions by hash in JSON-RPC batches. Hashes
// the EL does not know (anymore) are left out; blob transactions come without
// their sidecars.
func (c *Client) TransactionsByHash(ctx context.Context, hashes []common.Hash) (map[common.Hash]*types.Transaction, error) {
	out := make(map[common.Hash]*types.Transaction, len(hashes))

	for start := 0; start < len(hashes); start += txLookupBatchSize {
		chunk := hashes[start:min(start+txLookupBatchSize, len(hashes))]
		results := make([]*types.Transaction, len(chunk))
		batch := make([]rpc.BatchElem, len(chunk))

		for i, hash := range chunk {
			batch[i] = rpc.BatchElem{
				Method: "eth_getTransactionByHash",
				Args:   []any{hash},
				Result: &results[i],
			}
		}

		if err := c.rpcClient.BatchCallContext(ctx, batch); err != nil {
			return nil, fmt.Errorf("failed to fetch transactions: %w", err)
		}

		for i, elem := range batch {
			if elem.Error != nil || results[i] == nil {
				continue
			}

			out[chunk[i]] = results[i]
		}
	}

	return out, nil
}

// PendingTxSubscription is a live subscription to the EL mempool's arrivals.
type PendingTxSubscription interface {
	// Err delivers the error that ended the subscription (connection lost).
	Err() <-chan error
	Unsubscribe()
}

// PendingTxStream subscribes to the transactions entering the EL mempool over
// a websocket (eth_subscribe newPendingTransactions with full transactions):
// live, where a pending transaction filter has to be polled. Every
// subscription dials its own connection, so a reconnect is a new subscribe.
type PendingTxStream struct {
	url string
}

// NewPendingTxStream creates the stream for the EL's websocket URL.
func NewPendingTxStream(url string) *PendingTxStream {
	return &PendingTxStream{url: url}
}

// SubscribePendingTransactions delivers every transaction entering the EL
// mempool to ch (blob transactions without their sidecars).
func (s *PendingTxStream) SubscribePendingTransactions(
	ctx context.Context, ch chan<- *types.Transaction,
) (PendingTxSubscription, error) {
	client, err := rpc.DialContext(ctx, s.url)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", s.url, err)
	}

	sub, err := client.EthSubscribe(ctx, ch, "newPendingTransactions", true)
	if err != nil {
		client.Close()

		return nil, fmt.Errorf("failed to subscribe to pending transactions: %w", err)
	}

	return &pendingTxSubscription{client: client, sub: sub}, nil
}

type pendingTxSubscription struct {
	client *rpc.Client
	sub    *rpc.ClientSubscription
}

func (s *pendingTxSubscription) Err() <-chan error { return s.sub.Err() }

func (s *pendingTxSubscription) Unsubscribe() {
	s.sub.Unsubscribe()
	s.client.Close()
}
