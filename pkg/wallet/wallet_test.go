package wallet

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestIsNonceConflictErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"nonce too low", errors.New("nonce too low"), true},
		{"already known", errors.New("already known"), true},
		{"known transaction", errors.New("known transaction: abc123"), true},
		{"replacement underpriced", errors.New("replacement transaction underpriced"), true},
		{"mixed case", errors.New("RPC error: Nonce Too Low"), true},
		{"insufficient funds", errors.New("insufficient funds for gas * price + value"), false},
		{"generic", errors.New("connection refused"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isNonceConflictErr(tt.err))
		})
	}
}

// fakeBackend is a programmable walletBackend for exercising SendAndConfirm.
type fakeBackend struct {
	mu sync.Mutex

	pendingNonce   uint64
	confirmedNonce uint64

	// sendResults are returned by successive SendTransaction calls (nil = accept).
	sendResults []error
	sendCalls   int
	sentHashes  []common.Hash

	// receipts maps a tx hash to the receipt GetTransactionReceipt should return.
	receipts map[common.Hash]*types.Receipt
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{receipts: make(map[common.Hash]*types.Receipt)}
}

func (f *fakeBackend) GetChainID(context.Context) (*big.Int, error) { return big.NewInt(1337), nil }

func (f *fakeBackend) GetNonce(context.Context, common.Address) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.pendingNonce, nil
}

func (f *fakeBackend) GetConfirmedNonce(context.Context, common.Address) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.confirmedNonce, nil
}

func (f *fakeBackend) GetBalance(context.Context, common.Address) (*big.Int, error) {
	return big.NewInt(0), nil
}

func (f *fakeBackend) SuggestGasTipCap(context.Context) (*big.Int, error) {
	return big.NewInt(1_000_000_000), nil
}

func (f *fakeBackend) HeaderByNumber(context.Context, *big.Int) (*types.Header, error) {
	return &types.Header{BaseFee: big.NewInt(1_000_000_000)}, nil
}

func (f *fakeBackend) SendTransaction(_ context.Context, tx *types.Transaction) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sendCalls++

	var err error
	if len(f.sendResults) > 0 {
		err = f.sendResults[0]
		f.sendResults = f.sendResults[1:]
	}

	if err != nil {
		return err
	}

	f.sentHashes = append(f.sentHashes, tx.Hash())

	return nil
}

func (f *fakeBackend) GetTransactionReceipt(_ context.Context, txHash common.Hash) (*types.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if r, ok := f.receipts[txHash]; ok {
		return r, nil
	}

	return nil, errors.New("not found")
}

// newTestWallet builds a Wallet wired to the fake backend with fast tx timings.
func newTestWallet(t *testing.T, backend walletBackend) *Wallet {
	t.Helper()

	priv, err := crypto.GenerateKey()
	require.NoError(t, err)

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	return &Wallet{
		privkey:         priv,
		address:         crypto.PubkeyToAddress(priv.PublicKey),
		rpcClient:       backend,
		log:             log,
		pollInterval:    time.Millisecond,
		conflictBackoff: time.Millisecond,
		maxAttempts:     8,
	}
}

func successReceipt() *types.Receipt {
	return &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(100)}
}

// arrangeReceiptOnSend records a success receipt for whatever hash gets sent, so the
// next GetTransactionReceipt poll resolves it. Used after a SendAndConfirm send lands.
func (f *fakeBackend) markLastSentConfirmed() {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.sentHashes) == 0 {
		return
	}

	f.receipts[f.sentHashes[len(f.sentHashes)-1]] = successReceipt()
}

func TestSendAndConfirmHappyPath(t *testing.T) {
	backend := newFakeBackend()
	w := newTestWallet(t, backend)

	// Confirm the tx as soon as it is sent by watching in a goroutine.
	go func() {
		for {
			backend.mu.Lock()
			sent := len(backend.sentHashes) > 0
			backend.mu.Unlock()

			if sent {
				backend.markLastSentConfirmed()
				return
			}

			time.Sleep(time.Millisecond)
		}
	}()

	receipt, err := w.SendAndConfirm(
		context.Background(), common.Address{}, big.NewInt(1), nil, 21000, 5*time.Second,
	)
	require.NoError(t, err)
	require.Equal(t, types.ReceiptStatusSuccessful, receipt.Status)
	require.Equal(t, 1, backend.sendCalls)
}

func TestSendAndConfirmNonceConflictThenSuccess(t *testing.T) {
	backend := newFakeBackend()
	// First send is rejected as a nonce conflict; second send is accepted.
	backend.sendResults = []error{errors.New("nonce too low")}
	w := newTestWallet(t, backend)

	go func() {
		for {
			backend.mu.Lock()
			sent := len(backend.sentHashes) > 0
			backend.mu.Unlock()

			if sent {
				backend.markLastSentConfirmed()
				return
			}

			time.Sleep(time.Millisecond)
		}
	}()

	receipt, err := w.SendAndConfirm(
		context.Background(), common.Address{}, big.NewInt(1), nil, 21000, 5*time.Second,
	)
	require.NoError(t, err)
	require.Equal(t, types.ReceiptStatusSuccessful, receipt.Status)
	require.Equal(t, 2, backend.sendCalls, "should retry once after the nonce conflict")
}

func TestSendAndConfirmDisplacedThenSuccess(t *testing.T) {
	backend := newFakeBackend()
	w := newTestWallet(t, backend)

	// Drive the displacement: the first sent tx never gets a receipt, but the confirmed
	// nonce advances past it (another instance filled the slot). The retry's tx is then
	// confirmed normally.
	var once sync.Once

	go func() {
		for {
			backend.mu.Lock()
			calls := backend.sendCalls
			backend.mu.Unlock()

			switch {
			case calls == 1:
				// Simulate our nonce being consumed by someone else.
				once.Do(func() {
					backend.mu.Lock()
					backend.confirmedNonce = backend.pendingNonce + 1
					backend.mu.Unlock()
				})
			case calls >= 2:
				backend.markLastSentConfirmed()
				return
			}

			time.Sleep(time.Millisecond)
		}
	}()

	receipt, err := w.SendAndConfirm(
		context.Background(), common.Address{}, big.NewInt(1), nil, 21000, 5*time.Second,
	)
	require.NoError(t, err)
	require.Equal(t, types.ReceiptStatusSuccessful, receipt.Status)
	require.GreaterOrEqual(t, backend.sendCalls, 2, "should resubmit after displacement")
}

func TestSendAndConfirmFatalErrorNoRetry(t *testing.T) {
	backend := newFakeBackend()
	backend.sendResults = []error{errors.New("insufficient funds for gas * price + value")}
	w := newTestWallet(t, backend)

	_, err := w.SendAndConfirm(
		context.Background(), common.Address{}, big.NewInt(1), nil, 21000, 5*time.Second,
	)
	require.Error(t, err)
	require.Equal(t, 1, backend.sendCalls, "fatal send error must not retry")
}
