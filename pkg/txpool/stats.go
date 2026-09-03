package txpool

import (
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// counters are the pool's lifetime counters (guarded by Pool.mu).
type counters struct {
	Admitted               uint64
	Replaced               uint64
	Removed                uint64
	Cleared                uint64
	EvictedIncludedByUs    uint64
	EvictedIncludedByOther uint64
	EvictedNonceTooLow     uint64
	EvictedTTL             uint64
	Rejected               map[string]uint64
	LastAdmittedAt         time.Time
}

// Stats is a snapshot of the pool's content aggregates and lifetime counters,
// serialized for the WebUI.
type Stats struct {
	Enabled bool `json:"enabled"`

	Pending  int    `json:"pending"`
	Senders  int    `json:"senders"`
	BlobTxs  int    `json:"blob_txs"`
	Blobs    int    `json:"blobs"`
	Bytes    uint64 `json:"bytes"`
	GasSum   uint64 `json:"gas_sum"`
	ValueWei string `json:"value_wei"`

	Admitted               uint64            `json:"admitted"`
	Replaced               uint64            `json:"replaced"`
	Removed                uint64            `json:"removed"`
	Cleared                uint64            `json:"cleared"`
	EvictedIncludedByUs    uint64            `json:"evicted_included_by_us"`
	EvictedIncludedByOther uint64            `json:"evicted_included_by_other"`
	EvictedNonceTooLow     uint64            `json:"evicted_nonce_too_low"`
	EvictedTTL             uint64            `json:"evicted_ttl"`
	Rejected               map[string]uint64 `json:"rejected"`

	LastAdmittedAt *time.Time `json:"last_admitted_at,omitempty"`
	Version        uint64     `json:"version"`
}

// Stats returns the current snapshot.
func (p *Pool) Stats() Stats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	stats := Stats{
		Enabled:                p.enabled.Load(),
		Pending:                len(p.byHash),
		Senders:                len(p.bySender),
		Admitted:               p.stats.Admitted,
		Replaced:               p.stats.Replaced,
		Removed:                p.stats.Removed,
		Cleared:                p.stats.Cleared,
		EvictedIncludedByUs:    p.stats.EvictedIncludedByUs,
		EvictedIncludedByOther: p.stats.EvictedIncludedByOther,
		EvictedNonceTooLow:     p.stats.EvictedNonceTooLow,
		EvictedTTL:             p.stats.EvictedTTL,
		Rejected:               make(map[string]uint64, len(p.stats.Rejected)),
		Version:                p.version.Load(),
	}

	for reason, n := range p.stats.Rejected {
		stats.Rejected[reason] = n
	}

	if !p.stats.LastAdmittedAt.IsZero() {
		at := p.stats.LastAdmittedAt
		stats.LastAdmittedAt = &at
	}

	value := new(big.Int)

	for _, tx := range p.byHash {
		stats.Bytes += tx.Size
		stats.GasSum += tx.Tx.Gas()
		value.Add(value, tx.Tx.Value())

		if tx.Tx.Type() == types.BlobTxType {
			stats.BlobTxs++
			stats.Blobs += len(tx.Tx.BlobHashes())
		}
	}

	stats.ValueWei = value.String()

	return stats
}

// TxSummary is the WebUI/API view of one queued transaction.
type TxSummary struct {
	Hash           string    `json:"hash"`
	Sender         string    `json:"sender"`
	Nonce          uint64    `json:"nonce"`
	Type           uint8     `json:"type"`
	To             string    `json:"to,omitempty"`
	Gas            uint64    `json:"gas"`
	MaxFeePerGas   string    `json:"max_fee_per_gas"`
	MaxPriorityFee string    `json:"max_priority_fee_per_gas"`
	MaxBlobFee     string    `json:"max_fee_per_blob_gas,omitempty"`
	ValueWei       string    `json:"value_wei"`
	Blobs          int       `json:"blobs,omitempty"`
	Size           uint64    `json:"size"`
	Arrived        time.Time `json:"arrived"`
	ArrivedSlot    uint64    `json:"arrived_slot"`
	Seq            uint64    `json:"seq"`
}

// Summary renders the queued transaction for the API.
func (t *PooledTx) Summary() TxSummary {
	s := TxSummary{
		Hash:           t.Hash.Hex(),
		Sender:         t.Sender.Hex(),
		Nonce:          t.Tx.Nonce(),
		Type:           t.Tx.Type(),
		Gas:            t.Tx.Gas(),
		MaxFeePerGas:   t.Tx.GasFeeCap().String(),
		MaxPriorityFee: t.Tx.GasTipCap().String(),
		ValueWei:       t.Tx.Value().String(),
		Size:           t.Size,
		Arrived:        t.Arrived,
		ArrivedSlot:    uint64(t.ArrivedSlot),
		Seq:            t.Seq,
	}

	if to := t.Tx.To(); to != nil {
		s.To = to.Hex()
	}

	if t.Tx.Type() == types.BlobTxType {
		s.Blobs = len(t.Tx.BlobHashes())

		if fee := t.Tx.BlobGasFeeCap(); fee != nil {
			s.MaxBlobFee = fee.String()
		}
	}

	return s
}

// ListOptions filters and orders a content listing.
type ListOptions struct {
	Sender *common.Address // nil = all senders
	Sort   string          // arrival (default) | sender | nonce | tip
	Offset int
	Limit  int
}

// List returns a page of queued transactions plus the total match count.
func (p *Pool) List(opts ListOptions) ([]*PooledTx, int) {
	p.mu.RLock()

	matches := make([]*PooledTx, 0, len(p.byHash))

	for _, tx := range p.byHash {
		if opts.Sender != nil && tx.Sender != *opts.Sender {
			continue
		}

		matches = append(matches, tx)
	}

	p.mu.RUnlock()

	switch strings.ToLower(opts.Sort) {
	case "sender", "nonce":
		sort.Slice(matches, func(i, j int) bool {
			if matches[i].Sender != matches[j].Sender {
				return matches[i].Sender.Cmp(matches[j].Sender) < 0
			}

			return matches[i].Tx.Nonce() < matches[j].Tx.Nonce()
		})
	case "tip":
		sort.Slice(matches, func(i, j int) bool {
			if c := matches[i].Tx.GasTipCap().Cmp(matches[j].Tx.GasTipCap()); c != 0 {
				return c > 0
			}

			return matches[i].Seq < matches[j].Seq
		})
	default:
		sort.Slice(matches, func(i, j int) bool { return matches[i].Seq < matches[j].Seq })
	}

	total := len(matches)

	if opts.Offset >= total {
		return []*PooledTx{}, total
	}

	end := total
	if opts.Limit > 0 && opts.Offset+opts.Limit < end {
		end = opts.Offset + opts.Limit
	}

	return matches[opts.Offset:end], total
}
