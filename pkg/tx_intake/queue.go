// Package tx_intake is the private transaction path for testing builds: a
// JSON-RPC intake that keeps submitted transactions out of the EL's public
// txpool, a queue of per-sender nonce chains, and a packer that turns the
// queue into the exact ordered transaction list a block is built from.
package tx_intake

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Eviction reasons reported by the queue and the packer.
const (
	EvictNonceTooLow = "nonce_too_low" // a lower or equal nonce is already on chain
	EvictExpired     = "expired"       // older than the configured max age
	EvictReplaced    = "replaced"      // a newer tx for the same (sender, nonce)
	EvictStrikes     = "strikes"       // failed too many builds
	EvictFlushed     = "flushed"       // operator flush
)

// ErrQueueFull is returned by Add when the queue holds its maximum number of
// transactions. The submitter is expected to back off and rebroadcast.
var ErrQueueFull = errors.New("tx queue full")

// Entry is one queued transaction: the decoded form for packing decisions and
// the raw network encoding (blob sidecars included) for the build call.
// Entries are shared with Snapshot callers, so the only mutable field is the
// strike count and it is atomic.
type Entry struct {
	Tx      *types.Transaction
	Raw     []byte
	Sender  common.Address
	AddedAt time.Time

	// strikes counts attributed build failures. Written by Strike (under the
	// queue mutex) and read concurrently by snapshot holders, hence atomic.
	strikes atomic.Int32
}

// Strikes returns the transaction's attributed build failures.
func (e *Entry) Strikes() int { return int(e.strikes.Load()) }

// Hash returns the transaction hash.
func (e *Entry) Hash() common.Hash { return e.Tx.Hash() }

// Eviction records why a transaction left the queue.
type Eviction struct {
	Hash   common.Hash `json:"hash"`
	Sender string      `json:"sender"`
	Nonce  uint64      `json:"nonce"`
	Reason string      `json:"reason"`
	At     time.Time   `json:"at"`
}

// Stats is the queue summary exposed by the API and metrics.
type Stats struct {
	Txs         int               `json:"txs"`
	Senders     int               `json:"senders"`
	OldestAgeMs int64             `json:"oldest_age_ms"`
	Added       uint64            `json:"added_total"`
	Evictions   map[string]uint64 `json:"evictions_total"`
}

// Queue holds submitted transactions keyed by (sender, nonce) and by hash.
// Same hash is idempotent; a different hash for the same (sender, nonce)
// replaces the older entry. Nothing is removed at build time: a transaction
// leaves only through an explicit eviction, so the parent state nonce stays
// the single source of truth for what is still buildable.
type Queue struct {
	mu       sync.Mutex
	signer   types.Signer
	chainID  *big.Int
	maxTxs   int
	bySender map[common.Address]map[uint64]*Entry
	byHash   map[common.Hash]*Entry

	added     uint64
	evictions map[string]uint64
	recent    []Eviction // ring of the most recent evictions for the API
}

const recentEvictions = 256

// NewQueue creates a queue for the given chain id holding at most maxTxs
// transactions.
func NewQueue(chainID *big.Int, maxTxs int) *Queue {
	return &Queue{
		signer:    types.LatestSignerForChainID(chainID),
		chainID:   chainID,
		maxTxs:    maxTxs,
		bySender:  make(map[common.Address]map[uint64]*Entry),
		byHash:    make(map[common.Hash]*Entry),
		evictions: make(map[string]uint64),
	}
}

// Add decodes a network-encoded transaction and queues it. It returns the
// transaction hash. Re-adding a known hash is a no-op.
func (q *Queue) Add(raw []byte) (common.Hash, error) {
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(raw); err != nil {
		return common.Hash{}, fmt.Errorf("invalid transaction: %w", err)
	}

	if tx.ChainId() != nil && tx.ChainId().Sign() != 0 && tx.ChainId().Cmp(q.chainID) != 0 {
		return common.Hash{}, fmt.Errorf("wrong chain id %s (want %s)", tx.ChainId(), q.chainID)
	}

	sender, err := types.Sender(q.signer, tx)
	if err != nil {
		return common.Hash{}, fmt.Errorf("invalid signature: %w", err)
	}

	hash := tx.Hash()

	q.mu.Lock()
	defer q.mu.Unlock()

	if _, known := q.byHash[hash]; known {
		return hash, nil
	}

	chain := q.bySender[sender]
	if chain == nil {
		chain = make(map[uint64]*Entry)
		q.bySender[sender] = chain
	}

	if old := chain[tx.Nonce()]; old != nil {
		q.evictLocked(old, EvictReplaced)
	} else if len(q.byHash) >= q.maxTxs {
		return common.Hash{}, ErrQueueFull
	}

	entry := &Entry{Tx: tx, Raw: append([]byte(nil), raw...), Sender: sender, AddedAt: time.Now()}
	chain[tx.Nonce()] = entry
	q.byHash[hash] = entry
	q.added++

	return hash, nil
}

// PendingNonce returns the next nonce a sender should use: the chain nonce
// advanced over the contiguous queued chain that starts there.
func (q *Queue) PendingNonce(sender common.Address, chainNonce uint64) uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()

	next := chainNonce
	for q.bySender[sender][next] != nil {
		next++
	}

	return next
}

// Lookup returns the queued entry for a hash, or nil.
func (q *Queue) Lookup(hash common.Hash) *Entry {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.byHash[hash]
}

// Snapshot returns every sender's queued chain sorted by nonce. Entries are
// shared with the queue and must be treated as read-only.
func (q *Queue) Snapshot() map[common.Address][]*Entry {
	q.mu.Lock()
	defer q.mu.Unlock()

	out := make(map[common.Address][]*Entry, len(q.bySender))
	for sender, chain := range q.bySender {
		list := make([]*Entry, 0, len(chain))
		for _, e := range chain {
			list = append(list, e)
		}

		sort.Slice(list, func(i, j int) bool { return list[i].Tx.Nonce() < list[j].Tx.Nonce() })
		out[sender] = list
	}

	return out
}

// EvictBelow drops every queued transaction of the sender with a nonce below
// the given chain nonce: they can never be included any more.
func (q *Queue) EvictBelow(sender common.Address, chainNonce uint64) []Eviction {
	q.mu.Lock()
	defer q.mu.Unlock()

	var out []Eviction
	for nonce, e := range q.bySender[sender] {
		if nonce < chainNonce {
			out = append(out, q.evictLocked(e, EvictNonceTooLow))
		}
	}

	return out
}

// EvictExpired drops every transaction older than maxAge.
func (q *Queue) EvictExpired(maxAge time.Duration) []Eviction {
	q.mu.Lock()
	defer q.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)

	var out []Eviction
	for _, e := range q.byHash {
		if e.AddedAt.Before(cutoff) {
			out = append(out, q.evictLocked(e, EvictExpired))
		}
	}

	return out
}

// Strike counts a failed build against a transaction and evicts it once it
// reaches maxStrikes. It reports whether the transaction was evicted.
func (q *Queue) Strike(hash common.Hash, maxStrikes int) (Eviction, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	e := q.byHash[hash]
	if e == nil {
		return Eviction{}, false
	}

	if int(e.strikes.Add(1)) < maxStrikes {
		return Eviction{}, false
	}

	return q.evictLocked(e, EvictStrikes), true
}

// Remove drops the given transactions with the given reason (used when they
// were seen on chain).
func (q *Queue) Remove(hashes []common.Hash, reason string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, h := range hashes {
		if e := q.byHash[h]; e != nil {
			q.evictLocked(e, reason)
		}
	}
}

// Flush drops everything.
func (q *Queue) Flush() int {
	q.mu.Lock()
	defer q.mu.Unlock()

	n := len(q.byHash)
	for _, e := range q.byHash {
		q.evictLocked(e, EvictFlushed)
	}

	return n
}

// Stats returns the queue summary.
func (q *Queue) Stats() Stats {
	q.mu.Lock()
	defer q.mu.Unlock()

	s := Stats{Txs: len(q.byHash), Senders: len(q.bySender), Added: q.added, Evictions: make(map[string]uint64, len(q.evictions))}
	for k, v := range q.evictions {
		s.Evictions[k] = v
	}

	now := time.Now()
	for _, e := range q.byHash {
		if age := now.Sub(e.AddedAt).Milliseconds(); age > s.OldestAgeMs {
			s.OldestAgeMs = age
		}
	}

	return s
}

// RecentEvictions returns the most recent evictions, newest last.
func (q *Queue) RecentEvictions() []Eviction {
	q.mu.Lock()
	defer q.mu.Unlock()

	return append([]Eviction(nil), q.recent...)
}

func (q *Queue) evictLocked(e *Entry, reason string) Eviction {
	delete(q.byHash, e.Hash())

	chain := q.bySender[e.Sender]
	delete(chain, e.Tx.Nonce())

	if len(chain) == 0 {
		delete(q.bySender, e.Sender)
	}

	q.evictions[reason]++

	ev := Eviction{Hash: e.Hash(), Sender: e.Sender.Hex(), Nonce: e.Tx.Nonce(), Reason: reason, At: time.Now()}
	if len(q.recent) >= recentEvictions {
		q.recent = q.recent[1:]
	}

	q.recent = append(q.recent, ev)

	return ev
}
