package txpool

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/sirupsen/logrus"
)

// Transactions far below the base fee cannot make a block for many slots: the
// base fee falls by at most 1/8 per block. Such a COLD transaction keeps only
// its facts (nonce, fees, gas, ...) in memory; its body lives in the pool's
// store, or for the EL mempool mirror in the EL, and is loaded back once the
// base fee came close enough. The two thresholds form a hysteresis band, so
// a fee near one of them does not flap between the tiers.
const (
	// coldDemoteBlocks: a fee cap below the next base fee · (7/8)^16
	// (≈ 12%) turns cold.
	coldDemoteBlocks = 16
	// coldPromoteBlocks: a cold fee cap at or above the next base fee ·
	// (7/8)^12 (≈ 20%) is loaded back, at least 12 blocks before it can be
	// included.
	coldPromoteBlocks = 12
)

// txFacts are the properties of a transaction the pool's bookkeeping reads
// (queues, replacement pricing, listings, fee charts). A cold transaction
// keeps them while its body is not in memory.
type txFacts struct {
	nonce         uint64
	txType        uint8
	gas           uint64
	gasFeeCap     *big.Int
	gasTipCap     *big.Int
	blobGasFeeCap *big.Int
	value         *big.Int
	to            *common.Address
	blobs         int
}

func factsOf(tx *types.Transaction) *txFacts {
	return &txFacts{
		nonce:         tx.Nonce(),
		txType:        tx.Type(),
		gas:           tx.Gas(),
		gasFeeCap:     tx.GasFeeCap(),
		gasTipCap:     tx.GasTipCap(),
		blobGasFeeCap: tx.BlobGasFeeCap(),
		value:         tx.Value(),
		to:            tx.To(),
		blobs:         len(tx.BlobHashes()),
	}
}

// Cold reports that the transaction's body is not in memory (Tx is nil).
func (t *PooledTx) Cold() bool { return t.Tx == nil }

func (t *PooledTx) Nonce() uint64 {
	if t.Tx != nil {
		return t.Tx.Nonce()
	}

	return t.facts.nonce
}

func (t *PooledTx) Type() uint8 {
	if t.Tx != nil {
		return t.Tx.Type()
	}

	return t.facts.txType
}

func (t *PooledTx) Gas() uint64 {
	if t.Tx != nil {
		return t.Tx.Gas()
	}

	return t.facts.gas
}

func (t *PooledTx) GasFeeCap() *big.Int {
	if t.Tx != nil {
		return t.Tx.GasFeeCap()
	}

	return t.facts.gasFeeCap
}

func (t *PooledTx) GasTipCap() *big.Int {
	if t.Tx != nil {
		return t.Tx.GasTipCap()
	}

	return t.facts.gasTipCap
}

func (t *PooledTx) BlobGasFeeCap() *big.Int {
	if t.Tx != nil {
		return t.Tx.BlobGasFeeCap()
	}

	return t.facts.blobGasFeeCap
}

func (t *PooledTx) Value() *big.Int {
	if t.Tx != nil {
		return t.Tx.Value()
	}

	return t.facts.value
}

func (t *PooledTx) To() *common.Address {
	if t.Tx != nil {
		return t.Tx.To()
	}

	return t.facts.to
}

// Blobs is the number of blobs the transaction carries.
func (t *PooledTx) Blobs() int {
	if t.Tx != nil {
		return len(t.Tx.BlobHashes())
	}

	return t.facts.blobs
}

// cold returns a copy of a hot transaction without its body. Entries are
// replaced, never mutated: snapshots handed to a running selection keep the
// body they were taken with.
func (t *PooledTx) cold() *PooledTx {
	c := t.withBody(nil)
	c.facts = factsOf(t.Tx)

	return c
}

// withBody returns a copy carrying the given body.
func (t *PooledTx) withBody(tx *types.Transaction) *PooledTx {
	c := &PooledTx{
		Hash:        t.Hash,
		Sender:      t.Sender,
		Tx:          tx,
		Size:        t.Size,
		Arrived:     t.Arrived,
		ArrivedSlot: t.ArrivedSlot,
		Seq:         t.Seq,
		Submitter:   t.Submitter,
	}
	c.strikes.Store(t.strikes.Load())

	return c
}

// tierFloors are the demotion and promotion fee floors for a next base fee.
type tierFloors struct {
	demote  *big.Int
	promote *big.Int
}

func floorsFor(baseFee *big.Int) *tierFloors {
	if baseFee == nil || baseFee.Sign() <= 0 {
		return nil
	}

	return &tierFloors{
		demote:  decayed(baseFee, coldDemoteBlocks),
		promote: decayed(baseFee, coldPromoteBlocks),
	}
}

// decayed is the base fee after n blocks of the steepest decrease.
func decayed(baseFee *big.Int, n int64) *big.Int {
	num := new(big.Int).Exp(big.NewInt(7), big.NewInt(n), nil)
	den := new(big.Int).Exp(big.NewInt(8), big.NewInt(n), nil)

	return num.Mul(num, baseFee).Div(num, den)
}

func (f *tierFloors) coldFee(fee *big.Int) bool {
	return f != nil && fee.Cmp(f.demote) < 0
}

func (f *tierFloors) warmFee(fee *big.Int) bool {
	return f == nil || fee.Cmp(f.promote) >= 0
}

// retier moves transactions between the tiers for the next block's base
// fee. Called by the eviction loop on every head.
func (p *Pool) retier(ctx context.Context, baseFee *big.Int) {
	floors := floorsFor(baseFee)
	if floors == nil {
		return
	}

	p.floors.Store(floors)

	// Without a store the pool's bodies have nowhere else to live.
	if p.store != nil {
		p.retierPool(floors)
	}

	p.mirror.retier(ctx, floors)
}

// retierPool demotes the queued transactions below the demotion floor and
// loads the bodies of the cold ones that cleared the promotion floor back
// from the store.
func (p *Pool) retierPool(floors *tierFloors) {
	demoted := 0
	promote := make([]*PooledTx, 0)

	p.mu.Lock()
	for _, queue := range p.bySender {
		for i, tx := range queue.txs {
			switch {
			case !tx.Cold() && floors.coldFee(tx.GasFeeCap()):
				queue.txs[i] = tx.cold()
				p.byHash[tx.Hash] = queue.txs[i]
				demoted++
			case tx.Cold() && floors.warmFee(tx.GasFeeCap()):
				promote = append(promote, tx)
			}
		}
	}
	p.mu.Unlock()

	promoted, lost := 0, 0

	for _, tx := range promote {
		stored, err := p.store.Get(tx.Hash)

		p.mu.Lock()
		switch {
		case p.byHash[tx.Hash] != tx:
			// Removed or replaced meanwhile.
		case err != nil || stored == nil:
			p.removeLocked(tx.Hash)
			p.stats.Removed++
			p.evictedLocked("store_missing")
			lost++
		default:
			p.swapLocked(tx, tx.withBody(stored.Tx))
			promoted++
		}
		p.mu.Unlock()
	}

	if demoted+promoted+lost > 0 {
		p.version.Add(1)
		p.log.WithFields(logrus.Fields{
			"demoted":  demoted,
			"promoted": promoted,
			"lost":     lost,
		}).Debug("Pool transactions moved between memory and the store")
	}
}

// swapLocked replaces an entry by its other-tier copy. Must hold mu.
func (p *Pool) swapLocked(old, next *PooledTx) {
	p.byHash[old.Hash] = next

	if queue := p.bySender[old.Sender]; queue != nil {
		if i, ok := queue.find(old.Nonce()); ok && queue.txs[i] == old {
			queue.txs[i] = next
		}
	}
}

// Hydrated returns the transaction with its body: a hot one as is, a cold
// one loaded from the store (nil when the store lost it).
func (p *Pool) Hydrated(tx *PooledTx) *PooledTx {
	if tx == nil || !tx.Cold() {
		return tx
	}

	if p.store == nil {
		return nil
	}

	stored, err := p.store.Get(tx.Hash)
	if err != nil || stored == nil {
		return nil
	}

	return tx.withBody(stored.Tx)
}

// retier demotes mirrored transactions below the demotion floor (dropping
// their bodies, the EL keeps them) and refetches the bodies of the cold ones
// that cleared the promotion floor; what the EL no longer has leaves.
func (m *elMirror) retier(ctx context.Context, floors *tierFloors) {
	m.mu.Lock()
	if !m.active {
		m.mu.Unlock()

		return
	}

	promote := make([]common.Hash, 0)

	for hash, entry := range m.byHash {
		switch {
		case !entry.tx.Cold() && floors.coldFee(entry.tx.GasFeeCap()):
			entry.tx = entry.tx.cold()
		case entry.tx.Cold() && floors.warmFee(entry.tx.GasFeeCap()):
			promote = append(promote, hash)
		}
	}
	m.mu.Unlock()

	if len(promote) == 0 {
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, mirrorCallTimeout)
	defer cancel()

	bodies, err := m.pool.elClient.TransactionsByHash(callCtx, promote)
	if err != nil {
		m.log.WithError(err).Debug("Cannot reload EL mempool transactions above the cold floor")

		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, hash := range promote {
		entry := m.byHash[hash]
		if entry == nil || !entry.tx.Cold() {
			continue
		}

		if body := bodies[hash]; body != nil {
			entry.tx = entry.tx.withBody(body)
		} else {
			m.removeLocked(entry.tx)
		}
	}
}
