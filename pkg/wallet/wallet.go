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
	GetBalance(ctx context.Context, address common.Address) (*big.Int, error)
	SuggestGasTipCap(ctx context.Context) (*big.Int, error)
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
	SendTransaction(ctx context.Context, tx *types.Transaction) error
	GetTransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error)
	IsTxKnown(ctx context.Context, txHash common.Hash) (bool, error)
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

	// Always read the next free nonce straight from the node on every build — never
	// cached or tracked internally. This is the only source of truth for the nonce.
	nonce, err := w.rpcClient.GetNonce(ctx, w.address)
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %w", err)
	}

	w.log.WithFields(logrus.Fields{
		"address": w.address.Hex(),
		"nonce":   nonce,
	}).Debug("Fetched next nonce from RPC for transaction build")

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

// txOutcome is the decision reached after observing chain state for a sent tx.
type txOutcome int

const (
	outcomePending  txOutcome = iota // not yet resolved, keep polling
	outcomeIncluded                  // mined successfully
	outcomeReverted                  // mined but reverted
	outcomeRetry                     // our nonce slot was taken (or tx dropped); resubmit fresh
	outcomeFailed                    // the send genuinely failed; surface the error
)

// SendAndConfirm builds, signs, sends, and confirms a transaction in a way that is
// safe when several buildoor instances share this funding key.
//
// Each attempt sources a fresh on-chain nonce. Crucially, it never parses the send
// error string (which varies per execution client). Instead it decides what to do by
// observing node state: whether our specific transaction is known to the node, and
// where the account's pending nonce sits relative to the nonce we used.
//
//   - our tx is known (pending/mined) -> wait for its receipt
//   - our tx is unknown and the pending nonce has moved past ours -> another tx (likely
//     another instance) took our slot; resubmit with a fresh nonce
//   - our tx is unknown, the nonce is still free, and the send errored -> genuine
//     failure; surface it
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

		// Send, but do not branch on the error text. sendErr (if any) only matters as a
		// tie-breaker once node state shows the tx never entered the pool.
		sendErr := w.rpcClient.SendTransaction(ctx, signedTx)
		if sendErr == nil {
			w.log.WithFields(logrus.Fields{
				"hash":  signedTx.Hash().Hex(),
				"nonce": nonce,
				"to":    to.Hex(),
				"value": value.String(),
			}).Info("Transaction sent")
		}

		receipt, outcome, err := w.resolve(ctx, signedTx.Hash(), nonce, sendErr, timeout)

		switch outcome {
		case outcomeIncluded:
			return receipt, nil
		case outcomeReverted:
			return receipt, err
		case outcomeRetry:
			lastErr = err

			w.log.WithFields(logrus.Fields{
				"hash":    signedTx.Hash().Hex(),
				"nonce":   nonce,
				"attempt": attempt,
			}).Warn("Nonce slot taken by another tx, resubmitting with a fresh nonce")

			if waitErr := sleepCtx(ctx, w.conflictBackoff); waitErr != nil {
				return nil, waitErr
			}

			continue
		case outcomeFailed:
			return nil, fmt.Errorf("send transaction (nonce %d): %w", nonce, err)
		case outcomePending:
			// resolve only returns terminal outcomes; treat as failure defensively.
			return nil, fmt.Errorf("send transaction (nonce %d): %w", nonce, err)
		}
	}

	return nil, fmt.Errorf("transaction failed after %d attempts: %w", w.maxAttempts, lastErr)
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

// resolve polls node state until it can decide the fate of a sent transaction, without
// parsing the send error. It returns a terminal txOutcome (never outcomePending) plus,
// where relevant, the receipt and/or the error to surface.
func (w *Wallet) resolve(
	ctx context.Context,
	txHash common.Hash,
	nonce uint64,
	sendErr error,
	timeout time.Duration,
) (*types.Receipt, txOutcome, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			if sendErr != nil {
				return nil, outcomeFailed, sendErr
			}

			return nil, outcomeFailed, fmt.Errorf("transaction confirmation timeout: %w", timeoutCtx.Err())

		case <-ticker.C:
			// 1. Mined?
			if receipt, err := w.rpcClient.GetTransactionReceipt(ctx, txHash); err == nil {
				if receipt.Status == types.ReceiptStatusFailed {
					return receipt, outcomeReverted, fmt.Errorf("transaction reverted")
				}

				w.log.WithFields(logrus.Fields{
					"hash":        txHash.Hex(),
					"blockNumber": receipt.BlockNumber.Uint64(),
					"gasUsed":     receipt.GasUsed,
				}).Info("Transaction confirmed")

				return receipt, outcomeIncluded, nil
			}

			// 2. Is our specific tx still known to the node (pending in the mempool)?
			//    If so, it is alive and we simply keep waiting for it.
			known, err := w.rpcClient.IsTxKnown(ctx, txHash)
			if err != nil {
				continue // transient lookup error; retry on next tick
			}

			if known {
				continue
			}

			// 3. Our tx is not known. Decide by where the node's next nonce now sits.
			pendingNonce, perr := w.rpcClient.GetNonce(ctx, w.address)
			if perr != nil {
				continue
			}

			if pendingNonce > nonce {
				// Our nonce slot was consumed (by another instance/process); ours can never
				// land. Resubmit with a fresh, higher nonce.
				return nil, outcomeRetry, fmt.Errorf("nonce %d consumed by another transaction", nonce)
			}

			// 4. Nonce is still free and our tx is gone. If the send errored, it never
			//    entered the pool -> genuine failure. Otherwise it was dropped -> resubmit.
			if sendErr != nil {
				return nil, outcomeFailed, sendErr
			}

			return nil, outcomeRetry, fmt.Errorf("transaction at nonce %d dropped before inclusion", nonce)
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

// GetRPCClient returns the underlying concrete RPC client.
func (w *Wallet) GetRPCClient() *execution.Client {
	return w.execClient
}
