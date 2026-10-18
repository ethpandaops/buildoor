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
