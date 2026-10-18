package txpool

import (
	"context"
	"fmt"
	"math"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

// feeBucketsPerDecade sets the resolution of the fee distribution: buckets
// grow by a factor of 10^(1/8) ≈ 1.33, fine around the base fee, compact
// across the orders of magnitude a busy mempool spans.
const feeBucketsPerDecade = 8

// FeeDistribution is the pending gas of the EL mempool (mirror) and of the
// pool by max fee per gas: how much gas would clear a given base fee.
type FeeDistribution struct {
	NextBaseFee string      `json:"next_base_fee"`
	GasLimit    uint64      `json:"gas_limit"`
	Buckets     []FeeBucket `json:"buckets"`
	// Private: the pool's transactions are included (logged-in readers).
	Private bool `json:"private"`
	// MirrorActive: the EL mempool part is current (the mirror runs only
	// while builds use it).
	MirrorActive bool `json:"mirror_active"`
}

// FeeBucket is one max-fee range [MinFee, MaxFee) in wei.
type FeeBucket struct {
	MinFee     string `json:"min_fee"`
	MaxFee     string `json:"max_fee"`
	ELGas      uint64 `json:"el_gas"`
	ELTxs      int    `json:"el_txs"`
	PrivateGas uint64 `json:"private_gas,omitempty"`
	PrivateTxs int    `json:"private_txs,omitempty"`
}

// FeeDistribution buckets the pending transactions by max fee per gas on a
// log scale, against the base fee of the block after parentHash. The pool's
// (private) transactions are included only with includePrivate; one queued
// in both counts as private.
func (p *Pool) FeeDistribution(ctx context.Context, parentHash common.Hash, includePrivate bool) (*FeeDistribution, error) {
	parent, err := p.elClient.HeaderByHash(ctx, parentHash)
	if err != nil {
		return nil, fmt.Errorf("fee distribution: %w", err)
	}

	type point struct {
		fee     *big.Int
		gas     uint64
		private bool
	}

	points := make([]point, 0, 1024)
	private := make(map[common.Hash]struct{}, 64)

	if includePrivate {
		p.mu.RLock()
		for hash, tx := range p.byHash {
			private[hash] = struct{}{}
			points = append(points, point{fee: tx.GasFeeCap(), gas: tx.Gas(), private: true})
		}
		p.mu.RUnlock()
	}

	for _, tx := range p.mirror.snapshot() {
		if _, ok := private[tx.Hash]; !ok {
			points = append(points, point{fee: tx.GasFeeCap(), gas: tx.Gas()})
		}
	}

	dist := &FeeDistribution{
		NextBaseFee:  calcNextBaseFee(parent).String(),
		GasLimit:     parent.GasLimit,
		Buckets:      []FeeBucket{},
		Private:      includePrivate,
		MirrorActive: p.mirror.Stats().Active,
	}

	if len(points) == 0 {
		return dist, nil
	}

	lowest, highest := math.MaxInt, math.MinInt
	indices := make([]int, len(points))

	for i, pt := range points {
		indices[i] = feeBucketIndex(pt.fee)
		lowest = min(lowest, indices[i])
		highest = max(highest, indices[i])
	}

	dist.Buckets = make([]FeeBucket, highest-lowest+1)
	for i := range dist.Buckets {
		dist.Buckets[i].MinFee = feeBucketEdge(lowest + i).String()
		dist.Buckets[i].MaxFee = feeBucketEdge(lowest + i + 1).String()
	}

	for i, pt := range points {
		bucket := &dist.Buckets[indices[i]-lowest]

		if pt.private {
			bucket.PrivateGas += pt.gas
			bucket.PrivateTxs++
		} else {
			bucket.ELGas += pt.gas
			bucket.ELTxs++
		}
	}

	return dist, nil
}

// feeBucketIndex is the log-scale bucket of a fee: floor(8 * log10(gwei)),
// with fees below 0.001 gwei in the lowest bucket.
func feeBucketIndex(fee *big.Int) int {
	gwei, _ := new(big.Float).Quo(new(big.Float).SetInt(fee), big.NewFloat(1e9)).Float64()

	return int(math.Floor(feeBucketsPerDecade * math.Log10(max(gwei, 1e-3))))
}

// feeBucketEdge is the lower edge of a bucket in wei.
func feeBucketEdge(index int) *big.Int {
	wei, _ := big.NewFloat(1e9 * math.Pow(10, float64(index)/feeBucketsPerDecade)).Int(nil)

	return wei
}

// currentSlot is the chain's current slot (0 without a chain service).
func (p *Pool) currentSlot() phase0.Slot {
	if p.chainSvc == nil {
		return 0
	}

	return p.chainSvc.GetCurrentSlot()
}

// ELMempoolStats describes the EL mempool mirror.
func (p *Pool) ELMempoolStats() MirrorStats {
	return p.mirror.Stats()
}

// ListELMempool returns a page of the mirrored EL mempool transactions plus
// the total match count. It never starts the mirror.
func (p *Pool) ListELMempool(opts ListOptions) ([]*PooledTx, int) {
	all := p.mirror.snapshot()
	matches := all[:0]

	for _, tx := range all {
		if opts.Sender == nil || tx.Sender == *opts.Sender {
			matches = append(matches, tx)
		}
	}

	return pageTxs(matches, opts)
}
