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

// Transaction submission tuning for the multi-instance-safe send path.
const (
	// txPollInterval is how often SendAndConfirm polls for a receipt / nonce progress.
	txPollInterval = 2 * time.Second
	// txMaxAttempts bounds nonce-conflict / displacement retries within a single send.
	txMaxAttempts = 8
	// txConflictBackoff is the pause before refetching the nonce after a conflict.
	txConflictBackoff = 500 * time.Millisecond
)

// walletBackend is the subset of execution-RPC operations the wallet depends on.
// It is satisfied by *execution.Client and lets transaction-submission logic be
// exercised against a fake in tests.
type walletBackend interface {
	GetChainID(ctx context.Context) (*big.Int, error)
	GetNonce(ctx context.Context, address common.Address) (uint64, error)
	GetConfirmedNonce(ctx context.Context, address common.Address) (uint64, error)
	GetBalance(ctx context.Context, address common.Address) (*big.Int, error)
	SuggestGasTipCap(ctx context.Context) (*big.Int, error)
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
	SendTransaction(ctx context.Context, tx *types.Transaction) error
	GetTransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error)
}

var _ walletBackend = (*execution.Client)(nil)

// Wallet manages an Ethereum wallet for transaction operations.
//
// The wallet is safe to use when multiple independent buildoor instances share the
// same funding key: each transaction sources a fresh on-chain nonce, txMu serializes
// this process's own submissions, and SendAndConfirm resolves cross-process nonce
// conflicts (refetch + retry) and detects its tx being displaced by another instance.
type Wallet struct {
	privkey    *ecdsa.PrivateKey
	address    common.Address
	rpcClient  walletBackend
	execClient *execution.Client // concrete handle exposed via GetRPCClient (e.g. for contract bindings)
	chainID    *big.Int
	mu         sync.Mutex // protects balance
	txMu       sync.Mutex // serializes this process's transaction submissions
	balance    *big.Int
	log        logrus.FieldLogger

	// Transaction confirmation tuning (defaults from the tx* consts; overridable in tests).
	pollInterval    time.Duration
	conflictBackoff time.Duration
	maxAttempts     int
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
		privkey:         privkey,
		address:         address,
		rpcClient:       rpcClient,
		execClient:      rpcClient,
		log:             walletLog,
		pollInterval:    txPollInterval,
		conflictBackoff: txConflictBackoff,
		maxAttempts:     txMaxAttempts,
	}, nil
}

// Address returns the wallet's Ethereum address.
func (w *Wallet) Address() common.Address {
	return w.address
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

	// Fetch the current nonce for visibility only. The nonce is always sourced fresh
	// from the chain at send time (see BuildTransaction / SendAndConfirm), so it is not
	// cached here — that is what keeps multiple instances sharing this key consistent.
	nonce, err := w.GetNonce(ctx)
	if err != nil {
		return err
	}

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

	// Source the nonce fresh from the chain (pending, includes mempool) on every build.
	// This keeps multiple instances sharing this funding key from stamping stale nonces;
	// genuine collisions are resolved by SendAndConfirm.
	nonce, err := w.rpcClient.GetNonce(ctx, w.address)
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %w", err)
	}

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

// SendAndConfirm builds, signs, sends, and confirms a transaction in a way that is
// safe when several buildoor instances share this funding key.
//
// Each attempt sources a fresh on-chain nonce. If the node rejects the send because
// another instance already took that nonce ("nonce too low" / "already known" /
// "replacement transaction underpriced"), it backs off and retries with a fresh nonce.
// While awaiting confirmation it watches the account's confirmed nonce: if the nonce
// advances past our transaction without our hash being mined, our transaction was
// displaced by another instance and we resubmit with the next free nonce.
//
// txMu serializes this process's own submissions so concurrent lifecycle operations
// (e.g. the startup deposit and a balance top-up) never collide with each other.
func (w *Wallet) SendAndConfirm(
	ctx context.Context,
	to common.Address,
	value *big.Int,
	data []byte,
	gasLimit uint64,
	timeout time.Duration,
) (*types.Receipt, error) {
	w.txMu.Lock()
	defer w.txMu.Unlock()

	var lastErr error

	for attempt := 1; attempt <= w.maxAttempts; attempt++ {
		signedTx, err := w.buildAndSign(ctx, to, value, data, gasLimit)
		if err != nil {
			return nil, err
		}

		nonce := signedTx.Nonce()

		if err := w.rpcClient.SendTransaction(ctx, signedTx); err != nil {
			if isNonceConflictErr(err) {
				lastErr = err

				w.log.WithFields(logrus.Fields{
					"nonce":   nonce,
					"attempt": attempt,
					"error":   err.Error(),
				}).Warn("Nonce conflict on send (another instance?), retrying with fresh nonce")

				if waitErr := sleepCtx(ctx, w.conflictBackoff); waitErr != nil {
					return nil, waitErr
				}

				continue
			}

			return nil, fmt.Errorf("failed to send transaction: %w", err)
		}

		w.log.WithFields(logrus.Fields{
			"hash":  signedTx.Hash().Hex(),
			"nonce": nonce,
			"to":    to.Hex(),
			"value": value.String(),
		}).Info("Transaction sent")

		receipt, displaced, err := w.awaitOrDisplaced(ctx, signedTx.Hash(), nonce, timeout)
		if displaced {
			lastErr = fmt.Errorf("transaction displaced at nonce %d", nonce)

			w.log.WithFields(logrus.Fields{
				"hash":    signedTx.Hash().Hex(),
				"nonce":   nonce,
				"attempt": attempt,
			}).Warn("Transaction displaced by another tx at this nonce, resubmitting")

			continue
		}

		if err != nil {
			return receipt, err
		}

		return receipt, nil
	}

	return nil, fmt.Errorf("transaction failed after %d attempts: %w", txMaxAttempts, lastErr)
}

// buildAndSign builds a transaction with a fresh nonce and signs it.
func (w *Wallet) buildAndSign(
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

	return w.SignTransaction(tx)
}

// awaitOrDisplaced polls for the transaction receipt while watching the account's
// confirmed nonce. It returns:
//   - (receipt, false, nil)        on successful inclusion
//   - (receipt, false, err)        if the transaction was included but reverted
//   - (nil, true, nil)             if the nonce was consumed by another tx (displaced)
//   - (nil, false, err)            on timeout
func (w *Wallet) awaitOrDisplaced(
	ctx context.Context,
	txHash common.Hash,
	nonce uint64,
	timeout time.Duration,
) (*types.Receipt, bool, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			return nil, false, fmt.Errorf("transaction confirmation timeout: %w", timeoutCtx.Err())

		case <-ticker.C:
			receipt, err := w.rpcClient.GetTransactionReceipt(ctx, txHash)
			if err == nil {
				if receipt.Status == types.ReceiptStatusFailed {
					return receipt, false, fmt.Errorf("transaction reverted")
				}

				w.log.WithFields(logrus.Fields{
					"hash":        txHash.Hex(),
					"blockNumber": receipt.BlockNumber.Uint64(),
					"gasUsed":     receipt.GasUsed,
				}).Info("Transaction confirmed")

				return receipt, false, nil
			}

			// No receipt yet: if the confirmed nonce has moved past ours, another
			// transaction filled this nonce slot and ours will never be mined.
			confirmedNonce, nerr := w.rpcClient.GetConfirmedNonce(ctx, w.address)
			if nerr == nil && confirmedNonce > nonce {
				return nil, true, nil
			}
		}
	}
}

// sleepCtx sleeps for d or returns early if ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isNonceConflictErr reports whether a send error indicates the chosen nonce was
// already taken (typically by another instance sharing this funding key), meaning a
// retry with a freshly fetched nonce is worthwhile.
func isNonceConflictErr(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())

	return strings.Contains(msg, "nonce too low") ||
		strings.Contains(msg, "already known") ||
		strings.Contains(msg, "known transaction") ||
		strings.Contains(msg, "replacement transaction underpriced")
}

// GetRPCClient returns the underlying concrete RPC client.
func (w *Wallet) GetRPCClient() *execution.Client {
	return w.execClient
}
