// Package txpool is buildoor's owned transaction pool: transactions submitted
// through the JSON-RPC ingress are queued here — never in the EL mempool — and
// only reach the chain inside a payload the local build assembles from the pool.
package txpool

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/metrics"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
)

// Admission errors. Their messages are part of the ingress contract: spamoor
// treats "already known" as a successful submission.
var (
	// ErrDisabled rejects submissions while the pool is switched off.
	ErrDisabled = errors.New("txpool disabled")
	// ErrNotStarted rejects submissions before the chain id is known.
	ErrNotStarted = errors.New("txpool not started")
	// ErrAlreadyKnown marks a duplicate submission (same hash).
	ErrAlreadyKnown = errors.New("already known")
	// ErrReplaceUnderpriced rejects a same-nonce replacement without the fee bump.
	ErrReplaceUnderpriced = errors.New("replacement transaction underpriced")
	// ErrPoolFull rejects submissions beyond the pool cap.
	ErrPoolFull = errors.New("txpool is full")
	// ErrSenderFull rejects submissions beyond the per-sender cap.
	ErrSenderFull = errors.New("too many pending transactions for sender")
	// ErrChainID rejects transactions signed for another chain.
	ErrChainID = errors.New("invalid chain id")
	// ErrBlobNoSidecar rejects canonical-form blob transactions: without the
	// sidecar the blobs bundle cannot be produced.
	ErrBlobNoSidecar = errors.New("blob transaction without sidecar")
	// ErrOversized rejects transactions above the canonical size cap.
	ErrOversized = errors.New("oversized transaction")
	// ErrIntrinsicGas rejects transactions below the minimum gas.
	ErrIntrinsicGas = errors.New("intrinsic gas too low")
)

// maxTxSize is the canonical (sidecar-less) encoding size cap, matching the
// EL pools' 128 KiB limit.
const maxTxSize = 128 * 1024

// minTxGas is the intrinsic gas of the cheapest transaction.
const minTxGas = 21000

// replacementBumpPercent is the minimum fee bump a same-nonce replacement must
// carry on both the fee cap and the tip (geth's default price bump).
const replacementBumpPercent = 10

// builtBlockRetentionSlots bounds how long built block hashes are remembered
// for the included-by-us classification.
const builtBlockRetentionSlots = 64

// ELClient is the execution-layer surface the pool depends on (satisfied by
// *execution.Client; an interface so tests can substitute a fake).
type ELClient interface {
	GetChainID(ctx context.Context) (*big.Int, error)
	HeaderByHash(ctx context.Context, hash common.Hash) (*types.Header, error)
	AccountStatesAt(ctx context.Context, addrs []common.Address, blockHash common.Hash,
		blockNumber uint64) (map[common.Address]*execution.AccountState, error)
	BlobBaseFee(ctx context.Context) (*big.Int, error)
	BlockTransactions(ctx context.Context, blockHash common.Hash) (*execution.BlockTxList, bool, error)
	TransactionBlockHashes(ctx context.Context, hashes []common.Hash) (map[common.Hash]common.Hash, error)
	SendRawTransaction(ctx context.Context, raw []byte) (common.Hash, error)
}

var _ ELClient = (*execution.Client)(nil)

// PooledTx is one queued transaction with its admission metadata.
type PooledTx struct {
	Hash        common.Hash
	Sender      common.Address
	Tx          *types.Transaction // sidecar retained for blob transactions
	Size        uint64             // encoded size as received
	Arrived     time.Time
	ArrivedSlot phase0.Slot
	Seq         uint64 // arrival order (fifo ordering)

	// strikes counts attributed build failures. Written under the pool
	// mutex, read by snapshot holders, hence atomic.
	strikes atomic.Int32
}

// Strikes returns the transaction's attributed build failures.
func (t *PooledTx) Strikes() int { return int(t.strikes.Load()) }

// senderQueue is one sender's queued transactions in ascending nonce order.
type senderQueue struct {
	txs []*PooledTx
}

func (q *senderQueue) find(nonce uint64) (int, bool) {
	i := sort.Search(len(q.txs), func(i int) bool { return q.txs[i].Tx.Nonce() >= nonce })

	return i, i < len(q.txs) && q.txs[i].Tx.Nonce() == nonce
}

func (q *senderQueue) insert(tx *PooledTx) {
	i, _ := q.find(tx.Tx.Nonce())
	q.txs = append(q.txs, nil)
	copy(q.txs[i+1:], q.txs[i:])
	q.txs[i] = tx
}

func (q *senderQueue) remove(nonce uint64) *PooledTx {
	i, ok := q.find(nonce)
	if !ok {
		return nil
	}

	tx := q.txs[i]
	q.txs = append(q.txs[:i], q.txs[i+1:]...)

	return tx
}

// Pool is the owned transaction pool.
type Pool struct {
	log      logrus.FieldLogger
	cfg      *config.Config // shared config; mutable settings are read live, never cached
	elClient ELClient
	chainSvc chain.Service
	clClient *beacon.Client

	enabled atomic.Bool
	version atomic.Uint64 // bumped on every content change (UI refresh hint)

	mu       sync.RWMutex
	signer   types.Signer
	chainID  *big.Int
	byHash   map[common.Hash]*PooledTx
	bySender map[common.Address]*senderQueue
	seq      uint64
	stats    counters

	builtMu     sync.Mutex
	builtBlocks map[common.Hash]phase0.Slot // block hashes of payloads we built

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPool creates the pool. It does not touch the EL; Start resolves the chain
// id and begins the eviction loop.
func NewPool(
	cfg *config.Config,
	elClient ELClient,
	chainSvc chain.Service,
	clClient *beacon.Client,
	log logrus.FieldLogger,
) *Pool {
	p := &Pool{
		log:         log.WithField("component", "txpool"),
		cfg:         cfg,
		elClient:    elClient,
		chainSvc:    chainSvc,
		clClient:    clClient,
		byHash:      make(map[common.Hash]*PooledTx, 1024),
		bySender:    make(map[common.Address]*senderQueue, 64),
		builtBlocks: make(map[common.Hash]phase0.Slot, builtBlockRetentionSlots),
		stats:       counters{Rejected: make(map[string]uint64, 8)},
	}

	p.enabled.Store(cfg.TxPool.Enabled)

	return p
}

// Start resolves the chain id (retrying until the EL answers) and starts the
// eviction loop.
func (p *Pool) Start(ctx context.Context) error {
	p.ctx, p.cancel = context.WithCancel(ctx)

	chainID, err := p.resolveChainID(p.ctx)
	if err != nil {
		p.cancel()

		return err
	}

	p.mu.Lock()
	p.chainID = chainID
	p.signer = types.LatestSignerForChainID(chainID)
	p.mu.Unlock()

	// The eviction loop follows beacon head events; without a beacon client
	// (tests, tooling) the pool only admits and selects.
	if p.clClient != nil {
		p.wg.Add(1)

		go p.run()
	}

	p.log.WithFields(logrus.Fields{
		"chain_id": chainID.String(),
		"enabled":  p.enabled.Load(),
	}).Info("Transaction pool started")

	return nil
}

// resolveChainID asks the EL for its chain id, retrying on transport errors
// until the context ends.
func (p *Pool) resolveChainID(ctx context.Context) (*big.Int, error) {
	for {
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		chainID, err := p.elClient.GetChainID(callCtx)

		cancel()

		if err == nil {
			return chainID, nil
		}

		p.log.WithError(err).Warn("EL chain id unavailable, retrying in 5s")

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("resolve chain id: %w", ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
}

// Stop ends the eviction loop.
func (p *Pool) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}

	p.wg.Wait()

	return nil
}

// SetEnabled switches admission and selection on or off. The queued content
// is kept either way.
func (p *Pool) SetEnabled(enabled bool) {
	if p.enabled.Swap(enabled) != enabled {
		p.version.Add(1)
		p.log.WithField("enabled", enabled).Info("Transaction pool toggled")
	}
}

// Enabled reports whether the pool admits and offers transactions.
func (p *Pool) Enabled() bool {
	return p.enabled.Load()
}

// Version is a counter bumped on every content or state change; pollers use
// it to skip unchanged snapshots.
func (p *Pool) Version() uint64 {
	return p.version.Load()
}

// ChainID returns the EL chain id (nil before Start).
func (p *Pool) ChainID() *big.Int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.chainID == nil {
		return nil
	}

	return new(big.Int).Set(p.chainID)
}

// Add admits a raw (network-encoded) transaction. Validation is stateless:
// decoding, chain id, signature, size, sidecar presence, duplicates,
// same-nonce replacement pricing and the pool caps. Nonce and balance are
// checked against the parent state at selection time instead, so generators
// that submit ahead of confirmation are never rejected on stale state.
func (p *Pool) Add(raw []byte) (common.Hash, error) {
	if !p.enabled.Load() {
		return common.Hash{}, ErrDisabled
	}

	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(raw); err != nil {
		p.reject("decode")

		return common.Hash{}, fmt.Errorf("invalid transaction encoding: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.signer == nil {
		return common.Hash{}, ErrNotStarted
	}

	pooled, err := p.validateLocked(tx, uint64(len(raw)))
	if err != nil {
		return tx.Hash(), err
	}

	if _, ok := p.byHash[pooled.Hash]; ok {
		p.rejectLocked("already_known")

		return pooled.Hash, ErrAlreadyKnown
	}

	queue := p.bySender[pooled.Sender]
	if queue == nil {
		queue = &senderQueue{txs: make([]*PooledTx, 0, 8)}
	}

	if _, ok := queue.find(tx.Nonce()); ok {
		if err := p.replaceLocked(queue, pooled); err != nil {
			return pooled.Hash, err
		}
	} else {
		if uint64(len(p.byHash)) >= max(p.cfg.TxPool.MaxPoolTxs, 1) {
			p.rejectLocked("pool_full")

			return pooled.Hash, ErrPoolFull
		}

		if uint64(len(queue.txs)) >= max(p.cfg.TxPool.MaxTxsPerSender, 1) {
			p.rejectLocked("sender_full")

			return pooled.Hash, ErrSenderFull
		}

		queue.insert(pooled)
		p.bySender[pooled.Sender] = queue
	}

	p.byHash[pooled.Hash] = pooled
	p.stats.Admitted++
	p.stats.LastAdmittedAt = pooled.Arrived
	p.version.Add(1)
	metrics.TxPoolAdmitted.Inc()
	metrics.TxPoolPending.Set(float64(len(p.byHash)))

	if p.cfg.TxPool.ForwardToEL {
		go p.forwardToEL(raw, pooled.Hash)
	}

	return pooled.Hash, nil
}

// validateLocked runs the stateless admission checks and wraps the
// transaction. Must hold mu (for the signer).
func (p *Pool) validateLocked(tx *types.Transaction, size uint64) (*PooledTx, error) {
	if tx.Protected() && tx.ChainId().Cmp(p.chainID) != 0 {
		p.rejectLocked("chain_id")

		return nil, fmt.Errorf("%w: got %s, want %s", ErrChainID, tx.ChainId(), p.chainID)
	}

	sender, err := types.Sender(p.signer, tx)
	if err != nil {
		p.rejectLocked("signature")

		return nil, fmt.Errorf("invalid transaction signature: %w", err)
	}

	if tx.Type() == types.BlobTxType && tx.BlobTxSidecar() == nil {
		p.rejectLocked("blob_no_sidecar")

		return nil, ErrBlobNoSidecar
	}

	if tx.WithoutBlobTxSidecar().Size() > maxTxSize {
		p.rejectLocked("oversized")

		return nil, ErrOversized
	}

	if tx.Gas() < minTxGas {
		p.rejectLocked("intrinsic_gas")

		return nil, ErrIntrinsicGas
	}

	return &PooledTx{
		Hash:        tx.Hash(),
		Sender:      sender,
		Tx:          tx,
		Size:        size,
		Arrived:     time.Now(),
		ArrivedSlot: p.chainSvc.GetCurrentSlot(),
	}, nil
}

// replaceLocked swaps a same-nonce transaction when the replacement bumps the
// fee cap and the tip (and blob fee cap) by at least replacementBumpPercent.
func (p *Pool) replaceLocked(queue *senderQueue, replacement *PooledTx) error {
	i, _ := queue.find(replacement.Tx.Nonce())
	old := queue.txs[i]

	if !bumpsFee(old.Tx.GasFeeCap(), replacement.Tx.GasFeeCap()) ||
		!bumpsFee(old.Tx.GasTipCap(), replacement.Tx.GasTipCap()) {
		p.rejectLocked("replacement_underpriced")

		return ErrReplaceUnderpriced
	}

	if old.Tx.Type() == types.BlobTxType && replacement.Tx.Type() == types.BlobTxType &&
		!bumpsFee(old.Tx.BlobGasFeeCap(), replacement.Tx.BlobGasFeeCap()) {
		p.rejectLocked("replacement_underpriced")

		return ErrReplaceUnderpriced
	}

	delete(p.byHash, old.Hash)
	queue.txs[i] = replacement
	replacement.Seq = old.Seq // keeps the sender's fifo position
	p.stats.Replaced++

	return nil
}

// bumpsFee reports whether next is at least replacementBumpPercent above prev.
func bumpsFee(prev, next *big.Int) bool {
	if prev == nil {
		return true
	}

	if next == nil {
		return false
	}

	threshold := new(big.Int).Mul(prev, big.NewInt(100+replacementBumpPercent))
	threshold.Div(threshold, big.NewInt(100))

	return next.Cmp(threshold) >= 0
}

func (p *Pool) reject(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.rejectLocked(reason)
}

func (p *Pool) rejectLocked(reason string) {
	p.stats.Rejected[reason]++
	metrics.TxPoolRejected.WithLabelValues(reason).Inc()
}

// evictedLocked accounts one eviction. Must hold mu.
func (p *Pool) evictedLocked(reason string) {
	metrics.TxPoolEvicted.WithLabelValues(reason).Inc()
	metrics.TxPoolPending.Set(float64(len(p.byHash)))
}

// forwardToEL shadow-submits an admitted transaction to the EL mempool.
func (p *Pool) forwardToEL(raw []byte, hash common.Hash) {
	ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
	defer cancel()

	if _, err := p.elClient.SendRawTransaction(ctx, raw); err != nil {
		p.log.WithError(err).WithField("tx", hash.Hex()).Debug("Forwarding transaction to the EL failed")
	}
}

// Get returns the queued transaction with the given hash, or nil.
func (p *Pool) Get(hash common.Hash) *PooledTx {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.byHash[hash]
}

// Remove drops one transaction by hash.
func (p *Pool) Remove(hash common.Hash) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.removeLocked(hash) {
		return false
	}

	p.stats.Removed++
	p.evictedLocked("removed")
	p.version.Add(1)

	return true
}

// removeLocked unlinks a transaction from both indices. Must hold mu.
func (p *Pool) removeLocked(hash common.Hash) bool {
	tx, ok := p.byHash[hash]
	if !ok {
		return false
	}

	delete(p.byHash, hash)

	if queue := p.bySender[tx.Sender]; queue != nil {
		queue.remove(tx.Tx.Nonce())

		if len(queue.txs) == 0 {
			delete(p.bySender, tx.Sender)
		}
	}

	return true
}

// Strike counts an attributed build failure against a transaction and drops
// it once it reaches maxStrikes (0 = never drop). Reports whether it was
// dropped.
func (p *Pool) Strike(hash common.Hash, maxStrikes uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	tx, ok := p.byHash[hash]
	if !ok {
		return false
	}

	strikes := tx.strikes.Add(1)
	if maxStrikes == 0 || uint64(strikes) < maxStrikes {
		return false
	}

	p.removeLocked(hash)
	p.stats.EvictedStrikes++
	p.evictedLocked("strikes")
	p.version.Add(1)

	return true
}

// Clear drops every queued transaction and returns how many were dropped.
func (p *Pool) Clear() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	n := len(p.byHash)
	p.byHash = make(map[common.Hash]*PooledTx, 1024)
	p.bySender = make(map[common.Address]*senderQueue, 64)
	p.stats.Cleared += uint64(n)
	metrics.TxPoolEvicted.WithLabelValues("cleared").Add(float64(n))
	metrics.TxPoolPending.Set(0)
	p.version.Add(1)

	return n
}

// PendingNonce returns the nonce a new transaction of the sender should carry
// given the account's on-chain nonce: the chain nonce advanced over the
// contiguous run of queued transactions (the pool-aware "pending" view).
func (p *Pool) PendingNonce(sender common.Address, chainNonce uint64) uint64 {
	p.mu.RLock()
	defer p.mu.RUnlock()

	queue := p.bySender[sender]
	if queue == nil {
		return chainNonce
	}

	next := chainNonce

	for _, tx := range queue.txs {
		if tx.Tx.Nonce() < next {
			continue
		}

		if tx.Tx.Nonce() != next {
			break
		}

		next++
	}

	return next
}

// NoteBuiltBlock records the block hash of a payload we built, so an included
// block can be classified as ours in the eviction accounting.
func (p *Pool) NoteBuiltBlock(blockHash common.Hash, slot phase0.Slot) {
	p.builtMu.Lock()
	defer p.builtMu.Unlock()

	p.builtBlocks[blockHash] = slot

	if slot > builtBlockRetentionSlots {
		cutoff := slot - builtBlockRetentionSlots
		for hash, builtSlot := range p.builtBlocks {
			if builtSlot < cutoff {
				delete(p.builtBlocks, hash)
			}
		}
	}
}

func (p *Pool) isBuiltBlock(blockHash common.Hash) bool {
	p.builtMu.Lock()
	defer p.builtMu.Unlock()

	_, ok := p.builtBlocks[blockHash]

	return ok
}

// senderSnapshot returns every sender's queue (nonce-ascending copies) for
// selection without holding the lock during EL calls.
func (p *Pool) senderSnapshot() map[common.Address][]*PooledTx {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make(map[common.Address][]*PooledTx, len(p.bySender))
	for sender, queue := range p.bySender {
		txs := make([]*PooledTx, len(queue.txs))
		copy(txs, queue.txs)
		out[sender] = txs
	}

	return out
}
