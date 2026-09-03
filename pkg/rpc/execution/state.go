package execution

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

// accountStateBatchSize bounds how many accounts one JSON-RPC batch request
// resolves (two calls per account).
const accountStateBatchSize = 100

// AccountState is an account's nonce and balance at a specific block.
type AccountState struct {
	Nonce   uint64
	Balance *big.Int
}

// HeaderByHash returns the execution block header with the given hash.
func (c *Client) HeaderByHash(ctx context.Context, hash common.Hash) (*types.Header, error) {
	header, err := c.ethClient.HeaderByHash(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("failed to get header %s: %w", hash.Hex(), err)
	}

	return header, nil
}

// BlobBaseFee returns the blob base fee the EL computes for the block after
// its current head (eth_blobBaseFee). Exact when building on the head; an
// approximation for other parents.
func (c *Client) BlobBaseFee(ctx context.Context) (*big.Int, error) {
	var fee hexutil.Big
	if err := c.rpcClient.CallContext(ctx, &fee, "eth_blobBaseFee"); err != nil {
		return nil, fmt.Errorf("failed to get blob base fee: %w", err)
	}

	return fee.ToInt(), nil
}

// AccountStatesAt resolves nonce and balance of every address at the given
// block hash in batched JSON-RPC requests (EIP-1898 block-hash parameter).
// ELs that reject the hash form are retried with the block number.
func (c *Client) AccountStatesAt(
	ctx context.Context,
	addrs []common.Address,
	blockHash common.Hash,
	blockNumber uint64,
) (map[common.Address]*AccountState, error) {
	out := make(map[common.Address]*AccountState, len(addrs))
	if len(addrs) == 0 {
		return out, nil
	}

	byHash := rpc.BlockNumberOrHashWithHash(blockHash, false)

	for start := 0; start < len(addrs); start += accountStateBatchSize {
		end := min(start+accountStateBatchSize, len(addrs))
		chunk := addrs[start:end]

		states, err := c.accountStatesBatch(ctx, chunk, byHash)
		if err != nil {
			byNumber := rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(blockNumber)) //nolint:gosec // block numbers fit
			if states, err = c.accountStatesBatch(ctx, chunk, byNumber); err != nil {
				return nil, err
			}
		}

		for addr, state := range states {
			out[addr] = state
		}
	}

	return out, nil
}

// accountStatesBatch resolves one chunk of accounts in a single batch request.
func (c *Client) accountStatesBatch(
	ctx context.Context,
	addrs []common.Address,
	block rpc.BlockNumberOrHash,
) (map[common.Address]*AccountState, error) {
	nonces := make([]hexutil.Uint64, len(addrs))
	balances := make([]hexutil.Big, len(addrs))
	batch := make([]rpc.BatchElem, 0, 2*len(addrs))

	for i, addr := range addrs {
		batch = append(batch,
			rpc.BatchElem{Method: "eth_getTransactionCount", Args: []any{addr, block}, Result: &nonces[i]},
			rpc.BatchElem{Method: "eth_getBalance", Args: []any{addr, block}, Result: &balances[i]},
		)
	}

	if err := c.rpcClient.BatchCallContext(ctx, batch); err != nil {
		return nil, fmt.Errorf("account state batch failed: %w", err)
	}

	var errs []error

	for _, elem := range batch {
		if elem.Error != nil {
			errs = append(errs, elem.Error)
		}
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("account state batch failed: %w", errors.Join(errs...))
	}

	out := make(map[common.Address]*AccountState, len(addrs))
	for i, addr := range addrs {
		out[addr] = &AccountState{Nonce: uint64(nonces[i]), Balance: balances[i].ToInt()}
	}

	return out, nil
}

// BlockTxList is the transaction hash list of an execution block together
// with the block's number.
type BlockTxList struct {
	Number   uint64
	TxHashes []common.Hash
}

// BlockTransactions returns the transaction hashes (and number) of the
// execution block with the given hash, or found=false when the EL does not
// know the block yet (a Gloas payload whose envelope is still unrevealed).
func (c *Client) BlockTransactions(ctx context.Context, blockHash common.Hash) (*BlockTxList, bool, error) {
	var block *struct {
		Number       hexutil.Uint64 `json:"number"`
		Transactions []common.Hash  `json:"transactions"`
	}

	if err := c.rpcClient.CallContext(ctx, &block, "eth_getBlockByHash", blockHash, false); err != nil {
		return nil, false, fmt.Errorf("failed to get block %s: %w", blockHash.Hex(), err)
	}

	if block == nil {
		return nil, false, nil
	}

	return &BlockTxList{Number: uint64(block.Number), TxHashes: block.Transactions}, true, nil
}

// TransactionBlockHashes resolves, in one batch request, the block hash each
// transaction was included in; transactions the EL does not know or that are
// still pending map to the zero hash.
func (c *Client) TransactionBlockHashes(ctx context.Context, hashes []common.Hash) (map[common.Hash]common.Hash, error) {
	out := make(map[common.Hash]common.Hash, len(hashes))
	if len(hashes) == 0 {
		return out, nil
	}

	for start := 0; start < len(hashes); start += accountStateBatchSize {
		end := min(start+accountStateBatchSize, len(hashes))
		chunk := hashes[start:end]

		results := make([]*struct {
			BlockHash *common.Hash `json:"blockHash"`
		}, len(chunk))
		batch := make([]rpc.BatchElem, len(chunk))

		for i, hash := range chunk {
			batch[i] = rpc.BatchElem{Method: "eth_getTransactionByHash", Args: []any{hash}, Result: &results[i]}
		}

		if err := c.rpcClient.BatchCallContext(ctx, batch); err != nil {
			return nil, fmt.Errorf("transaction lookup batch failed: %w", err)
		}

		for i, hash := range chunk {
			if batch[i].Error != nil || results[i] == nil || results[i].BlockHash == nil {
				out[hash] = common.Hash{}

				continue
			}

			out[hash] = *results[i].BlockHash
		}
	}

	return out, nil
}

// SendRawTransaction submits an already-encoded transaction to the EL mempool.
func (c *Client) SendRawTransaction(ctx context.Context, raw []byte) (common.Hash, error) {
	var hash common.Hash
	if err := c.rpcClient.CallContext(ctx, &hash, "eth_sendRawTransaction", hexutil.Bytes(raw)); err != nil {
		return common.Hash{}, fmt.Errorf("failed to send raw transaction: %w", err)
	}

	return hash, nil
}

// RawCall forwards an arbitrary JSON-RPC call with pre-encoded parameters and
// returns the raw result. Used by the ingress passthrough.
func (c *Client) RawCall(ctx context.Context, method string, params []any) ([]byte, error) {
	var result rawJSON
	if err := c.rpcClient.CallContext(ctx, &result, method, params...); err != nil {
		return nil, err
	}

	return result, nil
}

// rawJSON captures a JSON-RPC result verbatim, including a null result.
type rawJSON []byte

func (r *rawJSON) UnmarshalJSON(data []byte) error {
	*r = append((*r)[:0], data...)

	return nil
}
