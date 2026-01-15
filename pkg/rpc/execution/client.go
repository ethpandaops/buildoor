package execution

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/sirupsen/logrus"
)

// Client handles standard JSON-RPC calls for wallet/transaction operations.
// Only needed when lifecycle management is enabled.
type Client struct {
	ethClient *ethclient.Client
	rpcClient *rpc.Client
	rpcURL    string
	log       logrus.FieldLogger
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

// GetNonce returns the pending nonce for an address.
func (c *Client) GetNonce(ctx context.Context, address common.Address) (uint64, error) {
	nonce, err := c.ethClient.PendingNonceAt(ctx, address)
	if err != nil {
		return 0, fmt.Errorf("failed to get nonce: %w", err)
	}

	return nonce, nil
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

// EstimateGas estimates gas for a transaction.
func (c *Client) EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error) {
	gas, err := c.ethClient.EstimateGas(ctx, msg)
	if err != nil {
		return 0, fmt.Errorf("failed to estimate gas: %w", err)
	}

	return gas, nil
}
