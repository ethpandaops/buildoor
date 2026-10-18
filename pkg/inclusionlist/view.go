package inclusionlist

import (
	"context"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

// maxAncestorWalk bounds the parent walk resolving a dependent root (the
// dependent slot lies at most two epochs behind the inclusion list slot).
const maxAncestorWalk = 256

// View is the builder's inclusion list view for one payload: the lists of
// the slot before it, produced by the committee whose shuffling the payload's
// chain agrees on (get_inclusion_list_transactions / get_inclusion_list_bits
// with only_timely=False).
type View struct {
	// Slot is the inclusion list slot (the payload's slot - 1).
	Slot          phase0.Slot `json:"slot"`
	DependentRoot phase0.Root `json:"dependent_root"`
	// Transactions are the unique transactions of the non-equivocating
	// lists, in committee order.
	Transactions [][]byte `json:"-"`
	// Lists counts the non-equivocating lists, Timely those of them that
	// arrived before the deadline; Equivocators the ignored members.
	Lists        int `json:"lists"`
	Timely       int `json:"timely"`
	Equivocators int `json:"equivocators,omitempty"`
	// Bits marks the committee positions whose member's list is in the view
	// (an InclusionListBits bitvector, bit i = byte i/8, bit i%8); nil when
	// the committee is unknown.
	Bits []byte `json:"bits,omitempty"`
}

// Empty reports a view without any list.
func (v *View) Empty() bool {
	return v == nil || v.Lists == 0
}

// View returns the inclusion list view for a payload of slot+1 built on
// parentRoot. slot is the inclusion list slot.
func (s *Store) View(ctx context.Context, slot phase0.Slot, parentRoot phase0.Root) *View {
	view := &View{Slot: slot}

	root, ok := s.dependentRoot(ctx, slot, parentRoot)
	if !ok {
		view.Bits = s.bits(slot, nil)

		return view
	}

	view.DependentRoot = root

	s.mu.RLock()
	defer s.mu.RUnlock()

	key := listKey{slot: slot, root: root}
	members := s.lists[key]
	equivocators := s.equivocators[key]
	view.Equivocators = len(equivocators)

	included := make(map[phase0.ValidatorIndex]*entry, len(members))

	for index, list := range members {
		if _, equivocated := equivocators[index]; equivocated {
			continue
		}

		included[index] = list
		view.Lists++

		if list.timely {
			view.Timely++
		}
	}

	committee := s.committee(slot)
	view.Bits = s.bits(slot, included)

	// Committee order first (a member may hold several positions), then any
	// list whose member the committee view does not know.
	seen := make(map[string]struct{}, 16)
	ordered := make([]phase0.ValidatorIndex, 0, len(included))
	listed := make(map[phase0.ValidatorIndex]struct{}, len(included))

	for _, member := range committee {
		if _, ok := included[member]; ok {
			if _, dup := listed[member]; !dup {
				listed[member] = struct{}{}
				ordered = append(ordered, member)
			}
		}
	}

	for member := range included {
		if _, ok := listed[member]; !ok {
			ordered = append(ordered, member)
		}
	}

	for _, member := range ordered {
		for _, tx := range included[member].txs {
			if _, dup := seen[string(tx)]; dup {
				continue
			}

			seen[string(tx)] = struct{}{}
			view.Transactions = append(view.Transactions, tx)
		}
	}

	return view
}

// BitsLen is the byte length of an InclusionListBits bitvector.
func (s *Store) BitsLen() int {
	size := uint64(16)
	if s.chainSvc != nil && s.chainSvc.GetChainSpec().InclusionListCommitteeSize > 0 {
		size = s.chainSvc.GetChainSpec().InclusionListCommitteeSize
	}

	return int((size + 7) / 8)
}

// committee returns the slot's inclusion list committee (nil without a
// chain view).
func (s *Store) committee(slot phase0.Slot) []phase0.ValidatorIndex {
	if s.chainSvc == nil {
		return nil
	}

	return s.chainSvc.GetInclusionListCommittee(slot)
}

// bits builds the InclusionListBits for the given members' lists.
func (s *Store) bits(slot phase0.Slot, included map[phase0.ValidatorIndex]*entry) []byte {
	committee := s.committee(slot)
	if len(committee) == 0 {
		return nil
	}

	bits := make([]byte, (len(committee)+7)/8)

	for i, member := range committee {
		if _, ok := included[member]; ok {
			bits[i/8] |= 1 << (i % 8)
		}
	}

	return bits
}

// dependentRoot picks the dependent root the payload's chain agrees on: the
// only one the slot's lists carry, or else the parent chain's ancestor at the
// shuffling dependent slot (get_shuffling_dependent_root).
func (s *Store) dependentRoot(ctx context.Context, slot phase0.Slot, parentRoot phase0.Root) (phase0.Root, bool) {
	s.mu.RLock()

	roots := make([]phase0.Root, 0, 2)

	for key := range s.lists {
		if key.slot == slot {
			roots = append(roots, key.root)
		}
	}

	s.mu.RUnlock()

	switch len(roots) {
	case 0:
		return phase0.Root{}, false
	case 1:
		return roots[0], true
	}

	if s.chainSvc == nil {
		return phase0.Root{}, false
	}

	headTracker := s.chainSvc.GetHeadTracker()
	if headTracker == nil {
		return phase0.Root{}, false
	}

	spec := s.chainSvc.GetChainSpec()
	epoch := uint64(slot) / spec.SlotsPerEpoch

	var dependentSlot phase0.Slot
	if epoch > spec.MinSeedLookahead {
		dependentSlot = phase0.Slot((epoch-spec.MinSeedLookahead)*spec.SlotsPerEpoch - 1)
	}

	root := parentRoot

	for range maxAncestorWalk {
		block, err := headTracker.GetBlock(ctx, root)
		if err != nil || block == nil {
			return phase0.Root{}, false
		}

		if block.Slot <= dependentSlot {
			return block.Root, true
		}

		root = block.ParentRoot
	}

	return phase0.Root{}, false
}

// MergeTransactions appends the transactions of extra that are not in base.
func MergeTransactions(base, extra [][]byte) [][]byte {
	if len(extra) == 0 {
		return base
	}

	seen := make(map[string]struct{}, len(base))
	for _, tx := range base {
		seen[string(tx)] = struct{}{}
	}

	out := append([][]byte(nil), base...)

	for _, tx := range extra {
		if _, dup := seen[string(tx)]; dup {
			continue
		}

		seen[string(tx)] = struct{}{}
		out = append(out, tx)
	}

	return out
}
