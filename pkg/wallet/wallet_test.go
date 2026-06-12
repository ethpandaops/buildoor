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

var errNotFound = errors.New("not found")

// fakeBackend models an execution node for SendAndConfirm. It deliberately tracks node
// state (which txs are known, where the account nonce sits) so the wallet's decisions
// can be exercised without parsing any send error strings — production code keys purely
// off this state, and the error returned from SendTransaction is opaque to it.
type fakeBackend struct {
	mu sync.Mutex

	pendingNonce   uint64
	confirmedNonce uint64
	sendCalls      int
	lastNonce      uint64 // nonce of the most recently accepted tx

	// known maps an accepted tx hash to its acceptance order (1-based).
	known    map[common.Hash]int
	accepted int

	// displaceFirstN: the first N accepted txs are dropped before inclusion — their
	// first receipt poll returns not-found and removes them from the pool, modelling
	// another instance replacing our tx at the same nonce.
	displaceFirstN int

	// sendBehavior, given the 1-based send-call number, decides the send result. A
	// non-nil error rejects the send; bumpNonce models another instance taking the
	// nonce slot (advances the account's pending nonce).
	sendBehavior func(call int) (err error, bumpNonce bool)
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{known: make(map[common.Hash]int)}
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

	var (
		err  error
		bump bool
	)

	if f.sendBehavior != nil {
		err, bump = f.sendBehavior(f.sendCalls)
	}

	if err != nil {
		if bump {
			// Another instance/process consumed this nonce slot and it mined.
			f.pendingNonce++
			f.confirmedNonce++
		}

		return err
	}

	f.accepted++
	f.known[tx.Hash()] = f.accepted
	f.lastNonce = tx.Nonce()

	if tx.Nonce()+1 > f.pendingNonce {
		f.pendingNonce = tx.Nonce() + 1
	}

	if tx.Nonce()+1 > f.confirmedNonce {
		f.confirmedNonce = tx.Nonce() + 1
	}

	return nil
}

func (f *fakeBackend) GetTransactionReceipt(_ context.Context, txHash common.Hash) (*types.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	idx, ok := f.known[txHash]
	if !ok {
		return nil, errNotFound
	}

	if idx <= f.displaceFirstN {
		delete(f.known, txHash) // dropped from the pool; the nonce slot stays taken
		return nil, errNotFound
	}

	return &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(100)}, nil
}

func (f *fakeBackend) IsTxKnown(_ context.Context, txHash common.Hash) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	_, ok := f.known[txHash]

	return ok, nil
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

func sendOnce(t *testing.T, w *Wallet) (*types.Receipt, error) {
	t.Helper()

	return w.SendAndConfirm(
		context.Background(), common.Address{}, big.NewInt(1), nil, 21000, 5*time.Second,
	)
}

func TestSendAndConfirmHappyPath(t *testing.T) {
	backend := newFakeBackend()
	w := newTestWallet(t, backend)

	receipt, err := sendOnce(t, w)
	require.NoError(t, err)
	require.Equal(t, types.ReceiptStatusSuccessful, receipt.Status)
	require.Equal(t, 1, backend.sendCalls)
}

// TestBuildTransactionAlwaysReadsNonceFromRPC asserts the nonce is taken straight from
// the node on every build (never cached/tracked): whatever the node reports as the next
// nonce is exactly what gets stamped.
func TestBuildTransactionAlwaysReadsNonceFromRPC(t *testing.T) {
	backend := newFakeBackend()
	w := newTestWallet(t, backend)

	backend.pendingNonce = 88

	receipt, err := sendOnce(t, w)
	require.NoError(t, err)
	require.Equal(t, types.ReceiptStatusSuccessful, receipt.Status)
	require.Equal(t, uint64(88), backend.lastNonce, "must build with the nonce the node reports")

	// A subsequent send picks up the node's new next nonce with no internal carry-over.
	backend.pendingNonce = 92
	backend.confirmedNonce = 92

	_, err = sendOnce(t, w)
	require.NoError(t, err)
	require.Equal(t, uint64(92), backend.lastNonce)
}

// TestSendAndConfirmEthrexStalePendingNonce reproduces the observed ethrex behaviour:
// the pending nonce (0x51 = 81) is reported *below* the latest/confirmed nonce
// (0x59 = 89). Building off the pending nonce alone stamps an already-used "too low"
// nonce; the wallet must floor at the confirmed nonce and send 89 on the first attempt.
func TestSendAndConfirmEthrexStalePendingNonce(t *testing.T) {
	backend := newFakeBackend()
	backend.pendingNonce = 81   // ethrex's stale/broken pending nonce
	backend.confirmedNonce = 89 // authoritative latest nonce
	w := newTestWallet(t, backend)

	receipt, err := sendOnce(t, w)
	require.NoError(t, err)
	require.Equal(t, types.ReceiptStatusSuccessful, receipt.Status)
	require.Equal(t, 1, backend.sendCalls, "should succeed on first attempt")
	require.Equal(t, uint64(89), backend.lastNonce, "must build off the confirmed nonce, not the stale pending nonce")
}

// TestSendAndConfirmNonceConflictThenSuccess reproduces the reported failure: the EL
// rejects the first send (here with a "nonce too low"-style error) while another
// instance consumes the nonce. The wallet must NOT treat this as fatal — it should
// observe the advanced pending nonce and resubmit with a fresh nonce.
func TestSendAndConfirmNonceConflictThenSuccess(t *testing.T) {
	backend := newFakeBackend()
	backend.sendBehavior = func(call int) (error, bool) {
		if call == 1 {
			// Opaque, client-specific phrasing — production code must not parse it.
			return errors.New("Invalid params: Nonce for account too low"), true
		}

		return nil, false
	}
	w := newTestWallet(t, backend)

	receipt, err := sendOnce(t, w)
	require.NoError(t, err)
	require.Equal(t, types.ReceiptStatusSuccessful, receipt.Status)
	require.Equal(t, 2, backend.sendCalls, "should resubmit after the nonce was taken")
}

// TestSendAndConfirmDisplacedThenSuccess covers our tx being accepted into the pool but
// then dropped/replaced (by another instance) before inclusion.
func TestSendAndConfirmDisplacedThenSuccess(t *testing.T) {
	backend := newFakeBackend()
	backend.displaceFirstN = 1
	w := newTestWallet(t, backend)

	receipt, err := sendOnce(t, w)
	require.NoError(t, err)
	require.Equal(t, types.ReceiptStatusSuccessful, receipt.Status)
	require.Equal(t, 2, backend.sendCalls, "should resubmit after displacement")
}

// TestSendAndConfirmFatalErrorNoRetry covers a genuine failure (e.g. insufficient
// funds): the send errors, the tx never enters the pool, and the nonce slot stays free.
// The wallet must surface the error without retrying.
func TestSendAndConfirmFatalErrorNoRetry(t *testing.T) {
	backend := newFakeBackend()
	backend.sendBehavior = func(int) (error, bool) {
		return errors.New("insufficient funds for gas * price + value"), false
	}
	w := newTestWallet(t, backend)

	_, err := sendOnce(t, w)
	require.Error(t, err)
	require.Equal(t, 1, backend.sendCalls, "fatal send error must not retry")
}
