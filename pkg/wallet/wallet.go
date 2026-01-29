// Package wallet provides Ethereum wallet management for transaction signing.
package wallet

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
)

// Wallet manages an Ethereum wallet for transaction operations.
type Wallet struct {
	privkey      *ecdsa.PrivateKey
	address      common.Address
	rpcClient    *execution.Client
	chainID      *big.Int
	mu           sync.Mutex
	pendingNonce uint64
	balance      *big.Int
	log          logrus.FieldLogger
}

// NewWallet creates a new wallet from a hex-encoded private key.
func NewWallet(
	privkeyHex string,
	rpcClient *execution.Client,
	log logrus.FieldLogger,
) (*Wallet, error) {
	walletLog := log.WithField("component", "wallet")

	privkeyHex = strings.TrimPrefix(privkeyHex, "0x")

	privkeyBytes, err := hex.DecodeString(privkeyHex)
	if err != nil {
		return nil, fmt.Errorf("failed to decode private key hex: %w", err)
	}

	if len(privkeyBytes) != 32 {
		return nil, fmt.Errorf("private key must be 32 bytes, got %d", len(privkeyBytes))
	}

	privkey, err := crypto.ToECDSA(privkeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	address := crypto.PubkeyToAddress(privkey.PublicKey)

	return &Wallet{
		privkey:   privkey,
		address:   address,
		rpcClient: rpcClient,
		log:       walletLog,
	}, nil
}

// Address returns the wallet's Ethereum address.
func (w *Wallet) Address() common.Address {
	return w.address
}

// PendingNonce returns the next nonce the wallet will use when building txs.
// This is set by Sync() and then incremented locally on each BuildTransaction().
func (w *Wallet) PendingNonce() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.pendingNonce
}

// GetBalance fetches the current balance from the chain.
func (w *Wallet) GetBalance(ctx context.Context) (*big.Int, error) {
	balance, err := w.rpcClient.GetBalance(ctx, w.address)
	if err != nil {
		return nil, fmt.Errorf("failed to get balance: %w", err)
	}

	w.mu.Lock()
	w.balance = balance
	w.mu.Unlock()

	return balance, nil
}

// GetNonce fetches the current nonce from the chain.
func (w *Wallet) GetNonce(ctx context.Context) (uint64, error) {
	nonce, err := w.rpcClient.GetNonce(ctx, w.address)
	if err != nil {
		return 0, fmt.Errorf("failed to get nonce: %w", err)
	}

	return nonce, nil
}

// Sync synchronizes the wallet state with the chain.
func (w *Wallet) Sync(ctx context.Context) error {
	// Get chain ID if not set
	if w.chainID == nil {
		chainID, err := w.rpcClient.GetChainID(ctx)
		if err != nil {
			return fmt.Errorf("failed to get chain ID: %w", err)
		}

		w.chainID = chainID
	}

	// Sync nonce
	nonce, err := w.GetNonce(ctx)
	if err != nil {
		return err
	}

	w.mu.Lock()
	w.pendingNonce = nonce
	w.mu.Unlock()

	// Sync balance
	if _, err := w.GetBalance(ctx); err != nil {
		return err
	}

	w.log.WithFields(logrus.Fields{
		"address": w.address.Hex(),
		"nonce":   nonce,
		"balance": w.balance.String(),
	}).Debug("Wallet synced")

	return nil
}

// BuildTransaction creates a new unsigned transaction.
func (w *Wallet) BuildTransaction(
	ctx context.Context,
	to common.Address,
	value *big.Int,
	data []byte,
	gasLimit uint64,
) (*types.Transaction, error) {
	// Ensure chain ID is available
	if w.chainID == nil {
		chainID, err := w.rpcClient.GetChainID(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get chain ID: %w", err)
		}

		w.chainID = chainID
	}

	// Get gas tip cap (priority fee)
	gasTipCap, err := w.rpcClient.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get gas tip cap: %w", err)
	}

	// Get base fee from latest header
	header, err := w.rpcClient.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest header: %w", err)
	}

	// Set gas fee cap to base fee * 2 + tip
	baseFee := header.BaseFee
	gasFeeCap := new(big.Int).Mul(baseFee, big.NewInt(2))
	gasFeeCap.Add(gasFeeCap, gasTipCap)

	// Get nonce
	w.mu.Lock()
	nonce := w.pendingNonce
	w.pendingNonce++
	w.mu.Unlock()

	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   w.chainID,
		Nonce:     nonce,
		GasTipCap: gasTipCap,
		GasFeeCap: gasFeeCap,
		Gas:       gasLimit,
		To:        &to,
		Value:     value,
		Data:      data,
	})

	return tx, nil
}

// SignTransaction signs a transaction with the wallet's private key.
func (w *Wallet) SignTransaction(tx *types.Transaction) (*types.Transaction, error) {
	if w.chainID == nil {
		return nil, fmt.Errorf("chain ID not set, call Sync first")
	}

	signer := types.NewCancunSigner(w.chainID)

	signedTx, err := types.SignTx(tx, signer, w.privkey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	return signedTx, nil
}

// SendTransaction sends a signed transaction.
func (w *Wallet) SendTransaction(ctx context.Context, tx *types.Transaction) error {
	if err := w.rpcClient.SendTransaction(ctx, tx); err != nil {
		return fmt.Errorf("failed to send transaction: %w", err)
	}

	w.log.WithFields(logrus.Fields{
		"hash":  tx.Hash().Hex(),
		"nonce": tx.Nonce(),
		"to":    tx.To().Hex(),
		"value": tx.Value().String(),
	}).Info("Transaction sent")

	return nil
}

// Await waits for a transaction to be confirmed.
func (w *Wallet) Await(
	ctx context.Context,
	txHash common.Hash,
	timeout time.Duration,
) (*types.Receipt, error) {
	// Create timeout context
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Poll for receipt
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			return nil, fmt.Errorf("transaction confirmation timeout: %w", timeoutCtx.Err())

		case <-ticker.C:
			receipt, err := w.rpcClient.GetTransactionReceipt(ctx, txHash)
			if err != nil {
				// Receipt not yet available
				continue
			}

			if receipt.Status == types.ReceiptStatusFailed {
				return receipt, fmt.Errorf("transaction failed")
			}

			w.log.WithFields(logrus.Fields{
				"hash":        txHash.Hex(),
				"blockNumber": receipt.BlockNumber.Uint64(),
				"gasUsed":     receipt.GasUsed,
			}).Info("Transaction confirmed")

			return receipt, nil
		}
	}
}

// BuildAndSend builds, signs, and sends a transaction.
func (w *Wallet) BuildAndSend(
	ctx context.Context,
	to common.Address,
	value *big.Int,
	data []byte,
	gasLimit uint64,
) (*types.Transaction, error) {
	tx, err := w.BuildTransaction(ctx, to, value, data, gasLimit)
	if err != nil {
		return nil, err
	}

	signedTx, err := w.SignTransaction(tx)
	if err != nil {
		return nil, err
	}

	if err := w.SendTransaction(ctx, signedTx); err != nil {
		return nil, err
	}

	return signedTx, nil
}

// GetRPCClient returns the underlying RPC client.
func (w *Wallet) GetRPCClient() *execution.Client {
	return w.rpcClient
}
