package txpool

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
)

// Skip reasons reported per selection.
const (
	SkipNonceGap            = "nonce_gap"
	SkipNonceTooLow         = "nonce_too_low"
	SkipInsufficientBalance = "insufficient_balance"
	SkipFeeBelowBase        = "fee_below_base"
	SkipBlobFeeBelowBase    = "blob_fee_below_base"
	SkipGasFull             = "gas_full"
	SkipBlobFull            = "blob_full"
	SkipMaxTxs              = "max_txs"
	SkipBlobUnsupported     = "blob_unsupported"
	// SkipBlobSidecarVersion marks a blob transaction whose sidecar has the
	// other proof format than the block's fork needs (admitted across the
	// Osaka transition).
	SkipBlobSidecarVersion = "blob_sidecar_version"
	// SkipBlobUnavailable marks an EL mempool blob transaction whose blobs the
	// EL's blob pool could not supply; the sender's later fill transactions
	// follow it out.
	SkipBlobUnavailable = "blob_unavailable"
	SkipSizeFull        = "size_full"
	SkipDisabled        = "txpool_disabled"
)

// maxBlockBytes bounds the encoded transaction bytes of a selection: geth's
// RLP block size cap (params.MaxBlockSize) minus a margin for the header
// and withdrawals, which the cap counts too.
const maxBlockBytes = 8_388_608 - 64<<10

// SelectParams describe the block a selection is assembled for.
type SelectParams struct {
	// ParentHash is the execution parent the payload builds on; nonce,
	// balance and base fee are resolved against it.
	ParentHash common.Hash
	// GasLimit is the limit the block will carry (0 = the parent's).
	GasLimit uint64
	// MaxBlobs caps the blobs per block (0 = no blob transactions).
	MaxBlobs uint64
	// MaxTxs caps the selected transactions (0 = unlimited).
	MaxTxs uint64
	// GasFillPct is the share of GasLimit to fill (1-100; 0 = 100).
	GasFillPct uint64
	// BaseFeeCeiling, when non-nil, halves the fill to the EIP-1559 gas
	// target once the next base fee exceeds it (a fill knob; SelectQueued
	// ignores it).
	BaseFeeCeiling *big.Int
	// Ordering is fifo, tip or random.
	Ordering string
	// Seed drives the random ordering (the slot, so a preview and the build
	// agree).
	Seed uint64
	// InclusionList holds mandatory (FOCIL) transactions prepended verbatim.
	InclusionList [][]byte
	// IncludeBlobTxs allows blob transactions; false skips them with
	// blob_unsupported (ELs without a blobs bundle on the testing path).
	IncludeBlobTxs bool
	// BlobEncoding is network (sidecar carried) or canonical.
	BlobEncoding string
	// CellProofs marks an Osaka+ block: the network encoding then needs
	// version 1 (cell proof) sidecars, before it version 0 ones.
	CellProofs bool
	// Fill holds the EL mempool's pending transactions (senders recovered,
	// each sender's in nonce order). They fill the gas and blobs the pool
	// selection left, after it: each sender continues at the nonce and
	// balance the pool selection left, the highest tip goes first,
	// duplicates of pool transactions are skipped.
	Fill []*PooledTx
	// FillSidecars completes the selected fill blob transactions, which
	// txpool_content lists without their blobs. Without it the network
	// encoding cannot carry them and the fill skips blob transactions; the
	// canonical encoding needs no sidecar.
	FillSidecars SidecarSource
}

// SidecarSource supplies the blob sidecars of EL mempool blob transactions.
type SidecarSource interface {
	// BlobSidecars returns the sidecar of every transaction it could
	// complete, keyed by transaction hash; the others are left out.
	BlobSidecars(ctx context.Context, txs []*types.Transaction) (map[common.Hash]*types.BlobTxSidecar, error)
}

// Selection is the assembled transaction list plus its accounting.
type Selection struct {
	// Txs are the encoded transactions in block order (inclusion list first).
	Txs [][]byte
	// Selected are the pool transactions in the order they were placed.
	Selected []*PooledTx
	// GasSum is the gas limit sum of the selected pool transactions.
	GasSum uint64
	// Blobs is the number of blobs the selection carries.
	Blobs int
	// Skipped counts pool transactions left out, by reason.
	Skipped map[string]int
	// InclusionListTxs is how many inclusion-list transactions were prepended.
	InclusionListTxs int
	// PoolSize is the pool's pending count at snapshot time.
	PoolSize int
	// BaseFee / BlobBaseFee are the fee floors the selection enforced.
	BaseFee     *big.Int
	BlobBaseFee *big.Int
	// GasBudget is the gas the selection was allowed to fill.
	GasBudget uint64
	// CeilingApplied marks that the base-fee ceiling halved the fill.
	CeilingApplied bool
	// Bytes is the encoded size of the selected pool transactions.
	Bytes uint64
	// Snapshot is when the pool was read.
	Snapshot time.Time

	// FillCandidates / Fill are the EL mempool transactions offered (params
	// Fill) and the ones placed after the pool selection; FillGasSum,
	// FillBlobs, FillSkipped and FillBytes account them separately from the
	// pool's.
	FillCandidates int
	Fill           []*PooledTx
	FillGasSum     uint64
	FillBlobs      int
	FillBytes      uint64
	FillSkipped    map[string]int
}

// Summary is the JSON view of a selection (the transactions themselves are
// in the payload artifact).
type Summary struct {
	Selected         int            `json:"selected"`
	GasSum           uint64         `json:"gas_sum"`
	Blobs            int            `json:"blobs,omitempty"`
	Skipped          map[string]int `json:"skipped,omitempty"`
	InclusionListTxs int            `json:"inclusion_list_txs,omitempty"`
	PoolSize         int            `json:"pool_size"`
	BaseFee          string         `json:"base_fee,omitempty"`
	BlobBaseFee      string         `json:"blob_base_fee,omitempty"`
	GasBudget        uint64         `json:"gas_budget"`
	CeilingApplied   bool           `json:"ceiling_applied,omitempty"`
	Bytes            uint64         `json:"bytes,omitempty"`
	Hashes           []string       `json:"hashes,omitempty"`

	// EL mempool fill (tx source txpool_el_fill).
	FillCandidates int            `json:"fill_candidates,omitempty"`
	FillSelected   int            `json:"fill_selected,omitempty"`
	FillGasSum     uint64         `json:"fill_gas_sum,omitempty"`
	FillBlobs      int            `json:"fill_blobs,omitempty"`
	FillSkipped    map[string]int `json:"fill_skipped,omitempty"`
}

// Summary renders the selection for results and the API.
func (s *Selection) Summary() *Summary {
	if s == nil {
		return nil
	}

	out := &Summary{
		Selected:         len(s.Selected),
		GasSum:           s.GasSum,
		Blobs:            s.Blobs,
		Skipped:          s.Skipped,
		InclusionListTxs: s.InclusionListTxs,
		PoolSize:         s.PoolSize,
		GasBudget:        s.GasBudget,
		CeilingApplied:   s.CeilingApplied,
		Bytes:            s.Bytes,
		Hashes:           make([]string, 0, len(s.Selected)),
		FillCandidates:   s.FillCandidates,
		FillSelected:     len(s.Fill),
		FillGasSum:       s.FillGasSum,
		FillBlobs:        s.FillBlobs,
	}

	if len(s.FillSkipped) > 0 {
		out.FillSkipped = s.FillSkipped
	}

	if s.BaseFee != nil {
		out.BaseFee = s.BaseFee.String()
	}

	if s.BlobBaseFee != nil {
		out.BlobBaseFee = s.BlobBaseFee.String()
	}

	for _, tx := range s.Selected {
		out.Hashes = append(out.Hashes, tx.Hash.Hex())
	}

	return out
}

// EIP-1559 parameters (mainnet defaults; devnets keep them).
const (
	elasticityMultiplier     = 2
	baseFeeChangeDenominator = 8
)

// calcNextBaseFee computes the base fee of the block after parent per
// EIP-1559: unchanged at the gas target, otherwise moved by up to 1/8 in
// proportion to the deviation from the target.
func calcNextBaseFee(parent *types.Header) *big.Int {
	baseFee := parent.BaseFee
	if baseFee == nil {
		return new(big.Int)
	}

	gasTarget := parent.GasLimit / elasticityMultiplier

	switch {
	case parent.GasUsed == gasTarget:
		return new(big.Int).Set(baseFee)

	case parent.GasUsed > gasTarget:
		delta := new(big.Int).SetUint64(parent.GasUsed - gasTarget)
		delta.Mul(delta, baseFee)
		delta.Div(delta, new(big.Int).SetUint64(gasTarget))
		delta.Div(delta, big.NewInt(baseFeeChangeDenominator))

		if delta.Sign() == 0 {
			delta.SetInt64(1)
		}

		return delta.Add(baseFee, delta)

	default:
		delta := new(big.Int).SetUint64(gasTarget - parent.GasUsed)
		delta.Mul(delta, baseFee)
		delta.Div(delta, new(big.Int).SetUint64(gasTarget))
		delta.Div(delta, big.NewInt(baseFeeChangeDenominator))

		next := new(big.Int).Sub(baseFee, delta)
		if next.Sign() < 0 {
			next.SetInt64(0)
		}

		return next
	}
}

// Select assembles the transaction list for a block on the given parent:
// every sender's queue is walked from its parent-state nonce (gaps stop the
// walk), each transaction must cover its cost from the remaining balance and
// clear the next block's base fee (and blob base fee), and the senders' heads
// are merged in the requested ordering until the gas, blob and count budgets
// are exhausted. Nonce-too-low transactions found on the way are evicted.
func (p *Pool) Select(ctx context.Context, params *SelectParams) (*Selection, error) {
	sel := &Selection{
		Skipped:  make(map[string]int, 8),
		Snapshot: time.Now(),
	}

	if !p.enabled.Load() {
		sel.Skipped[SkipDisabled] = p.Stats().Pending
		sel.Txs = p.prependInclusionList(sel, nil, params.InclusionList)

		return sel, nil
	}

	parent, err := p.elClient.HeaderByHash(ctx, params.ParentHash)
	if err != nil {
		return nil, fmt.Errorf("select: %w", err)
	}

	sel.BaseFee = calcNextBaseFee(parent)

	gasLimit := params.GasLimit
	if gasLimit == 0 {
		gasLimit = parent.GasLimit
	}

	fillPct := params.GasFillPct
	if fillPct == 0 || fillPct > 100 {
		fillPct = 100
	}

	// Every full block raises the base fee by 12.5%; above the ceiling the
	// fill drops to the gas target so a long run does not price its own
	// transactions out.
	if params.BaseFeeCeiling != nil && params.BaseFeeCeiling.Sign() > 0 &&
		sel.BaseFee.Cmp(params.BaseFeeCeiling) > 0 && fillPct > 50 {
		fillPct = 50
		sel.CeilingApplied = true
	}

	sel.GasBudget = gasLimit / 100 * fillPct

	queues := p.senderSnapshot()
	for _, txs := range queues {
		sel.PoolSize += len(txs)
	}

	if sel.PoolSize == 0 && len(params.Fill) == 0 {
		sel.Txs = p.prependInclusionList(sel, nil, params.InclusionList)

		return sel, nil
	}

	if params.IncludeBlobTxs && (p.hasBlobTxs(queues) || fillCarriesBlobs(params) && hasBlobTx(params.Fill)) {
		blobFee, err := p.elClient.BlobBaseFee(ctx)
		if err != nil {
			return nil, fmt.Errorf("select: %w", err)
		}

		sel.BlobBaseFee = blobFee
	}

	senders := make([]common.Address, 0, len(queues))
	for sender := range queues {
		senders = append(senders, sender)
	}

	sort.Slice(senders, func(i, j int) bool { return senders[i].Cmp(senders[j]) < 0 })

	states := make(map[common.Address]*execution.AccountState, len(senders))

	if len(senders) > 0 {
		states, err = p.elClient.AccountStatesAt(ctx, senders, params.ParentHash, parent.Number.Uint64())
		if err != nil {
			return nil, fmt.Errorf("select: %w", err)
		}
	}

	stale := make([]common.Hash, 0, 16)
	ready := make(map[common.Address][]*PooledTx, len(senders))

	for _, sender := range senders {
		state := states[sender]
		if state == nil {
			continue
		}

		ready[sender] = p.readyRun(sel, params, queues[sender], state, &stale)
	}

	p.evictStale(ctx, stale)

	selected := p.mergeHeads(sel, params, senders, ready, maxBlockBytes)

	sel.Selected = selected

	fill, err := p.selectFill(ctx, sel, params, parent, states)
	if err != nil {
		return nil, err
	}

	sel.Fill = fill

	encoded := make([][]byte, 0, len(selected)+len(fill))

	for _, tx := range append(append([]*PooledTx(nil), selected...), fill...) {
		raw, err := EncodeTx(tx.Tx, params.BlobEncoding)
		if err != nil {
			return nil, fmt.Errorf("select: encode %s: %w", tx.Hash.Hex(), err)
		}

		encoded = append(encoded, raw)
	}

	sel.Txs = p.prependInclusionList(sel, encoded, params.InclusionList)

	return sel, nil
}

// SelectQueued assembles exactly the given queued transactions in the given
// order. It is a contract, never a fill: every hash must be queued, each
// sender's transactions must appear in nonce order starting at the sender's
// parent-state nonce, every transaction must clear the fee floors and its
// sender's balance, and the list must fit the block's gas and blob caps (the
// fill percentage does not apply). Any deviation is an error and nothing is
// trimmed.
func (p *Pool) SelectQueued(ctx context.Context, hashes []common.Hash, params *SelectParams) (*Selection, error) {
	if len(hashes) == 0 {
		return nil, errors.New("select queued: empty transaction list")
	}

	if !p.enabled.Load() {
		return nil, ErrDisabled
	}

	entries := make([]*PooledTx, len(hashes))
	senders := make([]common.Address, 0, len(hashes))
	seen := make(map[common.Address]struct{}, len(hashes))

	for i, hash := range hashes {
		tx := p.Get(hash)
		if tx == nil {
			return nil, fmt.Errorf("select queued: tx %d (%s) is not queued", i, hash.Hex())
		}

		entries[i] = tx

		if _, ok := seen[tx.Sender]; !ok {
			seen[tx.Sender] = struct{}{}
			senders = append(senders, tx.Sender)
		}
	}

	parent, err := p.elClient.HeaderByHash(ctx, params.ParentHash)
	if err != nil {
		return nil, fmt.Errorf("select queued: %w", err)
	}

	sel := &Selection{
		Skipped:  make(map[string]int, 8),
		Snapshot: time.Now(),
		BaseFee:  calcNextBaseFee(parent),
		PoolSize: p.Stats().Pending,
	}

	sel.GasBudget = params.GasLimit
	if sel.GasBudget == 0 {
		sel.GasBudget = parent.GasLimit
	}

	if params.IncludeBlobTxs {
		for _, tx := range entries {
			if tx.Tx.Type() == types.BlobTxType {
				blobFee, err := p.elClient.BlobBaseFee(ctx)
				if err != nil {
					return nil, fmt.Errorf("select queued: %w", err)
				}

				sel.BlobBaseFee = blobFee

				break
			}
		}
	}

	states, err := p.elClient.AccountStatesAt(ctx, senders, params.ParentHash, parent.Number.Uint64())
	if err != nil {
		return nil, fmt.Errorf("select queued: %w", err)
	}

	expected := make(map[common.Address]uint64, len(senders))
	balance := make(map[common.Address]*big.Int, len(senders))

	for _, sender := range senders {
		state := states[sender]
		if state == nil {
			return nil, fmt.Errorf("select queued: no parent state for sender %s", sender.Hex())
		}

		expected[sender] = state.Nonce
		balance[sender] = new(big.Int).Set(state.Balance)
	}

	var gasUsed uint64

	blobs := 0
	encoded := make([][]byte, 0, len(entries))

	for i, tx := range entries {
		nonce := tx.Tx.Nonce()
		if want := expected[tx.Sender]; nonce != want {
			return nil, fmt.Errorf("select queued: tx %d (%s) has nonce %d, sender %s expects %d at this position",
				i, tx.Hash.Hex(), nonce, tx.Sender.Hex(), want)
		}

		if tx.Tx.GasFeeCap().Cmp(sel.BaseFee) < 0 {
			return nil, fmt.Errorf("select queued: tx %d (%s) does not clear the base fee %s", i, tx.Hash.Hex(), sel.BaseFee)
		}

		txBlobs := len(tx.Tx.BlobHashes())
		if txBlobs > 0 {
			if !params.IncludeBlobTxs {
				return nil, fmt.Errorf("select queued: tx %d (%s) is a blob transaction the EL cannot bundle", i, tx.Hash.Hex())
			}

			if params.BlobEncoding != config.BlobEncodingCanonical && !sidecarFits(tx.Tx, params.CellProofs) {
				return nil, fmt.Errorf("select queued: tx %d (%s) has a blob sidecar of the wrong proof format for this fork",
					i, tx.Hash.Hex())
			}

			if sel.BlobBaseFee != nil && tx.Tx.BlobGasFeeCap().Cmp(sel.BlobBaseFee) < 0 {
				return nil, fmt.Errorf("select queued: tx %d (%s) does not clear the blob base fee", i, tx.Hash.Hex())
			}

			if uint64(blobs+txBlobs) > params.MaxBlobs {
				return nil, fmt.Errorf("select queued: tx %d (%s) exceeds the blob cap %d", i, tx.Hash.Hex(), params.MaxBlobs)
			}
		}

		if gasUsed+tx.Tx.Gas() > sel.GasBudget {
			return nil, fmt.Errorf("select queued: tx %d (%s) does not fit the gas limit %d", i, tx.Hash.Hex(), sel.GasBudget)
		}

		if balance[tx.Sender].Cmp(tx.Tx.Cost()) < 0 {
			return nil, fmt.Errorf("select queued: tx %d (%s): sender %s cannot cover the cost", i, tx.Hash.Hex(), tx.Sender.Hex())
		}

		raw, err := EncodeTx(tx.Tx, params.BlobEncoding)
		if err != nil {
			return nil, fmt.Errorf("select queued: encode %s: %w", tx.Hash.Hex(), err)
		}

		balance[tx.Sender].Sub(balance[tx.Sender], tx.Tx.Cost())
		expected[tx.Sender]++
		gasUsed += tx.Tx.Gas()
		blobs += txBlobs
		encoded = append(encoded, raw)
		sel.Selected = append(sel.Selected, tx)
	}

	sel.GasSum = gasUsed
	sel.Blobs = blobs
	sel.Txs = p.prependInclusionList(sel, encoded, params.InclusionList)

	return sel, nil
}

// readyRun walks one sender's queue from the account nonce and returns the
// contiguous, affordable, fee-clearing prefix. Everything after the first
// break is skipped with the reason of the break (later nonces cannot execute
// without it).
func (p *Pool) readyRun(
	sel *Selection,
	params *SelectParams,
	queue []*PooledTx,
	state *execution.AccountState,
	stale *[]common.Hash,
) []*PooledTx {
	nonce := state.Nonce
	balance := new(big.Int).Set(state.Balance)
	run := make([]*PooledTx, 0, len(queue))

	for i, tx := range queue {
		txNonce := tx.Nonce()

		if txNonce < nonce {
			sel.Skipped[SkipNonceTooLow]++
			*stale = append(*stale, tx.Hash)

			continue
		}

		reason := ""

		switch {
		case txNonce > nonce:
			reason = SkipNonceGap
		case tx.Cold() || tx.GasFeeCap().Cmp(sel.BaseFee) < 0:
			// A cold transaction is far below the base fee by definition.
			reason = SkipFeeBelowBase
		case tx.Tx.Type() == types.BlobTxType && !params.IncludeBlobTxs:
			reason = SkipBlobUnsupported
		case params.BlobEncoding != config.BlobEncodingCanonical && !sidecarFits(tx.Tx, params.CellProofs):
			reason = SkipBlobSidecarVersion
		case tx.Tx.Type() == types.BlobTxType && sel.BlobBaseFee != nil &&
			tx.Tx.BlobGasFeeCap().Cmp(sel.BlobBaseFee) < 0:
			reason = SkipBlobFeeBelowBase
		case balance.Cmp(tx.Tx.Cost()) < 0:
			reason = SkipInsufficientBalance
		}

		if reason != "" {
			sel.Skipped[reason] += len(queue) - i

			break
		}

		balance.Sub(balance, tx.Tx.Cost())
		nonce++
		run = append(run, tx)
	}

	return run
}

// mergeHeads interleaves the senders' ready runs in the requested ordering
// under the gas, blob and count budgets.
func (p *Pool) mergeHeads(
	sel *Selection,
	params *SelectParams,
	senders []common.Address,
	ready map[common.Address][]*PooledTx,
	byteBudget uint64,
) []*PooledTx {
	type cursor struct {
		txs []*PooledTx
		pos int
	}

	cursors := make([]*cursor, 0, len(senders))

	for _, sender := range senders {
		if txs := ready[sender]; len(txs) > 0 {
			cursors = append(cursors, &cursor{txs: txs})
		}
	}

	// Sender order by first arrival: what fifo ties and round robin walk.
	sort.SliceStable(cursors, func(i, j int) bool { return cursors[i].txs[0].Seq < cursors[j].txs[0].Seq })

	ordering := config.NormalizedTxOrdering(params.Ordering, config.TxOrderingFIFO)
	rng := rand.New(rand.NewPCG(params.Seed, params.Seed^0x9e3779b97f4a7c15)) //nolint:gosec // deterministic test ordering, not security

	selected := make([]*PooledTx, 0, 64)

	var gasUsed, bytesUsed uint64

	blobs := 0
	rrNext := 0

	pick := func() int {
		switch ordering {
		case config.TxOrderingRoundRobin:
			// One transaction per sender per round, senders in the order of
			// their first arrival; cursors are appended in sender order.
			if rrNext >= len(cursors) {
				rrNext = 0
			}

			i := rrNext
			rrNext++

			return i
		case config.TxOrderingTip:
			best := 0

			for i := 1; i < len(cursors); i++ {
				a := cursors[i].txs[cursors[i].pos]
				b := cursors[best].txs[cursors[best].pos]

				if effectiveTip(a.Tx, sel.BaseFee).Cmp(effectiveTip(b.Tx, sel.BaseFee)) > 0 {
					best = i
				}
			}

			return best
		case config.TxOrderingRandom:
			return rng.IntN(len(cursors))
		default:
			best := 0

			for i := 1; i < len(cursors); i++ {
				if cursors[i].txs[cursors[i].pos].Seq < cursors[best].txs[cursors[best].pos].Seq {
					best = i
				}
			}

			return best
		}
	}

	dropCursor := func(i int, reason string) {
		sel.Skipped[reason] += len(cursors[i].txs) - cursors[i].pos
		cursors = append(cursors[:i], cursors[i+1:]...)

		if i < rrNext {
			rrNext--
		}
	}

	for len(cursors) > 0 {
		if params.MaxTxs > 0 && uint64(len(selected)) >= params.MaxTxs {
			for len(cursors) > 0 {
				dropCursor(0, SkipMaxTxs)
			}

			break
		}

		i := pick()
		cur := cursors[i]
		tx := cur.txs[cur.pos]

		if gasUsed+tx.Tx.Gas() > sel.GasBudget {
			dropCursor(i, SkipGasFull)

			continue
		}

		if bytesUsed+tx.Size > byteBudget {
			dropCursor(i, SkipSizeFull)

			continue
		}

		txBlobs := len(tx.Tx.BlobHashes())
		if txBlobs > 0 && uint64(blobs+txBlobs) > params.MaxBlobs {
			dropCursor(i, SkipBlobFull)

			continue
		}

		gasUsed += tx.Tx.Gas()
		bytesUsed += tx.Size
		blobs += txBlobs
		selected = append(selected, tx)
		cur.pos++

		if cur.pos >= len(cur.txs) {
			cursors = append(cursors[:i], cursors[i+1:]...)

			if i < rrNext {
				rrNext--
			}
		}
	}

	sel.GasSum = gasUsed
	sel.Bytes = bytesUsed
	sel.Blobs = blobs

	return selected
}

// SkipDuplicate marks an EL mempool fill transaction that is also queued in
// the pool (the pool selection decides about it).
const SkipDuplicate = "duplicate"

// selectFill places the EL mempool's pending transactions (params.Fill) after
// the pool selection, within the gas, byte, count and blob budgets it left.
// Each sender continues at the nonce and balance the pool selection left,
// the same parent-state checks apply, and the highest effective tip goes
// first, like an EL's own block building. txpool_content lists blob
// transactions without their blobs: the network encoding takes them only
// when params.FillSidecars completes them, which is asked for the selected
// ones alone (see completeFillBlobs).
func (p *Pool) selectFill(
	ctx context.Context,
	sel *Selection,
	params *SelectParams,
	parent *types.Header,
	states map[common.Address]*execution.AccountState,
) ([]*PooledTx, error) {
	sel.FillCandidates = len(params.Fill)
	sel.FillSkipped = make(map[string]int, 4)

	p.mirror.noteBaseFee(sel.BaseFee)

	if len(params.Fill) == 0 {
		return nil, nil
	}

	fillParams := *params
	fillParams.Ordering = config.TxOrderingTip
	fillParams.IncludeBlobTxs = fillCarriesBlobs(params)
	fillParams.MaxBlobs = 0

	if fillParams.IncludeBlobTxs {
		fillParams.MaxBlobs = params.MaxBlobs - min(uint64(sel.Blobs), params.MaxBlobs)
	}

	if params.MaxTxs > 0 {
		if uint64(len(sel.Selected)) >= params.MaxTxs {
			sel.FillSkipped[SkipMaxTxs] = len(params.Fill)

			return nil, nil
		}

		fillParams.MaxTxs = params.MaxTxs - uint64(len(sel.Selected))
	}

	queues := make(map[common.Address][]*PooledTx, 64)

	for _, tx := range params.Fill {
		if p.Get(tx.Hash) != nil {
			sel.FillSkipped[SkipDuplicate]++

			continue
		}

		// Most of a busy EL mempool sits below the base fee. Dropped before
		// any sender's state is read, which is the per-build cost that
		// scales with the mempool; a sender's later nonces then fail on
		// the gap.
		if tx.Tx.GasFeeCap().Cmp(sel.BaseFee) < 0 {
			sel.FillSkipped[SkipFeeBelowBase]++

			continue
		}

		queues[tx.Sender] = append(queues[tx.Sender], tx)
	}

	senders := make([]common.Address, 0, len(queues))
	missing := make([]common.Address, 0, len(queues))

	for sender := range queues {
		senders = append(senders, sender)

		if states[sender] == nil {
			missing = append(missing, sender)
		}
	}

	sort.Slice(senders, func(i, j int) bool { return senders[i].Cmp(senders[j]) < 0 })

	if len(missing) > 0 {
		fetched, err := p.elClient.AccountStatesAt(ctx, missing, params.ParentHash, parent.Number.Uint64())
		if err != nil {
			return nil, fmt.Errorf("select fill: %w", err)
		}

		for sender, state := range fetched {
			states[sender] = state
		}
	}

	// What the pool selection already spent per sender.
	spentNonces := make(map[common.Address]uint64, len(sel.Selected))
	spentValue := make(map[common.Address]*big.Int, len(sel.Selected))

	for _, tx := range sel.Selected {
		spentNonces[tx.Sender]++

		if spentValue[tx.Sender] == nil {
			spentValue[tx.Sender] = new(big.Int)
		}

		spentValue[tx.Sender].Add(spentValue[tx.Sender], tx.Tx.Cost())
	}

	fillSel := &Selection{
		Skipped:     sel.FillSkipped,
		BaseFee:     sel.BaseFee,
		BlobBaseFee: sel.BlobBaseFee,
		GasBudget:   sel.GasBudget - min(sel.GasSum, sel.GasBudget),
	}

	ready := make(map[common.Address][]*PooledTx, len(senders))
	ignored := make([]common.Hash, 0)

	for _, sender := range senders {
		state := states[sender]
		if state == nil {
			continue
		}

		remaining := &execution.AccountState{
			Nonce:   state.Nonce + spentNonces[sender],
			Balance: new(big.Int).Set(state.Balance),
		}

		if spent := spentValue[sender]; spent != nil {
			remaining.Balance.Sub(remaining.Balance, spent)
		}

		// Nonce-too-low fill transactions belong to the EL's pool: never
		// evict anything of ours for them, only the mirror's stale copies.
		ready[sender] = p.readyRun(fillSel, &fillParams, queues[sender], remaining, &ignored)
	}

	p.mirror.dropStale(ignored)

	fill := p.mergeHeads(fillSel, &fillParams, senders, ready, maxBlockBytes-min(sel.Bytes, maxBlockBytes))

	if fillParams.IncludeBlobTxs && params.BlobEncoding != config.BlobEncodingCanonical {
		fill = p.completeFillBlobs(ctx, sel, params.FillSidecars, fill)
	}

	sel.FillGasSum = 0
	sel.FillBytes = 0
	sel.FillBlobs = 0

	for _, tx := range fill {
		sel.FillGasSum += tx.Tx.Gas()
		sel.FillBytes += tx.Size
		sel.FillBlobs += len(tx.Tx.BlobHashes())
	}

	return fill, nil
}

// fillCarriesBlobs reports whether the fill may select blob transactions:
// blobs are allowed for the block, and the encoding either needs no sidecar
// (canonical) or a source can supply it.
func fillCarriesBlobs(params *SelectParams) bool {
	return params.IncludeBlobTxs &&
		(params.BlobEncoding == config.BlobEncodingCanonical || params.FillSidecars != nil)
}

// completeFillBlobs attaches the sidecars of the selected fill blob
// transactions. One the source cannot complete is dropped, and the same
// sender's later fill transactions with it: they would follow a nonce gap.
// The blob space it frees is not refilled.
func (p *Pool) completeFillBlobs(
	ctx context.Context,
	sel *Selection,
	source SidecarSource,
	fill []*PooledTx,
) []*PooledTx {
	blobTxs := make([]*types.Transaction, 0, len(fill))

	for _, tx := range fill {
		if tx.Tx.Type() == types.BlobTxType {
			blobTxs = append(blobTxs, tx.Tx)
		}
	}

	if len(blobTxs) == 0 {
		return fill
	}

	sidecars, err := source.BlobSidecars(ctx, blobTxs)
	if err != nil {
		p.log.WithError(err).WithField("blob_txs", len(blobTxs)).
			Debug("Cannot read the EL blob pool, leaving the EL mempool blob transactions out")
	}

	kept := make([]*PooledTx, 0, len(fill))
	dropped := make(map[common.Address]bool)

	for _, tx := range fill {
		if dropped[tx.Sender] {
			sel.FillSkipped[SkipNonceGap]++

			continue
		}

		if tx.Tx.Type() != types.BlobTxType {
			kept = append(kept, tx)

			continue
		}

		sidecar := sidecars[tx.Hash]
		if sidecar == nil {
			sel.FillSkipped[SkipBlobUnavailable]++
			dropped[tx.Sender] = true

			continue
		}

		kept = append(kept, &PooledTx{
			Hash:   tx.Hash,
			Tx:     tx.Tx.WithBlobTxSidecar(sidecar),
			Sender: tx.Sender,
			Size:   tx.Size,
			Seq:    tx.Seq,
		})
	}

	return kept
}

func hasBlobTx(txs []*PooledTx) bool {
	for _, tx := range txs {
		if tx.Tx.Type() == types.BlobTxType {
			return true
		}
	}

	return false
}

// effectiveTip is the priority fee a transaction actually pays over the base
// fee: min(tipCap, feeCap - baseFee).
func effectiveTip(tx *types.Transaction, baseFee *big.Int) *big.Int {
	tip := new(big.Int).Sub(tx.GasFeeCap(), baseFee)
	if tip.Sign() < 0 {
		tip.SetInt64(0)
	}

	if tip.Cmp(tx.GasTipCap()) > 0 {
		return new(big.Int).Set(tx.GasTipCap())
	}

	return tip
}

// EncodeTx encodes a transaction for the testing call: the network form keeps
// a blob transaction's sidecar, the canonical form strips it.
func EncodeTx(tx *types.Transaction, blobEncoding string) ([]byte, error) {
	if tx.Type() == types.BlobTxType && blobEncoding == config.BlobEncodingCanonical {
		return tx.WithoutBlobTxSidecar().MarshalBinary()
	}

	return tx.MarshalBinary()
}

// prependInclusionList places the inclusion-list transactions ahead of the
// selection and accounts them.
func (p *Pool) prependInclusionList(sel *Selection, selected [][]byte, inclusionList [][]byte) [][]byte {
	sel.InclusionListTxs = len(inclusionList)

	out := make([][]byte, 0, len(inclusionList)+len(selected))
	out = append(out, inclusionList...)
	out = append(out, selected...)

	return out
}

func (p *Pool) hasBlobTxs(queues map[common.Address][]*PooledTx) bool {
	for _, txs := range queues {
		for _, tx := range txs {
			if tx.Type() == types.BlobTxType {
				return true
			}
		}
	}

	return false
}

// evictStale drops transactions whose nonce is already used on chain. The
// selection usually sees them before the eviction loop does (the next slot's
// build runs before the previous block's payload is imported and processed),
// so the EL is asked which block included each of them to keep the
// included-by-us / included-by-other accounting right; a transaction the EL
// does not know was replaced by another one at its nonce. When the lookup
// fails nothing is evicted: the selection keeps skipping the stale
// transactions, and the next one (or the head-driven nonce pruning) retries
// instead of counting all of them as replaced.
func (p *Pool) evictStale(ctx context.Context, hashes []common.Hash) {
	if len(hashes) == 0 {
		return
	}

	included, err := p.elClient.TransactionBlockHashes(ctx, hashes)
	if err != nil {
		p.log.WithError(err).WithField("txs", len(hashes)).
			Debug("Cannot classify stale pool transactions, keeping them for the next attempt")

		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	for _, hash := range hashes {
		if !p.removeLocked(hash) {
			continue
		}

		switch block := included[hash]; {
		case block == (common.Hash{}):
			p.stats.EvictedNonceTooLow++
			p.evictedLocked("nonce_too_low")
		case p.isBuiltBlock(block):
			p.stats.EvictedIncludedByUs++
			p.evictedLocked("included_by_us")
		default:
			p.stats.EvictedIncludedByOther++
			p.evictedLocked("included_by_other")
		}
	}

	p.version.Add(1)
}
