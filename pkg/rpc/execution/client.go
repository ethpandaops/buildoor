package execution

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/tx_intake"
)

// Client handles standard JSON-RPC calls for wallet/transaction operations.
// Only needed when lifecycle management is enabled.
type Client struct {
	ethClient *ethclient.Client
	rpcClient *rpc.Client
	rpcURL    string
	intake    *tx_intake.Queue // optional: builder's own txs also enter the testing build queue
	log       logrus.FieldLogger
}

// SetTxIntake makes every transaction this client sends also enter the tx
// intake queue. With the testing build source the builder's own blocks hold
// only queued transactions, so its lifecycle deposits and top-ups would
// otherwise never land in a block it builds itself.
func (c *Client) SetTxIntake(q *tx_intake.Queue) {
	c.intake = q
}

// NewClient creates a new standard EL JSON-RPC client (no JWT).
func NewClient(ctx context.Context, rpcURL string, log logrus.FieldLogger) (*Client, error) {
	clientLog := log.WithField("component", "rpc-client")

	rpcClient, err := rpc.DialContext(ctx, rpcURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to EL RPC: %w", err)
	}

	ethClient := ethclient.NewClient(rpcClient)

	return &Client{
		ethClient: ethClient,
		rpcClient: rpcClient,
		rpcURL:    rpcURL,
		log:       clientLog,
	}, nil
}

// Close closes the RPC connections.
func (c *Client) Close() {
	if c.ethClient != nil {
		c.ethClient.Close()
	}

	if c.rpcClient != nil {
		c.rpcClient.Close()
	}
}

// EthClient returns the underlying ethclient.Client for direct operations.
func (c *Client) EthClient() *ethclient.Client {
	return c.ethClient
}

// GetChainID returns the chain ID.
func (c *Client) GetChainID(ctx context.Context) (*big.Int, error) {
	chainID, err := c.ethClient.ChainID(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get chain ID: %w", err)
	}

	return chainID, nil
}

// GetBlockByNumber returns a block by number.
func (c *Client) GetBlockByNumber(ctx context.Context, number *big.Int) (*types.Block, error) {
	block, err := c.ethClient.BlockByNumber(ctx, number)
	if err != nil {
		return nil, fmt.Errorf("failed to get block: %w", err)
	}

	return block, nil
}

// GetBlockByHash returns a block by hash.
func (c *Client) GetBlockByHash(ctx context.Context, hash common.Hash) (*types.Block, error) {
	block, err := c.ethClient.BlockByHash(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("failed to get block: %w", err)
	}

	return block, nil
}

// GetLatestBlock returns the latest block.
func (c *Client) GetLatestBlock(ctx context.Context) (*types.Block, error) {
	return c.GetBlockByNumber(ctx, nil)
}

// SendTransaction sends a signed transaction.
func (c *Client) SendTransaction(ctx context.Context, tx *types.Transaction) error {
	if err := c.ethClient.SendTransaction(ctx, tx); err != nil {
		return fmt.Errorf("failed to send transaction: %w", err)
	}

	if c.intake != nil {
		raw, err := tx.MarshalBinary()
		if err != nil {
			return fmt.Errorf("encoding transaction for the tx intake: %w", err)
		}

		if _, err := c.intake.Add(raw); err != nil {
			return fmt.Errorf("queueing transaction in the tx intake: %w", err)
		}
	}

	return nil
}

// GetTransactionReceipt returns the receipt for a transaction.
func (c *Client) GetTransactionReceipt(
	ctx context.Context,
	txHash common.Hash,
) (*types.Receipt, error) {
	receipt, err := c.ethClient.TransactionReceipt(ctx, txHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get receipt: %w", err)
	}

	return receipt, nil
}

// GetNonce returns the pending nonce for an address (includes mempool txs).
// Clients without a pending-block view (nimbus-eth1 rejects the "pending" block
// tag outright) fall back to the nonce at the latest block; callers that send
// transactions already treat the latest nonce as an authoritative floor.
func (c *Client) GetNonce(ctx context.Context, address common.Address) (uint64, error) {
	nonce, err := c.ethClient.PendingNonceAt(ctx, address)
	if err == nil {
		return nonce, nil
	}

	c.log.WithError(err).WithField("address", address.Hex()).
		Debug("Pending nonce unavailable, falling back to latest block nonce")

	nonce, latestErr := c.ethClient.NonceAt(ctx, address, nil)
	if latestErr != nil {
		return 0, fmt.Errorf("failed to get nonce: %w", errors.Join(err, latestErr))
	}

	return nonce, nil
}

// GetConfirmedNonce returns the account nonce at the latest block (state at head,
// excluding the mempool). It is reliable even on clients whose "pending" nonce is
// buggy: ethrex has been observed returning a pending nonce *below* the latest one,
// which would otherwise cause us to build an already-used ("too low") nonce.
func (c *Client) GetConfirmedNonce(ctx context.Context, address common.Address) (uint64, error) {
	nonce, err := c.ethClient.NonceAt(ctx, address, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to get confirmed nonce: %w", err)
	}

	return nonce, nil
}

// IsTxKnown reports whether the node currently knows the transaction (pending in the
// mempool or already mined). Returns false (without error) when the tx is unknown.
// This lets callers reason about transaction acceptance from node state rather than
// by parsing client-specific send error strings.
func (c *Client) IsTxKnown(ctx context.Context, txHash common.Hash) (bool, error) {
	_, _, err := c.ethClient.TransactionByHash(ctx, txHash)
	if err != nil {
		if errors.Is(err, ethereum.NotFound) {
			return false, nil
		}

		return false, fmt.Errorf("failed to look up transaction: %w", err)
	}

	return true, nil
}

// GetStorageAt returns the raw 32-byte value stored at slot for the given account
// at the latest block. Used to read the EIP-7002/7251/8282 system-contract queue
// excess (slot 0) for queue-fee calculation.
func (c *Client) GetStorageAt(
	ctx context.Context,
	account common.Address,
	slot common.Hash,
) ([]byte, error) {
	data, err := c.ethClient.StorageAt(ctx, account, slot, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get storage at %s slot %s: %w", account.Hex(), slot.Hex(), err)
	}

	return data, nil
}

// GetCode returns the contract code at the given address at the latest block.
// Used to verify a system contract predeploy actually exists before submitting
// requests to it.
func (c *Client) GetCode(ctx context.Context, address common.Address) ([]byte, error) {
	code, err := c.ethClient.CodeAt(ctx, address, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get code at %s: %w", address.Hex(), err)
	}

	return code, nil
}

// GetBalance returns the balance for an address.
func (c *Client) GetBalance(ctx context.Context, address common.Address) (*big.Int, error) {
	balance, err := c.ethClient.BalanceAt(ctx, address, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance: %w", err)
	}

	return balance, nil
}

// SuggestGasPrice returns the suggested gas price.
func (c *Client) SuggestGasPrice(ctx context.Context) (*big.Int, error) {
	gasPrice, err := c.ethClient.SuggestGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get gas price: %w", err)
	}

	return gasPrice, nil
}

// SuggestGasTipCap returns the suggested gas tip cap (priority fee).
func (c *Client) SuggestGasTipCap(ctx context.Context) (*big.Int, error) {
	gasTipCap, err := c.ethClient.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get gas tip cap: %w", err)
	}

	return gasTipCap, nil
}

// HeaderByNumber returns the header for a block number.
func (c *Client) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	header, err := c.ethClient.HeaderByNumber(ctx, number)
	if err != nil {
		return nil, fmt.Errorf("failed to get header: %w", err)
	}

	return header, nil
}

// CallContract executes a read-only message call against a contract. A nil
// blockNumber selects the latest block.
func (c *Client) CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error) {
	result, err := c.ethClient.CallContract(ctx, msg, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to call contract: %w", err)
	}

	return result, nil
}

// EstimateGas estimates gas for a transaction.
func (c *Client) EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error) {
	gas, err := c.ethClient.EstimateGas(ctx, msg)
	if err != nil {
		return 0, fmt.Errorf("failed to estimate gas: %w", err)
	}

	return gas, nil
}
