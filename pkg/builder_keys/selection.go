package builder_keys

import (
	"math/rand/v2"
	"slices"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

// Key selection strategies: which of the ready keys a bid is signed with when
// more keys are ready than the slot needs.
const (
	// StrategyRoundRobin rotates through the ready keys by slot, spreading bids
	// and their payments evenly across the fleet.
	StrategyRoundRobin = "round_robin"
	// StrategySingle always uses the lowest-index ready key. One key bids per
	// slot, which is how a single-key deployment behaves.
	StrategySingle = "single"
	// StrategyRandom shuffles the ready keys per slot (deterministically, so a
	// slot's assignment is reproducible).
	StrategyRandom = "random"
	// StrategyLeastUsed prefers the keys that have bid least, which keeps
	// balance drain even when slots are not evenly distributed.
	StrategyLeastUsed = "least_used"
)

// NormalizedStrategy returns the strategy, falling back to round-robin for
// unknown values (config and plan overrides are free-form strings).
func NormalizedStrategy(strategy string) string {
	switch strategy {
	case StrategyRoundRobin, StrategySingle, StrategyRandom, StrategyLeastUsed:
		return strategy
	default:
		return StrategyRoundRobin
	}
}

// SelectRequest describes the keys a caller needs for one slot.
//
// A key may only bid once per slot: the gossip rules ignore every bid after a
// builder's first for a slot, so a second bid from the same key never
// propagates. Callers therefore ask for distinct keys and pass the ones they
// already committed in Exclude.
type SelectRequest struct {
	// Strategy is the selection strategy; unknown values fall back to
	// round-robin.
	Strategy string
	// RequiredGwei is the bid value a returned key should be able to cover: a
	// builder whose effective balance is below its bid has the bid rejected on
	// chain. It is a preference, not a filter — keys that can cover it come
	// first, and an underfunded key is still returned when nothing else is
	// available, because deliberately underfunded bids are a scenario buildoor
	// exists to test.
	RequiredGwei uint64
	// Count caps how many keys to return; 0 means all ready keys.
	Count uint64
	// Exclude holds key indices already committed for this slot.
	Exclude map[uint64]struct{}
}

// SelectForBid returns the keys to sign a slot's bids with, ordered by the
// requested strategy. It returns nil when no ready key qualifies.
func (r *Registry) SelectForBid(slot phase0.Slot, req SelectRequest) []*Key {
	funded := make([]*Key, 0, 4)
	underfunded := make([]*Key, 0, 2)

	for _, key := range r.Keys() {
		if _, excluded := req.Exclude[key.KeyIndex()]; excluded {
			continue
		}

		if key.State().Status != StatusActive {
			continue
		}

		// Balances move between refreshes (a payment settles, a top-up lands),
		// so this reads the live effective balance rather than the snapshot's.
		if r.EffectiveBalance(key.KeyIndex()) >= req.RequiredGwei {
			funded = append(funded, key)
		} else {
			underfunded = append(underfunded, key)
		}
	}

	if len(funded) == 0 && len(underfunded) == 0 {
		return nil
	}

	strategy := NormalizedStrategy(req.Strategy)

	// Funded keys first, so an underfunded one is only reached once nothing
	// else can cover the bid.
	ordered := make([]*Key, 0, len(funded)+len(underfunded))
	ordered = append(ordered, orderForStrategy(funded, strategy, slot)...)
	ordered = append(ordered, orderForStrategy(underfunded, strategy, slot)...)

	if req.Count > 0 && uint64(len(ordered)) > req.Count {
		ordered = ordered[:req.Count]
	}

	return ordered
}

// orderForStrategy orders ready keys (key-index ascending on input) per the
// strategy. The order decides which keys win when there are more ready keys
// than the slot needs.
func orderForStrategy(ready []*Key, strategy string, slot phase0.Slot) []*Key {
	if len(ready) == 0 {
		return nil
	}

	switch strategy {
	case StrategySingle:
		return ready[:1]

	case StrategyRandom:
		// Seeded from the slot so a slot's assignment is reproducible across
		// re-evaluations within the slot (and across a restart mid-slot).
		shuffled := slices.Clone(ready)
		rng := rand.New(rand.NewPCG(uint64(slot), 0x6275696c64))

		rng.Shuffle(len(shuffled), func(i, j int) {
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		})

		return shuffled

	case StrategyLeastUsed:
		sorted := slices.Clone(ready)
		slices.SortStableFunc(sorted, func(a, b *Key) int {
			return int(a.State().BidsSubmitted) - int(b.State().BidsSubmitted) //nolint:gosec // counters stay far below int range
		})

		return sorted

	default: // StrategyRoundRobin
		rotated := slices.Clone(ready)
		offset := int(uint64(slot) % uint64(len(rotated))) //nolint:gosec // bounded by len

		return append(rotated[offset:], rotated[:offset]...)
	}
}
