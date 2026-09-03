package tx_intake

import (
	"container/heap"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// Packing policies.
const (
	PolicyFIFO       = "fifo"        // oldest submission first
	PolicyFee        = "fee"         // highest effective tip first
	PolicyRoundRobin = "round_robin" // one tx per sender per round
	PolicyAsGiven    = "as_given"    // exactly the explicit list, in order
)

// Skip reasons: the tx stays queued but is not in this plan.
const (
	SkipNonceGap          = "nonce_gap"
	SkipFeeTooLow         = "fee_too_low"
	SkipBlobFeeTooLow     = "blob_fee_too_low"
	SkipInsufficientFunds = "insufficient_funds"
	SkipGasCap            = "gas_cap"
	SkipBlobCap           = "blob_cap"
	SkipSizeCap           = "size_cap"
	SkipTxCap             = "tx_cap"
)

// ValidPolicy reports whether p is a known packing policy.
func ValidPolicy(p string) bool {
	switch p {
	case PolicyFIFO, PolicyFee, PolicyRoundRobin, PolicyAsGiven:
		return true
	default:
		return false
	}
}

// BlockContext is what the packer needs to know about the block being built.
type BlockContext struct {
	ParentHash  common.Hash
	GasLimit    uint64   // gas limit the block will carry
	BaseFee     *big.Int // base fee of the block being built
	BlobBaseFee *big.Int // blob base fee of the block being built
	MaxBlobs    int
	MaxBytes    uint64
}

// SenderState is the sender's account state at the parent block.
type SenderState struct {
	Nonce   uint64
	Balance *big.Int
}

// FillSpec is the operator's fill instruction for one block.
type FillSpec struct {
	GasPct   uint64        // share of the block gas limit to fill, 1..100
	MaxTxs   uint64        // 0 = unlimited
	MaxBlobs uint64        // 0 = the block's blob cap
	Policy   string        // one of the Policy* constants
	Txs      []common.Hash // explicit ordered list, policy as_given only
}

// Plan is the ordered transaction list a block will be built from, plus the
// accounting of what was left out and why.
type Plan struct {
	Entries []*Entry
	GasSum  uint64
	Blobs   int
	Bytes   uint64
	GasCap  uint64
	Skipped map[string]int
	Evicted []Eviction
}

// Hashes returns the plan's transaction hashes in order.
func (p *Plan) Hashes() []common.Hash {
	out := make([]common.Hash, len(p.Entries))
	for i, e := range p.Entries {
		out[i] = e.Hash()
	}

	return out
}

// Raw returns the plan's network-encoded transactions in order.
func (p *Plan) Raw() [][]byte {
	out := make([][]byte, len(p.Entries))
	for i, e := range p.Entries {
		out[i] = e.Raw
	}

	return out
}

// Pack selects the transactions for one block from the caller's queue
// snapshot. Snapshot and states MUST describe the same moment — take the
// snapshot once and derive states from it — otherwise a sender that arrives
// in between has no state and the pack fails. The parent state nonce decides
// what is still buildable, and stale entries are evicted here.
func Pack(q *Queue, snapshot map[common.Address][]*Entry, states map[common.Address]SenderState, bctx *BlockContext, spec FillSpec) (*Plan, error) {
	if spec.GasPct == 0 || spec.GasPct > 100 {
		return nil, fmt.Errorf("fill gas_pct must be 1..100, got %d", spec.GasPct)
	}

	if !ValidPolicy(spec.Policy) {
		return nil, fmt.Errorf("unknown packing policy %q", spec.Policy)
	}

	maxBlobs := bctx.MaxBlobs
	if spec.MaxBlobs != 0 && int(spec.MaxBlobs) < maxBlobs {
		maxBlobs = int(spec.MaxBlobs)
	}

	ps := &packer{
		plan:     &Plan{GasCap: bctx.GasLimit * spec.GasPct / 100, Skipped: make(map[string]int)},
		bctx:     bctx,
		maxBlobs: maxBlobs,
		maxTxs:   spec.MaxTxs,
		spent:    make(map[common.Address]*big.Int),
		states:   states,
	}

	chains, err := ps.prepareChains(q, snapshot)
	if err != nil {
		return nil, err
	}

	switch spec.Policy {
	case PolicyAsGiven:
		err = ps.packExplicit(q, chains, spec.Txs)
	case PolicyRoundRobin:
		ps.packRoundRobin(chains)
	default:
		ps.packByPriority(chains, spec.Policy)
	}

	if err != nil {
		return nil, err
	}

	return ps.plan, nil
}

// senderChain is a sender's contiguous buildable nonce chain and a cursor.
type senderChain struct {
	sender  common.Address
	entries []*Entry
	next    int
	state   SenderState
	first   time.Time
}

func (c *senderChain) head() *Entry {
	if c.next >= len(c.entries) {
		return nil
	}

	return c.entries[c.next]
}

type packer struct {
	plan     *Plan
	bctx     *BlockContext
	maxBlobs int
	maxTxs   uint64
	spent    map[common.Address]*big.Int
	states   map[common.Address]SenderState
}

// prepareChains applies the state-nonce rule per sender: nonces below the
// parent state nonce are evicted, the chain starts at the state nonce and
// stops at the first gap.
func (ps *packer) prepareChains(q *Queue, snapshot map[common.Address][]*Entry) ([]*senderChain, error) {
	chains := make([]*senderChain, 0, len(snapshot))

	for sender, entries := range snapshot {
		st, ok := ps.states[sender]
		if !ok {
			return nil, fmt.Errorf("no parent state for sender %s "+
				"(snapshot and states disagree; they must come from the same Snapshot call)", sender.Hex())
		}

		ps.plan.Evicted = append(ps.plan.Evicted, q.EvictBelow(sender, st.Nonce)...)

		chain := &senderChain{sender: sender, state: st}
		expected := st.Nonce

		for _, e := range entries {
			if e.Tx.Nonce() < st.Nonce {
				continue
			}

			if e.Tx.Nonce() != expected {
				ps.plan.Skipped[SkipNonceGap]++
				continue
			}

			if chain.first.IsZero() || e.AddedAt.Before(chain.first) {
				chain.first = e.AddedAt
			}

			chain.entries = append(chain.entries, e)
			expected++
		}

		if len(chain.entries) > 0 {
			chains = append(chains, chain)
		}
	}

	sort.Slice(chains, func(i, j int) bool { return chains[i].first.Before(chains[j].first) })

	return chains, nil
}

// tryAdd checks every cap and the sender budget for the chain's next entry
// and appends it to the plan. It returns the skip reason when it does not fit.
func (ps *packer) tryAdd(e *Entry) string {
	tx := e.Tx

	if ps.maxTxs != 0 && uint64(len(ps.plan.Entries)) >= ps.maxTxs {
		return SkipTxCap
	}

	if tx.GasFeeCapIntCmp(ps.bctx.BaseFee) < 0 {
		return SkipFeeTooLow
	}

	blobs := len(tx.BlobHashes())
	if blobs > 0 {
		if ps.bctx.BlobBaseFee != nil && tx.BlobGasFeeCapIntCmp(ps.bctx.BlobBaseFee) < 0 {
			return SkipBlobFeeTooLow
		}

		if ps.plan.Blobs+blobs > ps.maxBlobs {
			return SkipBlobCap
		}
	}

	if ps.plan.GasSum+tx.Gas() > ps.plan.GasCap {
		return SkipGasCap
	}

	if ps.bctx.MaxBytes != 0 && ps.plan.Bytes+tx.Size() > ps.bctx.MaxBytes {
		return SkipSizeCap
	}

	spent := ps.spent[e.Sender]
	if spent == nil {
		spent = new(big.Int)
		ps.spent[e.Sender] = spent
	}

	cost := new(big.Int).Add(spent, tx.Cost())
	if cost.Cmp(ps.states[e.Sender].Balance) > 0 {
		return SkipInsufficientFunds
	}

	spent.Set(cost)
	ps.plan.Entries = append(ps.plan.Entries, e)
	ps.plan.GasSum += tx.Gas()
	ps.plan.Blobs += blobs
	ps.plan.Bytes += tx.Size()

	return ""
}

// chainStops reports whether a skip reason ends the sender's chain for this
// block: every later nonce depends on the skipped one.
func chainStops(reason string) bool {
	return reason != SkipBlobCap
}

// candidateHeap orders sender chains by their head transaction.
type candidateHeap struct {
	chains []*senderChain
	less   func(a, b *Entry) bool
}

func (h *candidateHeap) Len() int { return len(h.chains) }
func (h *candidateHeap) Less(i, j int) bool {
	return h.less(h.chains[i].head(), h.chains[j].head())
}
func (h *candidateHeap) Swap(i, j int) { h.chains[i], h.chains[j] = h.chains[j], h.chains[i] }
func (h *candidateHeap) Push(x any)    { h.chains = append(h.chains, x.(*senderChain)) }
func (h *candidateHeap) Pop() any {
	n := len(h.chains)
	c := h.chains[n-1]
	h.chains = h.chains[:n-1]

	return c
}

func (ps *packer) packByPriority(chains []*senderChain, policy string) {
	less := func(a, b *Entry) bool { return a.AddedAt.Before(b.AddedAt) }
	if policy == PolicyFee {
		baseFee := ps.bctx.BaseFee
		less = func(a, b *Entry) bool {
			ta, tb := a.Tx.EffectiveGasTipValue(baseFee), b.Tx.EffectiveGasTipValue(baseFee)
			if c := ta.Cmp(tb); c != 0 {
				return c > 0
			}

			return a.AddedAt.Before(b.AddedAt)
		}
	}

	h := &candidateHeap{less: less}
	for _, c := range chains {
		heap.Push(h, c)
	}

	for h.Len() > 0 {
		c := heap.Pop(h).(*senderChain)
		e := c.head()

		reason := ps.tryAdd(e)
		if reason == SkipTxCap {
			ps.plan.Skipped[reason] += len(c.entries) - c.next
			for h.Len() > 0 {
				rest := heap.Pop(h).(*senderChain)
				ps.plan.Skipped[reason] += len(rest.entries) - rest.next
			}

			return
		}

		if reason != "" {
			ps.plan.Skipped[reason]++
			if chainStops(reason) {
				ps.plan.Skipped[reason] += len(c.entries) - c.next - 1
				continue
			}
		}

		c.next++
		if c.head() != nil {
			heap.Push(h, c)
		}
	}
}

func (ps *packer) packRoundRobin(chains []*senderChain) {
	active := chains
	for len(active) > 0 {
		var still []*senderChain

		for _, c := range active {
			e := c.head()

			reason := ps.tryAdd(e)
			if reason == SkipTxCap {
				for _, rest := range active {
					ps.plan.Skipped[reason] += len(rest.entries) - rest.next
				}

				return
			}

			if reason != "" {
				ps.plan.Skipped[reason]++
				if chainStops(reason) {
					ps.plan.Skipped[reason] += len(c.entries) - c.next - 1
					continue
				}
			}

			c.next++
			if c.head() != nil {
				still = append(still, c)
			}
		}

		active = still
	}
}

// packExplicit builds exactly the requested list. Anything that would make
// the plan differ from the request is an error, never a silent trim.
func (ps *packer) packExplicit(q *Queue, chains []*senderChain, hashes []common.Hash) error {
	if len(hashes) == 0 {
		return errors.New("policy as_given needs a non-empty tx list")
	}

	byHash := make(map[common.Hash]*Entry)
	expected := make(map[common.Address]uint64, len(chains))

	for _, c := range chains {
		expected[c.sender] = c.state.Nonce
		for _, e := range c.entries {
			byHash[e.Hash()] = e
		}
	}

	for i, h := range hashes {
		e := byHash[h]
		if e == nil {
			if q.Lookup(h) == nil {
				return fmt.Errorf("tx %d (%s) is not queued", i, h.Hex())
			}

			return fmt.Errorf("tx %d (%s) is not buildable on the parent (stale nonce or nonce gap)", i, h.Hex())
		}

		if want := expected[e.Sender]; e.Tx.Nonce() != want {
			return fmt.Errorf("tx %d (%s) has nonce %d, sender %s expects %d at this position",
				i, h.Hex(), e.Tx.Nonce(), e.Sender.Hex(), want)
		}

		if reason := ps.tryAdd(e); reason != "" {
			return fmt.Errorf("tx %d (%s) does not fit: %s", i, h.Hex(), reason)
		}

		expected[e.Sender]++
	}

	return nil
}
