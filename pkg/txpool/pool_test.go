package txpool

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
)

var testChainID = big.NewInt(1234)

// stubChainService provides the minimal chain.Service surface the pool uses.
type stubChainService struct {
	chain.Service
	slot     phase0.Slot
	fork     version.DataVersion
	hezeSlot phase0.Slot
}

func (s *stubChainService) GetCurrentSlot() phase0.Slot { return s.slot }
func (s *stubChainService) GetEpochOfSlot(slot phase0.Slot) phase0.Epoch {
	return phase0.Epoch(slot / 32)
}
func (s *stubChainService) ActiveForkAtEpoch(phase0.Epoch) version.DataVersion { return s.fork }
func (s *stubChainService) GetHeadTracker() *chain.HeadTracker {
	return nil
}

// The stub chain runs 12s slots from genesis at unix 0, 9s slots from the
// slot set in hezeSlot (0 = never).
func (s *stubChainService) SlotToTime(slot phase0.Slot) time.Time {
	return time.Unix(0, 0).Add(s.spec().SlotStartOffset(slot))
}

func (s *stubChainService) TimeToSlot(t time.Time) phase0.Slot {
	return s.spec().SlotAtOffset(t.Sub(time.Unix(0, 0)))
}

func (s *stubChainService) spec() *chain.ChainSpec {
	spec := &chain.ChainSpec{SlotDuration: 12 * time.Second, SlotsPerEpoch: 32}
	if s.hezeSlot > 0 {
		spec.ForkSchedule = []chain.ForkSchedule{{Fork: version.DataVersionHeze, Epoch: phase0.Epoch(s.hezeSlot / 32)}}
		spec.ForkSlotDurations = map[version.DataVersion]time.Duration{version.DataVersionHeze: 9 * time.Second}
	}

	return spec
}

// fakeEL is an in-memory ELClient.
type fakeEL struct {
	mu          sync.Mutex
	header      *types.Header
	states      map[common.Address]*execution.AccountState
	blobBaseFee *big.Int
	blocks      map[common.Hash]*execution.BlockTxList
	txBlocks    map[common.Hash]common.Hash // tx hash -> including block
	lookupErr   error                       // TransactionBlockHashes failure
	sent        []common.Hash

	// The EL mempool: txpool_content lists pending (no blob transactions,
	// like geth), the filter reports arrivals, lookups resolve mempool.
	pending  []*types.Transaction
	mempool  map[common.Hash]*types.Transaction
	arrivals []common.Hash
	filters  int // installed filters

	stateReads []common.Address // accounts AccountStatesAt was asked for
}

func (f *fakeEL) PendingPoolTransactions(context.Context) ([]*types.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]*types.Transaction(nil), f.pending...), nil
}

func (f *fakeEL) NewPendingTransactionFilter(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.filters++

	return "0x1", nil
}

func (f *fakeEL) PendingTransactionFilterChanges(context.Context, string) ([]common.Hash, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := f.arrivals
	f.arrivals = nil

	return out, nil
}

func (f *fakeEL) UninstallFilter(context.Context, string) error { return nil }

func (f *fakeEL) TransactionsByHash(_ context.Context, hashes []common.Hash) (map[common.Hash]*types.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make(map[common.Hash]*types.Transaction, len(hashes))
	for _, hash := range hashes {
		if tx := f.mempool[hash]; tx != nil {
			out[hash] = tx
		}
	}

	return out, nil
}

func newFakeEL() *fakeEL {
	return &fakeEL{
		header: &types.Header{
			Number:   big.NewInt(100),
			GasLimit: 30_000_000,
			GasUsed:  15_000_000,
			BaseFee:  big.NewInt(1_000_000_000),
		},
		states:      make(map[common.Address]*execution.AccountState),
		blobBaseFee: big.NewInt(1),
		blocks:      make(map[common.Hash]*execution.BlockTxList),
		txBlocks:    make(map[common.Hash]common.Hash),
		mempool:     make(map[common.Hash]*types.Transaction),
	}
}

func (f *fakeEL) TransactionBlockHashes(_ context.Context, hashes []common.Hash) (map[common.Hash]common.Hash, error) {
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}

	out := make(map[common.Hash]common.Hash, len(hashes))
	for _, h := range hashes {
		out[h] = f.txBlocks[h]
	}

	return out, nil
}

func (f *fakeEL) GetChainID(context.Context) (*big.Int, error) { return testChainID, nil }
func (f *fakeEL) HeaderByHash(context.Context, common.Hash) (*types.Header, error) {
	return f.header, nil
}

func (f *fakeEL) AccountStatesAt(_ context.Context, addrs []common.Address, _ common.Hash, _ uint64,
) (map[common.Address]*execution.AccountState, error) {
	out := make(map[common.Address]*execution.AccountState, len(addrs))

	f.mu.Lock()
	f.stateReads = append(f.stateReads, addrs...)
	f.mu.Unlock()

	for _, addr := range addrs {
		state, ok := f.states[addr]
		if !ok {
			state = &execution.AccountState{Nonce: 0, Balance: new(big.Int)}
		}

		out[addr] = &execution.AccountState{Nonce: state.Nonce, Balance: new(big.Int).Set(state.Balance)}
	}

	return out, nil
}

func (f *fakeEL) BlobBaseFee(context.Context) (*big.Int, error) { return f.blobBaseFee, nil }
func (f *fakeEL) BlockTransactions(_ context.Context, hash common.Hash) (*execution.BlockTxList, bool, error) {
	block, ok := f.blocks[hash]

	return block, ok, nil
}

func (f *fakeEL) SendRawTransaction(_ context.Context, raw []byte) (common.Hash, error) {
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(raw); err != nil {
		return common.Hash{}, err
	}

	f.mu.Lock()
	f.sent = append(f.sent, tx.Hash())
	f.mu.Unlock()

	return tx.Hash(), nil
}

func (f *fakeEL) sentHashes() []common.Hash {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]common.Hash(nil), f.sent...)
}

type account struct {
	key  *ecdsa.PrivateKey
	addr common.Address
}

func newAccount(t *testing.T) account {
	t.Helper()

	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	return account{key: key, addr: crypto.PubkeyToAddress(key.PublicKey)}
}

type txOpts struct {
	nonce  uint64
	gas    uint64
	feeCap int64
	tip    int64
	value  int64
}

func signTx(t *testing.T, acc account, opts txOpts) (*types.Transaction, []byte) {
	t.Helper()

	if opts.gas == 0 {
		opts.gas = 21000
	}

	if opts.feeCap == 0 {
		opts.feeCap = 2_000_000_000
	}

	if opts.tip == 0 {
		opts.tip = 1_000_000_000
	}

	to := common.Address{0x42}
	tx, err := types.SignNewTx(acc.key, types.LatestSignerForChainID(testChainID), &types.DynamicFeeTx{
		ChainID:   testChainID,
		Nonce:     opts.nonce,
		GasTipCap: big.NewInt(opts.tip),
		GasFeeCap: big.NewInt(opts.feeCap),
		Gas:       opts.gas,
		To:        &to,
		Value:     big.NewInt(opts.value),
	})
	require.NoError(t, err)

	raw, err := tx.MarshalBinary()
	require.NoError(t, err)

	return tx, raw
}

func newTestPool(t *testing.T, el *fakeEL) (*Pool, *config.Config, *stubChainService) {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.TxPool.Enabled = true

	chainSvc := &stubChainService{slot: 10}
	pool := NewPool(cfg, el, chainSvc, nil, logrus.New())

	// Start without the eviction loop: set the signer directly.
	pool.mu.Lock()
	pool.chainID = testChainID
	pool.signer = types.LatestSignerForChainID(testChainID)
	pool.mu.Unlock()

	return pool, cfg, chainSvc
}

func fund(el *fakeEL, acc account, nonce uint64, balanceEth int64) {
	el.states[acc.addr] = &execution.AccountState{
		Nonce:   nonce,
		Balance: new(big.Int).Mul(big.NewInt(balanceEth), big.NewInt(1e18)),
	}
}

func TestAddAdmitsAndRejects(t *testing.T) {
	el := newFakeEL()
	pool, cfg, _ := newTestPool(t, el)
	acc := newAccount(t)

	tx, raw := signTx(t, acc, txOpts{nonce: 0})

	hash, err := pool.Add(raw)
	require.NoError(t, err)
	require.Equal(t, tx.Hash(), hash)

	_, err = pool.Add(raw)
	require.ErrorIs(t, err, ErrAlreadyKnown)

	_, err = pool.Add([]byte{0xde, 0xad})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid transaction encoding")

	// Wrong chain id.
	other, err := types.SignNewTx(acc.key, types.LatestSignerForChainID(big.NewInt(99)), &types.DynamicFeeTx{
		ChainID: big.NewInt(99), Nonce: 1, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2), Gas: 21000,
	})
	require.NoError(t, err)

	otherRaw, err := other.MarshalBinary()
	require.NoError(t, err)

	_, err = pool.Add(otherRaw)
	require.ErrorIs(t, err, ErrChainID)

	// Disabled pool refuses admission but keeps content.
	pool.SetEnabled(false)

	_, moreRaw := signTx(t, acc, txOpts{nonce: 1})
	_, err = pool.Add(moreRaw)
	require.ErrorIs(t, err, ErrDisabled)
	require.Equal(t, 1, pool.Stats().Pending)

	pool.SetEnabled(true)

	// Per-sender cap.
	cfg.TxPool.MaxTxsPerSender = 2

	_, err = pool.Add(moreRaw)
	require.NoError(t, err)

	_, capRaw := signTx(t, acc, txOpts{nonce: 2})
	_, err = pool.Add(capRaw)
	require.ErrorIs(t, err, ErrSenderFull)

	// Pool cap.
	cfg.TxPool.MaxTxsPerSender = 256
	cfg.TxPool.MaxPoolTxs = 2

	_, err = pool.Add(capRaw)
	require.ErrorIs(t, err, ErrPoolFull)

	stats := pool.Stats()
	require.Equal(t, uint64(2), stats.Admitted)
	require.Equal(t, uint64(1), stats.Rejected["already_known"])
	require.Equal(t, uint64(1), stats.Rejected["chain_id"])
	require.Equal(t, uint64(42000), stats.GasSum)
}

func TestReplacementRequiresFeeBump(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)
	acc := newAccount(t)

	first, raw := signTx(t, acc, txOpts{nonce: 0, feeCap: 100, tip: 10})
	_, err := pool.Add(raw)
	require.NoError(t, err)

	_, underpriced := signTx(t, acc, txOpts{nonce: 0, feeCap: 105, tip: 11})
	_, err = pool.Add(underpriced)
	require.ErrorIs(t, err, ErrReplaceUnderpriced)

	replacement, bumped := signTx(t, acc, txOpts{nonce: 0, feeCap: 110, tip: 11})
	_, err = pool.Add(bumped)
	require.NoError(t, err)

	require.Nil(t, pool.Get(first.Hash()))
	require.NotNil(t, pool.Get(replacement.Hash()))
	require.Equal(t, 1, pool.Stats().Pending)
	require.Equal(t, uint64(1), pool.Stats().Replaced)
}

func TestPendingNonce(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)
	acc := newAccount(t)

	for _, nonce := range []uint64{5, 6, 8} {
		_, raw := signTx(t, acc, txOpts{nonce: nonce})
		_, err := pool.Add(raw)
		require.NoError(t, err)
	}

	require.Equal(t, uint64(7), pool.PendingNonce(acc.addr, 5), "contiguous run 5,6 advances to 7; 8 is a gap")
	require.Equal(t, uint64(3), pool.PendingNonce(acc.addr, 3), "chain nonce below the queue stays")
	require.Equal(t, uint64(9), pool.PendingNonce(acc.addr, 8))
	require.Equal(t, uint64(1), pool.PendingNonce(common.Address{0x1}, 1))
}

func TestSelectHonoursStateAndBudgets(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	rich := newAccount(t)
	poor := newAccount(t)
	gapped := newAccount(t)
	stale := newAccount(t)

	fund(el, rich, 0, 100)
	fund(el, poor, 0, 0) // cannot pay for gas
	fund(el, gapped, 0, 100)
	fund(el, stale, 5, 100) // chain nonce already past the queued tx

	var expectedOrder []common.Hash

	for nonce := uint64(0); nonce < 3; nonce++ {
		tx, raw := signTx(t, rich, txOpts{nonce: nonce})
		_, err := pool.Add(raw)
		require.NoError(t, err)

		expectedOrder = append(expectedOrder, tx.Hash())
	}

	_, raw := signTx(t, poor, txOpts{nonce: 0})
	_, err := pool.Add(raw)
	require.NoError(t, err)

	_, raw = signTx(t, gapped, txOpts{nonce: 1}) // nonce 0 missing
	_, err = pool.Add(raw)
	require.NoError(t, err)

	staleTx, raw := signTx(t, stale, txOpts{nonce: 2})
	_, err = pool.Add(raw)
	require.NoError(t, err)

	// A second stale transaction the EL reports as included in a block we
	// built: it counts as included-by-us, not as nonce-too-low.
	ourBlock := common.Hash{0xbe}
	pool.NoteBuiltBlock(ourBlock, 9)

	staleOurs, raw := signTx(t, stale, txOpts{nonce: 3})
	_, err = pool.Add(raw)
	require.NoError(t, err)

	el.txBlocks[staleOurs.Hash()] = ourBlock

	// Fee floor: parent at exactly the gas target keeps the base fee at 1 gwei;
	// a 0.5 gwei fee cap must be skipped.
	_, raw = signTx(t, rich, txOpts{nonce: 3, feeCap: 500_000_000, tip: 1})
	_, err = pool.Add(raw)
	require.NoError(t, err)

	sel, err := pool.Select(context.Background(), &SelectParams{
		ParentHash: common.Hash{0xaa},
		Ordering:   config.TxOrderingFIFO,
	})
	require.NoError(t, err)

	require.Len(t, sel.Selected, 3)
	for i, tx := range sel.Selected {
		require.Equal(t, expectedOrder[i], tx.Hash)
	}

	require.Len(t, sel.Txs, 3)
	require.Equal(t, uint64(63000), sel.GasSum)
	require.Equal(t, 1, sel.Skipped[SkipInsufficientBalance])
	require.Equal(t, 1, sel.Skipped[SkipNonceGap])
	require.Equal(t, 2, sel.Skipped[SkipNonceTooLow])
	require.Equal(t, 1, sel.Skipped[SkipFeeBelowBase])
	require.Equal(t, "1000000000", sel.BaseFee.String())
	require.Equal(t, uint64(30_000_000), sel.GasBudget)

	// The stale transactions are evicted by the selection pass, classified by
	// the block that included them.
	require.Nil(t, pool.Get(staleTx.Hash()))
	require.Nil(t, pool.Get(staleOurs.Hash()))
	require.Equal(t, uint64(1), pool.Stats().EvictedNonceTooLow)
	require.Equal(t, uint64(1), pool.Stats().EvictedIncludedByUs)
}

func TestSelectGasAndCountBudgets(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	a := newAccount(t)
	b := newAccount(t)
	fund(el, a, 0, 100)
	fund(el, b, 0, 100)

	for nonce := uint64(0); nonce < 4; nonce++ {
		_, raw := signTx(t, a, txOpts{nonce: nonce, gas: 100_000})
		_, err := pool.Add(raw)
		require.NoError(t, err)
	}

	_, raw := signTx(t, b, txOpts{nonce: 0, gas: 50_000})
	_, err := pool.Add(raw)
	require.NoError(t, err)

	// 50% of 500k = 250k budget: a0 (100k), a1 (100k) fit; a2 does not, so
	// a's remaining two are gas_full; b0 (50k) still fits after a is dropped.
	el.header.GasLimit = 500_000
	el.header.GasUsed = 250_000

	sel, err := pool.Select(context.Background(), &SelectParams{
		ParentHash: common.Hash{0xaa},
		GasFillPct: 50,
	})
	require.NoError(t, err)
	require.Len(t, sel.Selected, 3)
	require.Equal(t, uint64(250_000), sel.GasSum)
	require.Equal(t, 2, sel.Skipped[SkipGasFull])

	// Count cap.
	el.header.GasLimit = 30_000_000
	el.header.GasUsed = 15_000_000

	sel, err = pool.Select(context.Background(), &SelectParams{
		ParentHash: common.Hash{0xaa},
		MaxTxs:     2,
	})
	require.NoError(t, err)
	require.Len(t, sel.Selected, 2)
	require.Equal(t, 3, sel.Skipped[SkipMaxTxs])
}

func TestSelectTipOrderingAndInclusionList(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	low := newAccount(t)
	high := newAccount(t)
	fund(el, low, 0, 100)
	fund(el, high, 0, 100)

	lowTx, raw := signTx(t, low, txOpts{nonce: 0, tip: 1_000, feeCap: 5_000_000_000})
	_, err := pool.Add(raw)
	require.NoError(t, err)

	highTx, raw := signTx(t, high, txOpts{nonce: 0, tip: 2_000_000_000, feeCap: 5_000_000_000})
	_, err = pool.Add(raw)
	require.NoError(t, err)

	il := [][]byte{{0x01, 0x02}}

	sel, err := pool.Select(context.Background(), &SelectParams{
		ParentHash:    common.Hash{0xaa},
		Ordering:      config.TxOrderingTip,
		InclusionList: il,
	})
	require.NoError(t, err)
	require.Len(t, sel.Selected, 2)
	require.Equal(t, highTx.Hash(), sel.Selected[0].Hash, "highest effective tip first")
	require.Equal(t, lowTx.Hash(), sel.Selected[1].Hash)
	require.Len(t, sel.Txs, 2, "an undecodable list transaction cannot go in")
	require.Zero(t, sel.InclusionListTxs)
	require.Equal(t, 1, sel.InclusionListSkipped[SkipUndecodable])
}

func TestSelectDisabledPool(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)
	acc := newAccount(t)
	fund(el, acc, 0, 100)

	_, raw := signTx(t, acc, txOpts{nonce: 0})
	_, err := pool.Add(raw)
	require.NoError(t, err)

	pool.SetEnabled(false)

	sel, err := pool.Select(context.Background(), &SelectParams{ParentHash: common.Hash{0xaa}})
	require.NoError(t, err)
	require.Empty(t, sel.Selected)
	require.NotNil(t, sel.Txs, "an empty selection must encode as [] (empty block), never null")
	require.Equal(t, 1, sel.Skipped[SkipDisabled])
}

func TestEvictIncludedAndTTL(t *testing.T) {
	el := newFakeEL()
	pool, cfg, chainSvc := newTestPool(t, el)
	pool.ctx = context.Background()

	acc := newAccount(t)
	fund(el, acc, 0, 100)

	tx0, raw := signTx(t, acc, txOpts{nonce: 0})
	_, err := pool.Add(raw)
	require.NoError(t, err)

	tx1, raw := signTx(t, acc, txOpts{nonce: 1})
	_, err = pool.Add(raw)
	require.NoError(t, err)

	// Block by us includes tx0; the chain nonce moves to 1.
	block := common.Hash{0xbb}
	el.blocks[block] = &execution.BlockTxList{Number: 101, TxHashes: []common.Hash{tx0.Hash()}}
	el.states[acc.addr].Nonce = 1

	pool.NoteBuiltBlock(block, 11)

	require.False(t, pool.evictIncluded(common.Hash{0xcc}), "unknown block stays pending")
	require.True(t, pool.evictIncluded(block))
	require.Nil(t, pool.Get(tx0.Hash()))
	require.NotNil(t, pool.Get(tx1.Hash()))

	stats := pool.Stats()
	require.Equal(t, uint64(1), stats.EvictedIncludedByUs)
	require.Equal(t, uint64(0), stats.EvictedIncludedByOther)

	// A block by someone else that confirms tx1 (and thus passes its nonce).
	other := common.Hash{0xdd}
	el.blocks[other] = &execution.BlockTxList{Number: 102, TxHashes: []common.Hash{tx1.Hash()}}
	el.states[acc.addr].Nonce = 2

	require.True(t, pool.evictIncluded(other))
	require.Equal(t, uint64(1), pool.Stats().EvictedIncludedByOther)
	require.Equal(t, 0, pool.Stats().Pending)

	// TTL sweep.
	cfg.TxPool.TxTTLSlots = 4
	chainSvc.slot = 20

	_, raw = signTx(t, acc, txOpts{nonce: 2})
	_, err = pool.Add(raw) // arrives at slot 20
	require.NoError(t, err)

	pool.sweepTTL(24)
	require.Equal(t, 1, pool.Stats().Pending, "not expired yet")

	pool.sweepTTL(25)
	require.Equal(t, 0, pool.Stats().Pending)
	require.Equal(t, uint64(1), pool.Stats().EvictedTTL)
}

func TestForwardToEL(t *testing.T) {
	el := newFakeEL()
	pool, cfg, _ := newTestPool(t, el)
	pool.ctx = context.Background()
	cfg.TxPool.ForwardToEL = true

	acc := newAccount(t)
	tx, raw := signTx(t, acc, txOpts{nonce: 0})

	_, err := pool.Add(raw)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		sent := el.sentHashes()

		return len(sent) == 1 && sent[0] == tx.Hash()
	}, 2e9, 1e7)
}

func TestListAndClear(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	a := newAccount(t)
	b := newAccount(t)

	for _, item := range []struct {
		acc   account
		nonce uint64
	}{{a, 0}, {b, 0}, {a, 1}} {
		_, raw := signTx(t, item.acc, txOpts{nonce: item.nonce})
		_, err := pool.Add(raw)
		require.NoError(t, err)
	}

	all, total := pool.List(ListOptions{})
	require.Equal(t, 3, total)
	require.Len(t, all, 3)

	onlyA, total := pool.List(ListOptions{Sender: &a.addr, Sort: "nonce"})
	require.Equal(t, 2, total)
	require.Equal(t, uint64(0), onlyA[0].Tx.Nonce())
	require.Equal(t, uint64(1), onlyA[1].Tx.Nonce())

	page, total := pool.List(ListOptions{Offset: 2, Limit: 5})
	require.Equal(t, 3, total)
	require.Len(t, page, 1)

	require.Equal(t, 3, pool.Clear())
	require.Equal(t, 0, pool.Stats().Pending)
}

func TestSelectQueuedIsExactOrFails(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	a := newAccount(t)
	b := newAccount(t)
	fund(el, a, 0, 100)
	fund(el, b, 0, 100)

	a0, raw := signTx(t, a, txOpts{nonce: 0})
	_, err := pool.Add(raw)
	require.NoError(t, err)

	a1, raw := signTx(t, a, txOpts{nonce: 1})
	_, err = pool.Add(raw)
	require.NoError(t, err)

	b0, raw := signTx(t, b, txOpts{nonce: 0})
	_, err = pool.Add(raw)
	require.NoError(t, err)

	params := &SelectParams{ParentHash: common.Hash{0xaa}}

	// Interleaved order across senders, nonce order within a sender.
	sel, err := pool.SelectQueued(context.Background(), []common.Hash{a0.Hash(), b0.Hash(), a1.Hash()}, params)
	require.NoError(t, err)
	require.Len(t, sel.Txs, 3)
	require.Equal(t, []common.Hash{a0.Hash(), b0.Hash(), a1.Hash()},
		[]common.Hash{sel.Selected[0].Hash, sel.Selected[1].Hash, sel.Selected[2].Hash})
	require.Equal(t, uint64(63000), sel.GasSum)

	// Nonce order violated: a1 before a0.
	_, err = pool.SelectQueued(context.Background(), []common.Hash{a1.Hash(), a0.Hash()}, params)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expects 0 at this position")

	// Unknown hash.
	_, err = pool.SelectQueued(context.Background(), []common.Hash{{0xff}}, params)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not queued")

	// Gas limit: the exact list must fit the whole block, the fill percentage
	// does not apply.
	el.header.GasLimit = 30_000
	el.header.GasUsed = 15_000
	_, err = pool.SelectQueued(context.Background(), []common.Hash{a0.Hash(), b0.Hash()}, params)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not fit the gas limit")

	// Stale nonce (chain moved on) is a deviation too, never trimmed.
	el.header.GasLimit = 30_000_000
	el.header.GasUsed = 15_000_000
	el.states[a.addr].Nonce = 1
	_, err = pool.SelectQueued(context.Background(), []common.Hash{a0.Hash()}, params)
	require.Error(t, err)

	// A disabled pool refuses.
	pool.SetEnabled(false)
	_, err = pool.SelectQueued(context.Background(), []common.Hash{b0.Hash()}, params)
	require.ErrorIs(t, err, ErrDisabled)
}

func TestSelectRoundRobinAndCeiling(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	a := newAccount(t)
	b := newAccount(t)
	fund(el, a, 0, 100)
	fund(el, b, 0, 100)

	var order []common.Hash

	// a0, a1, a2 arrive first, then b0, b1.
	for _, item := range []struct {
		acc   account
		nonce uint64
	}{{a, 0}, {a, 1}, {a, 2}, {b, 0}, {b, 1}} {
		tx, raw := signTx(t, item.acc, txOpts{nonce: item.nonce})
		_, err := pool.Add(raw)
		require.NoError(t, err)

		order = append(order, tx.Hash())
	}

	sel, err := pool.Select(context.Background(), &SelectParams{
		ParentHash: common.Hash{0xaa},
		Ordering:   config.TxOrderingRoundRobin,
	})
	require.NoError(t, err)
	require.Len(t, sel.Selected, 5)

	got := make([]common.Hash, 0, 5)
	for _, tx := range sel.Selected {
		got = append(got, tx.Hash)
	}

	// a0 b0 a1 b1 a2
	require.Equal(t, []common.Hash{order[0], order[3], order[1], order[4], order[2]}, got)
	require.Positive(t, sel.Bytes)

	// Ceiling: the parent's base fee is 1 gwei and it sits at the gas target,
	// so the next base fee is 1 gwei; a 0.5 gwei ceiling halves the fill.
	sel, err = pool.Select(context.Background(), &SelectParams{
		ParentHash:     common.Hash{0xaa},
		BaseFeeCeiling: big.NewInt(500_000_000),
	})
	require.NoError(t, err)
	require.True(t, sel.CeilingApplied)
	require.Equal(t, uint64(15_000_000), sel.GasBudget)

	sel, err = pool.Select(context.Background(), &SelectParams{
		ParentHash:     common.Hash{0xaa},
		BaseFeeCeiling: big.NewInt(2_000_000_000),
	})
	require.NoError(t, err)
	require.False(t, sel.CeilingApplied)
	require.Equal(t, uint64(30_000_000), sel.GasBudget)
}

func TestSweepTTLDropsSenderSuffix(t *testing.T) {
	el := newFakeEL()
	pool, cfg, chainSvc := newTestPool(t, el)
	pool.ctx = context.Background()
	cfg.TxPool.TxTTLSlots = 4

	// stalled: its lowest nonce is old, so the whole chain goes.
	stalled := newAccount(t)
	fund(el, stalled, 0, 100)
	// active: an old higher nonce behind a fresh lower one; only the suffix
	// from the expired nonce goes, the executable lower nonce stays.
	active := newAccount(t)
	fund(el, active, 0, 100)
	// fresh: nothing expired.
	fresh := newAccount(t)
	fund(el, fresh, 0, 100)

	chainSvc.slot = 10

	stalled0, raw := signTx(t, stalled, txOpts{nonce: 0})
	_, err := pool.Add(raw)
	require.NoError(t, err)

	active1, raw := signTx(t, active, txOpts{nonce: 1}) // arrives before nonce 0
	_, err = pool.Add(raw)
	require.NoError(t, err)

	chainSvc.slot = 20

	stalled1, raw := signTx(t, stalled, txOpts{nonce: 1})
	_, err = pool.Add(raw)
	require.NoError(t, err)

	active0, raw := signTx(t, active, txOpts{nonce: 0})
	_, err = pool.Add(raw)
	require.NoError(t, err)

	fresh0, raw := signTx(t, fresh, txOpts{nonce: 0})
	_, err = pool.Add(raw)
	require.NoError(t, err)

	pool.sweepTTL(14)
	require.Equal(t, 5, pool.Stats().Pending, "slot 10 arrivals are not expired before the cutoff passes them")

	pool.sweepTTL(15)

	require.Nil(t, pool.Get(stalled0.Hash()), "expired head")
	require.Nil(t, pool.Get(stalled1.Hash()), "fresh nonce above an expired one goes with it")
	require.Nil(t, pool.Get(active1.Hash()), "expired higher nonce")
	require.NotNil(t, pool.Get(active0.Hash()), "fresh lower nonce stays executable")
	require.NotNil(t, pool.Get(fresh0.Hash()))
	require.Equal(t, uint64(3), pool.Stats().EvictedTTL)
	require.Equal(t, 2, pool.Stats().Pending)
	require.Equal(t, uint64(1), pool.PendingNonce(active.addr, 0), "the sender restarts at the surviving run")
}

// TestStaleEvictionWaitsForClassification keeps stale transactions while the
// EL cannot say which block included them, instead of counting them all as
// replaced (nonce too low), and evicts them on the next successful lookup.
func TestStaleEvictionWaitsForClassification(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	stale := newAccount(t)
	fund(el, stale, 5, 100) // chain nonce already past the queued tx

	staleTx, raw := signTx(t, stale, txOpts{nonce: 2})
	_, err := pool.Add(raw)
	require.NoError(t, err)

	el.lookupErr = errors.New("el unavailable")

	params := &SelectParams{ParentHash: common.Hash{0xaa}, Ordering: config.TxOrderingFIFO}

	sel, err := pool.Select(context.Background(), params)
	require.NoError(t, err)
	require.Empty(t, sel.Selected, "a stale transaction is never selected")
	require.NotNil(t, pool.Get(staleTx.Hash()), "kept until it can be classified")
	require.Zero(t, pool.Stats().EvictedNonceTooLow)

	el.lookupErr = nil

	_, err = pool.Select(context.Background(), params)
	require.NoError(t, err)
	require.Nil(t, pool.Get(staleTx.Hash()))
	require.Equal(t, uint64(1), pool.Stats().EvictedNonceTooLow)
}

// TestSelectFillsFromELMempool places EL mempool transactions after the pool
// selection: a sender continues at the nonce the pool selection left, the
// highest tip goes first, pool duplicates and nonce gaps are skipped, and the
// fill stays within the gas the pool selection left.
func TestSelectFillsFromELMempool(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	shared := newAccount(t) // in both pools
	cheap := newAccount(t)
	rich := newAccount(t)
	gapped := newAccount(t)

	fund(el, shared, 0, 100)
	fund(el, cheap, 0, 100)
	fund(el, rich, 0, 100)
	fund(el, gapped, 0, 100)

	poolTx, raw := signTx(t, shared, txOpts{nonce: 0})
	_, err := pool.Add(raw)
	require.NoError(t, err)

	sharedNext, _ := signTx(t, shared, txOpts{nonce: 1, tip: 2})
	cheapTx, _ := signTx(t, cheap, txOpts{nonce: 0, tip: 1})
	richTx, _ := signTx(t, rich, txOpts{nonce: 0}) // 1 gwei tip
	gappedTx, _ := signTx(t, gapped, txOpts{nonce: 3})

	sel, err := pool.Select(context.Background(), &SelectParams{
		ParentHash: common.Hash{0xaa},
		GasLimit:   3 * 21000, // the pool transaction + two fill transactions
		Ordering:   config.TxOrderingFIFO,
		Fill:       fillOf(t, poolTx, sharedNext, cheapTx, richTx, gappedTx),
	})
	require.NoError(t, err)

	require.Len(t, sel.Selected, 1)
	require.Equal(t, poolTx.Hash(), sel.Selected[0].Hash)

	require.Len(t, sel.Fill, 2)
	require.Equal(t, richTx.Hash(), sel.Fill[0].Hash, "highest tip first")
	require.Equal(t, sharedNext.Hash(), sel.Fill[1].Hash, "continues the sender after the pool's nonce")

	require.Len(t, sel.Txs, 3)
	require.Equal(t, 5, sel.FillCandidates)
	require.Equal(t, 1, sel.FillSkipped[SkipDuplicate])
	require.Equal(t, 1, sel.FillSkipped[SkipNonceGap])
	require.Equal(t, 1, sel.FillSkipped[SkipGasFull])
	require.Equal(t, uint64(42000), sel.FillGasSum)
	require.Equal(t, 2, sel.Summary().FillSelected)
	require.NotNil(t, pool.Get(poolTx.Hash()), "fill accounting never evicts pool transactions")
}

// fillOf wraps EL mempool transactions as the mirror hands them to the
// selection: senders recovered, in the given order.
func fillOf(t *testing.T, txs ...*types.Transaction) []*PooledTx {
	t.Helper()

	signer := types.LatestSignerForChainID(testChainID)
	out := make([]*PooledTx, 0, len(txs))

	for i, tx := range txs {
		sender, err := types.Sender(signer, tx)
		require.NoError(t, err)

		out = append(out, &PooledTx{Hash: tx.Hash(), Tx: tx, Sender: sender, Size: tx.Size(), Seq: uint64(i)})
	}

	return out
}

// TestSelectMostProfitable merges the private pool and the EL mempool into
// one selection by effective tip: a sender's chain continues across the
// pools in nonce order, of two transactions at one nonce the higher tip
// wins, and the origins stay accounted apart.
func TestSelectMostProfitable(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	const gwei = 1_000_000_000

	low, high, mid, top, cross, conflict := newAccount(t), newAccount(t), newAccount(t), newAccount(t), newAccount(t), newAccount(t)
	for _, acc := range []account{low, high, mid, top, cross, conflict} {
		fund(el, acc, 0, 100)
	}

	opts := func(nonce uint64, tip int64) txOpts { return txOpts{nonce: nonce, tip: tip * gwei, feeCap: 100 * gwei} }

	addPrivate := func(acc account, o txOpts) *types.Transaction {
		tx, raw := signTx(t, acc, o)
		_, err := pool.Add(raw)
		require.NoError(t, err)

		return tx
	}

	privLow := addPrivate(low, opts(0, 1))
	privHigh := addPrivate(high, opts(0, 5))
	privCross := addPrivate(cross, opts(1, 8))       // after the public nonce 0
	privConflict := addPrivate(conflict, opts(0, 2)) // the public one pays more

	pubMid, _ := signTx(t, mid, opts(0, 3))
	pubTop, _ := signTx(t, top, opts(0, 9))
	pubCross, _ := signTx(t, cross, opts(0, 4))
	pubConflict, _ := signTx(t, conflict, opts(0, 6))

	sel, err := pool.Select(context.Background(), &SelectParams{
		ParentHash:     common.Hash{0xaa},
		GasLimit:       6 * 21000, // the lowest tip does not fit
		Ordering:       config.TxOrderingFIFO,
		Fill:           fillOf(t, pubMid, pubTop, pubCross, pubConflict),
		MostProfitable: true,
	})
	require.NoError(t, err)

	order := make([]common.Hash, 0, len(sel.Ordered))
	for _, tx := range sel.Ordered {
		order = append(order, tx.Hash)
	}

	require.Equal(t, []common.Hash{
		pubTop.Hash(),      // 9
		pubConflict.Hash(), // 6, wins its nonce over the private 2
		privHigh.Hash(),    // 5
		pubCross.Hash(),    // 4, then its sender's private nonce 1 is the head
		privCross.Hash(),   // 8
		pubMid.Hash(),      // 3
	}, order)
	require.Len(t, sel.Txs, 6)

	require.Equal(t, []*PooledTx{pool.Get(privHigh.Hash()), pool.Get(privCross.Hash())}, sel.Selected)
	require.Len(t, sel.Fill, 4)
	require.Equal(t, 1, sel.Skipped[SkipNonceTaken], "the private transaction at the public one's nonce")
	require.Equal(t, 1, sel.Skipped[SkipGasFull])
	require.NotNil(t, pool.Get(privLow.Hash()))
	require.NotNil(t, pool.Get(privConflict.Hash()), "a taken nonce never evicts")

	require.Equal(t, uint64(2*21000), sel.GasSum, "the pool's share")
	require.Equal(t, uint64(4*21000), sel.FillGasSum)

	origins := sel.Summary().Origins
	require.Equal(t, 2, origins.Private.Txs)
	require.Equal(t, 4, origins.Public.Txs)
	require.Equal(t, uint64(4*21000), origins.Public.Gas)
	require.Equal(t, new(big.Int).Mul(big.NewInt((9+6+4+3)*gwei), big.NewInt(21000)).String(), origins.Public.Tips)
}

// TestSelectAfterPrecedingBlock estimates the block after one that is not
// on chain yet: its transactions are left out, their senders continue after
// them, the base fee override applies and nothing is evicted.
func TestSelectAfterPrecedingBlock(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	acc := newAccount(t)
	fund(el, acc, 0, 100)

	first, raw0 := signTx(t, acc, txOpts{nonce: 0})
	_, err := pool.Add(raw0)
	require.NoError(t, err)

	second, raw1 := signTx(t, acc, txOpts{nonce: 1})
	_, err = pool.Add(raw1)
	require.NoError(t, err)

	baseFee := big.NewInt(7)

	sel, err := pool.Select(context.Background(), &SelectParams{
		ParentHash: common.Hash{0xaa},
		Ordering:   config.TxOrderingFIFO,
		Preceding:  []*PooledTx{{Hash: first.Hash(), Tx: first, Sender: acc.addr}},
		BaseFee:    baseFee,
	})
	require.NoError(t, err)

	require.Len(t, sel.Selected, 1)
	require.Equal(t, second.Hash(), sel.Selected[0].Hash, "continues after the preceding block's nonce")
	require.Equal(t, 1, sel.Skipped[SkipInPrecedingBlock])
	require.Equal(t, 0, baseFee.Cmp(sel.BaseFee))
	require.NotNil(t, pool.Get(first.Hash()), "a speculative selection evicts nothing")
}
