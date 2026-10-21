package inclusionlist

import (
	"context"
	"testing"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

var genesis = time.Unix(1_700_000_000, 0)

// stubChain implements the parts of chain.Service the store reads.
type stubChain struct {
	chain.Service
	committee []phase0.ValidatorIndex
}

func (s *stubChain) GetChainSpec() *chain.ChainSpec {
	return &chain.ChainSpec{
		SlotDuration:               12 * time.Second,
		SlotsPerEpoch:              32,
		MinSeedLookahead:           1,
		InclusionListCommitteeSize: 16,
		InclusionListDueBps:        6667,
	}
}

func (s *stubChain) SlotToTime(slot phase0.Slot) time.Time {
	return genesis.Add(time.Duration(slot) * 12 * time.Second)
}

func (s *stubChain) GetInclusionListCommittee(phase0.Slot) []phase0.ValidatorIndex {
	return s.committee
}

func (s *stubChain) GetHeadTracker() *chain.HeadTracker { return nil }

func newTestStore(committee ...phase0.ValidatorIndex) *Store {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	return NewStore(&stubChain{committee: committee}, log)
}

func list(slot phase0.Slot, member phase0.ValidatorIndex, at time.Duration, txs ...string) *beacon.InclusionListEvent {
	encoded := make([][]byte, len(txs))
	for i, tx := range txs {
		encoded[i] = []byte(tx)
	}

	return &beacon.InclusionListEvent{
		Slot:           slot,
		ValidatorIndex: member,
		DependentRoot:  phase0.Root{0xd1},
		Transactions:   encoded,
		ReceivedAt:     genesis.Add(time.Duration(slot)*12*time.Second + at),
	}
}

func txStrings(txs [][]byte) []string {
	out := make([]string, len(txs))
	for i, tx := range txs {
		out[i] = string(tx)
	}

	return out
}

func TestViewMergesListsInCommitteeOrder(t *testing.T) {
	// Committee of 16 positions over three members (wrapping around).
	committee := make([]phase0.ValidatorIndex, 16)
	for i := range committee {
		committee[i] = phase0.ValidatorIndex(10 + i%3)
	}

	store := newTestStore(committee...)

	store.Add(list(100, 11, time.Second, "b", "shared"))
	store.Add(list(100, 10, 2*time.Second, "a", "shared"))
	store.Add(list(100, 12, 9*time.Second, "late")) // after the 8 s deadline

	view := store.View(context.Background(), 100, phase0.Root{})
	require.Equal(t, phase0.Root{0xd1}, view.DependentRoot)
	require.Equal(t, []string{"a", "shared", "b", "late"}, txStrings(view.Transactions), "committee order, deduplicated")
	require.Equal(t, 3, view.Lists)
	require.Equal(t, 2, view.Timely, "the builder view keeps untimely lists, flagged")
	require.Equal(t, []byte{0xff, 0xff}, view.Bits, "every position of the three members")
}

func TestEquivocatorsAreIgnored(t *testing.T) {
	store := newTestStore(10, 11)

	store.Add(list(100, 10, time.Second, "a"))
	store.Add(list(100, 11, time.Second, "b"))

	before := store.Version()

	store.Add(list(100, 11, time.Second, "b")) // the same list again
	require.Equal(t, before, store.Version(), "a repeated list changes nothing")

	store.Add(list(100, 11, 2*time.Second, "c")) // a different one
	require.Greater(t, store.Version(), before)

	view := store.View(context.Background(), 100, phase0.Root{})
	require.Equal(t, []string{"a"}, txStrings(view.Transactions))
	require.Equal(t, 1, view.Equivocators)
	require.Equal(t, []byte{0x01}, view.Bits, "the equivocator's position stays unset")
}

func TestViewWithoutLists(t *testing.T) {
	store := newTestStore(10, 11)

	view := store.View(context.Background(), 100, phase0.Root{})
	require.True(t, view.Empty())
	require.Equal(t, []byte{0x00}, view.Bits, "known committee, no list seen")
}

func TestAmbiguousDependentRootNeedsTheChain(t *testing.T) {
	store := newTestStore(10, 11)

	store.Add(list(100, 10, time.Second, "a"))

	other := list(100, 11, time.Second, "b")
	other.DependentRoot = phase0.Root{0xd2}
	store.Add(other)

	// Two branches' lists and no chain view to tell them apart.
	require.True(t, store.View(context.Background(), 100, phase0.Root{}).Empty())
}

func TestPruneKeepsRecentSlots(t *testing.T) {
	store := newTestStore(10)

	store.Add(list(100, 10, time.Second, "old"))
	store.Add(list(100+retentionSlots+1, 10, time.Second, "new"))

	require.True(t, store.View(context.Background(), 100, phase0.Root{}).Empty())
	require.False(t, store.View(context.Background(), 100+retentionSlots+1, phase0.Root{}).Empty())
}

func TestMergeTransactions(t *testing.T) {
	merged := MergeTransactions([][]byte{[]byte("a"), []byte("b")}, [][]byte{[]byte("b"), []byte("c")})
	require.Equal(t, []string{"a", "b", "c"}, txStrings(merged))
}
