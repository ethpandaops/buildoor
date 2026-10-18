// Package inclusionlist keeps the inclusion lists (FOCIL, EIP-7805, Heze) the
// beacon node gossips and serves the builder's inclusion list view: the
// transactions a payload must carry and the committee bits a bid claims.
package inclusionlist

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// retentionSlots is how long lists are kept after their slot: a payload of
// slot N needs the lists of slot N-1, a late or re-bound build a few more.
const retentionSlots = 8

// listKey groups lists like the spec's InclusionListStore: by slot and the
// shuffling dependent root of the committee that produced them.
type listKey struct {
	slot phase0.Slot
	root phase0.Root
}

// entry is one committee member's list.
type entry struct {
	txs        [][]byte
	digest     [32]byte // identifies the list, to recognise an equivocation
	timely     bool
	receivedAt time.Time
}

// Store keeps the gossiped inclusion lists per (slot, dependent root): one
// list per committee member, a member that published two different lists is
// an equivocator and ignored (process_inclusion_list). Lists are kept
// whether timely or not: the builder's view (get_inclusion_list_bits with
// only_timely=False) covers every list observed.
type Store struct {
	log      logrus.FieldLogger
	chainSvc chain.Service

	mu           sync.RWMutex
	lists        map[listKey]map[phase0.ValidatorIndex]*entry
	equivocators map[listKey]map[phase0.ValidatorIndex]struct{}
	newest       phase0.Slot

	// version changes with every stored list: a build that could carry a new
	// list watches it.
	version atomic.Uint64
}

// NewStore creates an empty store.
func NewStore(chainSvc chain.Service, log logrus.FieldLogger) *Store {
	return &Store{
		log:          log.WithField("component", "inclusion-lists"),
		chainSvc:     chainSvc,
		lists:        make(map[listKey]map[phase0.ValidatorIndex]*entry, 4),
		equivocators: make(map[listKey]map[phase0.ValidatorIndex]struct{}, 4),
	}
}

// Start follows the beacon node's inclusion_list events until ctx ends.
func (s *Store) Start(ctx context.Context, events *beacon.EventStream) {
	sub := events.SubscribeInclusionLists()

	go func() {
		defer sub.Unsubscribe()

		for {
			select {
			case <-ctx.Done():
				return
			case event := <-sub.Channel():
				s.Add(event)
			}
		}
	}()
}

// Version changes whenever a list is stored or a member turns out to
// equivocate.
func (s *Store) Version() uint64 {
	return s.version.Load()
}

// Add stores a gossiped list. A list arriving within its slot before the
// inclusion list deadline is timely.
func (s *Store) Add(event *beacon.InclusionListEvent) {
	if event == nil {
		return
	}

	key := listKey{slot: event.Slot, root: event.DependentRoot}
	digest := listDigest(event.Transactions)

	s.mu.Lock()
	defer s.mu.Unlock()

	members := s.lists[key]
	if members == nil {
		members = make(map[phase0.ValidatorIndex]*entry, 16)
		s.lists[key] = members
	}

	if stored, ok := members[event.ValidatorIndex]; ok {
		if stored.digest != digest {
			if s.equivocators[key] == nil {
				s.equivocators[key] = make(map[phase0.ValidatorIndex]struct{}, 1)
			}

			if _, known := s.equivocators[key][event.ValidatorIndex]; !known {
				s.equivocators[key][event.ValidatorIndex] = struct{}{}
				s.version.Add(1)

				s.log.WithFields(logrus.Fields{
					"slot":      event.Slot,
					"validator": event.ValidatorIndex,
				}).Warn("Inclusion list equivocation, the member's lists are ignored")
			}
		}

		return
	}

	members[event.ValidatorIndex] = &entry{
		txs:        event.Transactions,
		digest:     digest,
		timely:     s.isTimely(event.Slot, event.ReceivedAt),
		receivedAt: event.ReceivedAt,
	}

	s.version.Add(1)

	if event.Slot > s.newest {
		s.newest = event.Slot
		s.pruneLocked()
	}
}

// isTimely reports whether a list arrived in its own slot before the
// inclusion list deadline.
func (s *Store) isTimely(slot phase0.Slot, receivedAt time.Time) bool {
	if s.chainSvc == nil || receivedAt.IsZero() {
		return true
	}

	spec := s.chainSvc.GetChainSpec()
	start := s.chainSvc.SlotToTime(slot)
	due := start.Add(spec.SecondsPerSlot * time.Duration(spec.InclusionListDueBps) / 10000)

	return !receivedAt.Before(start) && receivedAt.Before(due)
}

func (s *Store) pruneLocked() {
	if s.newest <= retentionSlots {
		return
	}

	cutoff := s.newest - retentionSlots

	for key := range s.lists {
		if key.slot < cutoff {
			delete(s.lists, key)
			delete(s.equivocators, key)
		}
	}
}

// listDigest identifies a list by its transactions.
func listDigest(txs [][]byte) [32]byte {
	parts := make([][]byte, 0, 2*len(txs))

	for _, tx := range txs {
		length := len(tx)
		parts = append(parts, []byte{byte(length >> 24), byte(length >> 16), byte(length >> 8), byte(length)}, tx)
	}

	return crypto.Keccak256Hash(parts...)
}
