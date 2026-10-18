package txpool

import (
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
)

// Inclusion list skip reasons beyond the pool's. A list is satisfied when
// every transaction left out would be invalid appended to the block (EIP-7805),
// so each skip records why the transaction could not go in.
const (
	SkipUndecodable = "undecodable"
	// SkipBlobInList: a blob transaction in an inclusion list carries no
	// sidecar, so the block cannot take it.
	SkipBlobInList = "blob_transaction"
	// SkipInInclusionList: a pool or fill transaction displaced by an
	// inclusion list transaction (the same one, or another at its nonce).
	SkipInInclusionList = "in_inclusion_list"
)

// inclusionPlan is the inclusion list's placement in a selection: the
// transactions at the front of the block, the ones placed after the pool's
// (their sender's nonce gap was filled by a pool transaction), and per
// sender the nonce and balance the list leaves for the pool.
type inclusionPlan struct {
	head     []*PooledTx
	deferred []*PooledTx // nonce gap at the head, retried after the pool
	hashes   map[common.Hash]struct{}
	nonce    map[common.Address]uint64
	balance  map[common.Address]*big.Int
}

// decodeInclusionList decodes and sender-recovers the list, skipping
// undecodable, duplicate and blob transactions.
func (p *Pool) decodeInclusionList(sel *Selection, raw [][]byte) []*PooledTx {
	if len(raw) == 0 {
		return nil
	}

	p.mu.RLock()
	signer := p.signer
	p.mu.RUnlock()

	out := make([]*PooledTx, 0, len(raw))
	seen := make(map[common.Hash]struct{}, len(raw))

	for i, encoded := range raw {
		tx := new(types.Transaction)
		if err := tx.UnmarshalBinary(encoded); err != nil {
			sel.InclusionListSkipped[SkipUndecodable]++

			continue
		}

		if _, dup := seen[tx.Hash()]; dup {
			continue
		}

		seen[tx.Hash()] = struct{}{}

		if tx.Type() == types.BlobTxType {
			sel.InclusionListSkipped[SkipBlobInList]++

			continue
		}

		sender, err := types.Sender(signer, tx)
		if err != nil {
			sel.InclusionListSkipped[SkipUndecodable]++

			continue
		}

		out = append(out, &PooledTx{
			Hash:   tx.Hash(),
			Sender: sender,
			Tx:     tx,
			Size:   uint64(len(encoded)),
			Seq:    uint64(i), // list order
		})
	}

	return out
}

// placeInclusionList checks the list against the parent state, front of the
// block first: per sender in nonce order, each transaction must continue the
// sender's nonce, clear the base fee and be covered by the balance. A nonce
// gap is deferred (a pool transaction may fill it), anything else left out
// is recorded with its reason. The placed transactions use up gas, bytes and
// count before the pool's.
func (p *Pool) placeInclusionList(
	sel *Selection,
	params *SelectParams,
	txs []*PooledTx,
	states map[common.Address]*execution.AccountState,
) *inclusionPlan {
	plan := &inclusionPlan{
		hashes:  make(map[common.Hash]struct{}, len(txs)),
		nonce:   make(map[common.Address]uint64, 8),
		balance: make(map[common.Address]*big.Int, 8),
	}

	// Per sender in nonce order, senders in the order the list names them.
	ordered := append([]*PooledTx(nil), txs...)
	firstSeen := make(map[common.Address]uint64, len(txs))

	for _, tx := range ordered {
		if _, ok := firstSeen[tx.Sender]; !ok {
			firstSeen[tx.Sender] = tx.Seq
		}
	}

	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Sender != b.Sender {
			return firstSeen[a.Sender] < firstSeen[b.Sender]
		}

		return a.Tx.Nonce() < b.Tx.Nonce()
	})

	for _, tx := range ordered {
		plan.hashes[tx.Hash] = struct{}{}

		state := states[tx.Sender]
		if state == nil {
			state = &execution.AccountState{Balance: new(big.Int)}
		}

		if _, ok := plan.nonce[tx.Sender]; !ok {
			plan.nonce[tx.Sender] = state.Nonce
			plan.balance[tx.Sender] = new(big.Int).Set(state.Balance)
		}

		switch nonce := tx.Tx.Nonce(); {
		case nonce < plan.nonce[tx.Sender]:
			sel.InclusionListSkipped[SkipNonceTooLow]++
		case nonce > plan.nonce[tx.Sender]:
			plan.deferred = append(plan.deferred, tx)
		default:
			if reason := p.fitInclusionTx(sel, params, tx, plan.balance[tx.Sender], len(plan.head)); reason != "" {
				sel.InclusionListSkipped[reason]++

				continue
			}

			plan.place(sel, tx)
			plan.head = append(plan.head, tx)
		}
	}

	return plan
}

// placeDeferred retries the deferred transactions after the pool selection:
// one whose nonce the pool's transactions of its sender reached now fits.
// Returns the placed ones (block order: after the pool's).
func (p *Pool) placeDeferred(sel *Selection, params *SelectParams, plan *inclusionPlan) []*PooledTx {
	if len(plan.deferred) == 0 {
		return nil
	}

	// Where the pool selection left each sender.
	nonce := make(map[common.Address]uint64, len(plan.nonce))
	for sender, n := range plan.nonce {
		nonce[sender] = n
	}

	balance := make(map[common.Address]*big.Int, len(plan.balance))
	for sender, b := range plan.balance {
		balance[sender] = new(big.Int).Set(b)
	}

	for _, tx := range sel.Selected {
		if _, ok := nonce[tx.Sender]; ok {
			nonce[tx.Sender]++
			balance[tx.Sender].Sub(balance[tx.Sender], tx.Tx.Cost())
		}
	}

	placed := make([]*PooledTx, 0, len(plan.deferred))

	for _, tx := range plan.deferred {
		if tx.Tx.Nonce() != nonce[tx.Sender] {
			sel.InclusionListSkipped[SkipNonceGap]++

			continue
		}

		if reason := p.fitInclusionTx(sel, params, tx, balance[tx.Sender], len(sel.Selected)+len(plan.head)+len(placed)); reason != "" {
			sel.InclusionListSkipped[reason]++

			continue
		}

		nonce[tx.Sender]++
		balance[tx.Sender].Sub(balance[tx.Sender], tx.Tx.Cost())
		sel.GasSum += tx.Tx.Gas()
		sel.Bytes += tx.Size
		placed = append(placed, tx)
	}

	return placed
}

// fitInclusionTx returns why the transaction cannot be placed ("" = it
// fits): fee floor, balance, and the selection's remaining budgets.
func (p *Pool) fitInclusionTx(sel *Selection, params *SelectParams, tx *PooledTx, balance *big.Int, placed int) string {
	switch {
	case tx.Tx.GasFeeCap().Cmp(sel.BaseFee) < 0:
		return SkipFeeBelowBase
	case balance.Cmp(tx.Tx.Cost()) < 0:
		return SkipInsufficientBalance
	case sel.GasSum+tx.Tx.Gas() > sel.GasBudget:
		return SkipGasFull
	case sel.Bytes+tx.Size > maxBlockBytes:
		return SkipSizeFull
	case params.MaxTxs > 0 && uint64(placed) >= params.MaxTxs:
		return SkipMaxTxs
	}

	return ""
}

// place accounts a transaction placed at the front of the block.
func (plan *inclusionPlan) place(sel *Selection, tx *PooledTx) {
	plan.nonce[tx.Sender]++
	plan.balance[tx.Sender].Sub(plan.balance[tx.Sender], tx.Tx.Cost())
	sel.GasSum += tx.Tx.Gas()
	sel.Bytes += tx.Size
}

// senderState is the state the pool selection starts a sender at: after its
// inclusion list transactions, when it has any.
func (plan *inclusionPlan) senderState(sender common.Address, state *execution.AccountState) *execution.AccountState {
	if plan == nil {
		return state
	}

	nonce, ok := plan.nonce[sender]
	if !ok {
		return state
	}

	return &execution.AccountState{Nonce: nonce, Balance: new(big.Int).Set(plan.balance[sender])}
}
