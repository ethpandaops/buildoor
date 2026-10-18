package txpool

import (
	"context"
	"math/big"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
)

const (
	// mirrorPollInterval is how often the pending transaction filter is
	// polled for new arrivals.
	mirrorPollInterval = 500 * time.Millisecond
	// mirrorResyncInterval bounds how long the mirror runs on the filter
	// alone: a full txpool_content read then drops what the EL evicted
	// without a trace (replaced, underpriced, timed out).
	mirrorResyncInterval = 5 * time.Minute
	// mirrorBlobMaxAge expires blob transactions, which the resync cannot
	// confirm: geth's txpool_content leaves its blob pool out.
	mirrorBlobMaxAge = 2 * mirrorResyncInterval
	// mirrorIdleTimeout stops the mirror once no build asked for it.
	mirrorIdleTimeout = 10 * time.Minute
	// mirrorMaxTxs bounds the mirror's memory.
	mirrorMaxTxs = 100_000
	// mirrorCallTimeout bounds each EL call of the mirror.
	mirrorCallTimeout = 10 * time.Second
	// Reconnect backoff of the websocket stream.
	mirrorStreamMinBackoff = time.Second
	mirrorStreamMaxBackoff = 30 * time.Second
	// mirrorStreamBuffer absorbs arrival bursts while senders are recovered.
	mirrorStreamBuffer = 4096
)

// PendingTxStream streams the transactions entering the EL mempool (see
// execution.PendingTxStream).
type PendingTxStream interface {
	SubscribePendingTransactions(ctx context.Context, ch chan<- *types.Transaction) (execution.PendingTxSubscription, error)
}

// elMirror keeps a decoded copy of the EL mempool's pending transactions so a
// build does not read, decode and sender-recover the whole txpool_content
// every time. Arrivals come live from a websocket subscription when one is
// configured, else (and while it is down) from a polled pending transaction
// filter; both cover blob transactions, which geth's txpool_content leaves
// out. Included blocks, stale nonces and a periodic full resync remove what
// left the EL. It runs only while builds use it.
type elMirror struct {
	pool *Pool
	log  logrus.FieldLogger

	// stream, when set, delivers the arrivals live. streamLive pauses the
	// filter polls; pollOnce asks for one more poll after a (re)subscribe,
	// which catches what arrived before the subscription.
	stream       PendingTxStream
	streamLive   atomic.Bool
	pollOnce     atomic.Bool
	followCancel context.CancelFunc // owned by the run goroutine

	// arrivals counts mirrored transactions that clear baseFeeHint (the
	// latest selection's base fee): the signal that a rebuild could pick up
	// something new. Arrivals below it are the bulk of a busy mempool and
	// would never make a block.
	arrivals    atomic.Uint64
	baseFeeHint atomic.Uint64 // wei; 0 = unknown, every arrival counts

	// syncMu serializes the EL-facing work (activation, polls, resyncs).
	syncMu   sync.Mutex
	filterID string
	syncedAt time.Time

	mu       sync.Mutex
	active   bool
	lastUsed time.Time
	seq      uint64
	byHash   map[common.Hash]*mirrorEntry
	bySender map[common.Address]map[uint64]*mirrorEntry
}

// mirrorEntry is one mirrored transaction.
type mirrorEntry struct {
	tx     *PooledTx
	seenAt time.Time // when it arrived or a resync last confirmed it
}

func newELMirror(pool *Pool) *elMirror {
	return &elMirror{
		pool:     pool,
		log:      pool.log.WithField("subcomponent", "el-mirror"),
		byHash:   make(map[common.Hash]*mirrorEntry, 1024),
		bySender: make(map[common.Address]map[uint64]*mirrorEntry, 1024),
	}
}

// Pending returns the mirrored pending transactions, each sender's in nonce
// order. The first call after an idle period reads the full EL mempool
// (started reports it); later calls are served from memory while the
// background loop keeps it current.
func (m *elMirror) Pending(ctx context.Context) (txs []*PooledTx, started bool, err error) {
	m.mu.Lock()
	m.lastUsed = time.Now()
	active := m.active
	m.mu.Unlock()

	if !active {
		if err := m.activate(ctx); err != nil {
			return nil, false, err
		}

		started = true
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	senders := make([]common.Address, 0, len(m.bySender))
	for sender := range m.bySender {
		senders = append(senders, sender)
	}

	sort.Slice(senders, func(i, j int) bool { return senders[i].Cmp(senders[j]) < 0 })

	out := make([]*PooledTx, 0, len(m.byHash))

	for _, sender := range senders {
		byNonce := m.bySender[sender]

		nonces := make([]uint64, 0, len(byNonce))
		for nonce := range byNonce {
			nonces = append(nonces, nonce)
		}

		sort.Slice(nonces, func(i, j int) bool { return nonces[i] < nonces[j] })

		for _, nonce := range nonces {
			out = append(out, byNonce[nonce].tx)
		}
	}

	return out, started, nil
}

// noteBaseFee records the base fee the latest selection used: arrivals
// below it do not count as news for a rebuild.
func (m *elMirror) noteBaseFee(baseFee *big.Int) {
	if baseFee != nil && baseFee.IsUint64() {
		m.baseFeeHint.Store(baseFee.Uint64())
	}
}

// activate installs the filter and loads the full EL mempool.
func (m *elMirror) activate(ctx context.Context) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	m.mu.Lock()
	active := m.active
	m.mu.Unlock()

	if active {
		return nil
	}

	// The filter goes in first, so nothing arriving during the resync is
	// missed; an EL without filters still gets a full read per resync.
	m.installFilter(ctx)

	if err := m.resync(ctx); err != nil {
		return err
	}

	m.mu.Lock()
	m.active = true
	m.mu.Unlock()

	m.log.WithField("txs", m.size()).Info("EL mempool mirror started")

	return nil
}

// run is the mirror's background loop: polls the filter, resyncs
// periodically and stops the mirror once it went unused.
func (m *elMirror) run(ctx context.Context) {
	ticker := time.NewTicker(mirrorPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.deactivate()

			return
		case <-ticker.C:
		}

		m.mu.Lock()
		active, idle := m.active, time.Since(m.lastUsed) > mirrorIdleTimeout
		m.mu.Unlock()

		if !active {
			continue
		}

		if idle {
			m.deactivate()

			continue
		}

		if m.stream != nil && m.followCancel == nil {
			followCtx, cancel := context.WithCancel(ctx)
			m.followCancel = cancel

			go m.follow(followCtx)
		}

		m.syncMu.Lock()
		if !m.streamLive.Load() || m.pollOnce.Swap(false) {
			m.poll(ctx)
		}

		if time.Since(m.syncedAt) > mirrorResyncInterval {
			if err := m.resync(ctx); err != nil {
				m.log.WithError(err).Warn("EL mempool resync failed")
			}
		}
		m.syncMu.Unlock()
	}
}

// follow keeps the websocket subscription up while the mirror runs,
// reconnecting with backoff; while it is down the filter polls take over.
func (m *elMirror) follow(ctx context.Context) {
	backoff := mirrorStreamMinBackoff

	for ctx.Err() == nil {
		ch := make(chan *types.Transaction, mirrorStreamBuffer)

		sub, err := m.stream.SubscribePendingTransactions(ctx, ch)
		if err != nil {
			m.log.WithError(err).Debug("Cannot subscribe to the EL mempool, polling the filter")

			if !sleepCtx(ctx, backoff) {
				return
			}

			backoff = min(2*backoff, mirrorStreamMaxBackoff)

			continue
		}

		backoff = mirrorStreamMinBackoff
		m.streamLive.Store(true)
		m.pollOnce.Store(true)
		m.log.Info("Following the EL mempool live over websocket")

		err = m.consume(ctx, ch, sub)

		sub.Unsubscribe()
		m.streamLive.Store(false)

		if err != nil {
			m.log.WithError(err).Warn("EL mempool subscription lost, polling the filter until it is back")
		}
	}
}

// consume inserts the streamed arrivals until the subscription or ctx ends.
func (m *elMirror) consume(ctx context.Context, ch <-chan *types.Transaction, sub execution.PendingTxSubscription) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-sub.Err():
			return err
		case tx := <-ch:
			if tx != nil {
				m.insert(tx, time.Now())
			}
		}
	}
}

// sleepCtx waits d; false when ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// installFilter (re)installs the pending transaction filter; on failure the
// mirror falls back to the periodic resync alone.
func (m *elMirror) installFilter(ctx context.Context) {
	callCtx, cancel := context.WithTimeout(ctx, mirrorCallTimeout)
	defer cancel()

	id, err := m.pool.elClient.NewPendingTransactionFilter(callCtx)
	if err != nil {
		m.log.WithError(err).Debug("No pending transaction filter, the EL mempool mirror resyncs only")

		m.filterID = ""

		return
	}

	m.filterID = id
}

// poll fetches the arrivals the filter reported. A lost filter is
// reinstalled and followed by a resync, since arrivals in between are unknown.
func (m *elMirror) poll(ctx context.Context) {
	if m.filterID == "" {
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, mirrorCallTimeout)
	defer cancel()

	hashes, err := m.pool.elClient.PendingTransactionFilterChanges(callCtx, m.filterID)
	if err != nil {
		m.log.WithError(err).Debug("Pending transaction filter lost, reinstalling")
		m.installFilter(ctx)
		m.syncedAt = time.Time{}

		return
	}

	m.mu.Lock()
	unknown := make([]common.Hash, 0, len(hashes))

	for _, hash := range hashes {
		if m.byHash[hash] == nil {
			unknown = append(unknown, hash)
		}
	}
	m.mu.Unlock()

	if len(unknown) == 0 {
		return
	}

	txs, err := m.pool.elClient.TransactionsByHash(callCtx, unknown)
	if err != nil {
		m.log.WithError(err).Debug("Cannot fetch new EL mempool transactions")

		return
	}

	now := time.Now()

	for _, hash := range unknown {
		if tx := txs[hash]; tx != nil {
			m.insert(tx, now)
		}
	}
}

// resync reads the full EL mempool: adds what the filter missed and drops
// the non-blob entries the EL no longer lists. Blob entries cannot be
// confirmed this way (geth lists no blob transactions) and expire by age.
func (m *elMirror) resync(ctx context.Context) error {
	callCtx, cancel := context.WithTimeout(ctx, mirrorCallTimeout)
	defer cancel()

	started := time.Now()

	txs, err := m.pool.elClient.PendingPoolTransactions(callCtx)
	if err != nil {
		return err
	}

	listed := make(map[common.Hash]struct{}, len(txs))
	for _, tx := range txs {
		listed[tx.Hash()] = struct{}{}
		m.insert(tx, started)
	}

	m.mu.Lock()
	for hash, entry := range m.byHash {
		if _, ok := listed[hash]; ok {
			entry.seenAt = started

			continue
		}

		blob := entry.tx.Tx.Type() == types.BlobTxType
		if (!blob && entry.seenAt.Before(started)) || (blob && started.Sub(entry.seenAt) > mirrorBlobMaxAge) {
			m.removeLocked(entry.tx)
		}
	}
	m.mu.Unlock()

	m.syncedAt = started

	return nil
}

// insert adds a transaction, replacing the sender's entry at the same nonce
// (the EL accepted a replacement).
func (m *elMirror) insert(tx *types.Transaction, now time.Time) {
	m.pool.mu.RLock()
	signer := m.pool.signer
	m.pool.mu.RUnlock()

	if signer == nil {
		return
	}

	m.mu.Lock()
	if entry := m.byHash[tx.Hash()]; entry != nil {
		entry.seenAt = now
		m.mu.Unlock()

		return
	}
	m.mu.Unlock()

	// Recovered outside the lock: the expensive part, paid once per
	// transaction.
	sender, err := types.Sender(signer, tx)
	if err != nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.byHash) >= mirrorMaxTxs {
		return
	}

	byNonce := m.bySender[sender]
	if byNonce == nil {
		byNonce = make(map[uint64]*mirrorEntry, 4)
		m.bySender[sender] = byNonce
	}

	if old := byNonce[tx.Nonce()]; old != nil {
		delete(m.byHash, old.tx.Hash)
	}

	m.seq++
	entry := &mirrorEntry{
		tx: &PooledTx{
			Hash:   tx.Hash(),
			Tx:     tx,
			Sender: sender,
			Size:   tx.Size(),
			Seq:    m.seq,
		},
		seenAt: now,
	}

	byNonce[tx.Nonce()] = entry
	m.byHash[entry.tx.Hash] = entry

	if hint := m.baseFeeHint.Load(); hint == 0 || tx.GasFeeCap().Cmp(new(big.Int).SetUint64(hint)) >= 0 {
		m.arrivals.Add(1)
	}
}

// noteBlock drops the transactions an included block carries and every
// lower nonce of their senders.
func (m *elMirror) noteBlock(ctx context.Context, blockHash common.Hash) {
	m.mu.Lock()
	active := m.active
	m.mu.Unlock()

	if !active {
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, mirrorCallTimeout)
	defer cancel()

	block, found, err := m.pool.elClient.BlockTransactions(callCtx, blockHash)
	if err != nil || !found {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, hash := range block.TxHashes {
		entry := m.byHash[hash]
		if entry == nil {
			continue
		}

		included := entry.tx.Tx.Nonce()

		for nonce, other := range m.bySender[entry.tx.Sender] {
			if nonce <= included {
				m.removeLocked(other.tx)
			}
		}
	}
}

// dropStale removes transactions the selection found below their sender's
// account nonce.
func (m *elMirror) dropStale(hashes []common.Hash) {
	if len(hashes) == 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, hash := range hashes {
		if entry := m.byHash[hash]; entry != nil {
			m.removeLocked(entry.tx)
		}
	}
}

func (m *elMirror) removeLocked(tx *PooledTx) {
	delete(m.byHash, tx.Hash)

	byNonce := m.bySender[tx.Sender]
	if byNonce == nil {
		return
	}

	if entry := byNonce[tx.Tx.Nonce()]; entry != nil && entry.tx.Hash == tx.Hash {
		delete(byNonce, tx.Tx.Nonce())
	}

	if len(byNonce) == 0 {
		delete(m.bySender, tx.Sender)
	}
}

// deactivate stops following the EL mempool and frees the mirror. Called
// from the run goroutine only (it owns followCancel).
func (m *elMirror) deactivate() {
	if m.followCancel != nil {
		m.followCancel()
		m.followCancel = nil
	}

	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	m.mu.Lock()
	wasActive := m.active
	m.active = false
	m.byHash = make(map[common.Hash]*mirrorEntry, 1024)
	m.bySender = make(map[common.Address]map[uint64]*mirrorEntry, 1024)
	m.mu.Unlock()

	if m.filterID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), mirrorCallTimeout)
		_ = m.pool.elClient.UninstallFilter(ctx, m.filterID)

		cancel()

		m.filterID = ""
	}

	m.syncedAt = time.Time{}

	if wasActive {
		m.log.Info("EL mempool mirror stopped (unused)")
	}
}

func (m *elMirror) size() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.byHash)
}
