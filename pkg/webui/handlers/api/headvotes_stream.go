package api

import (
	"math"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/chain"
)

// headVoteKey identifies one participation curve: a slot can collect votes
// for several block roots.
type headVoteKey struct {
	slot phase0.Slot
	root phase0.Root
}

// headVoteCoalescer thins the head-vote updates sent to stream viewers to
// the latest value per curve and flush interval. It is owned by the event
// loop goroutine and therefore unsynchronized.
type headVoteCoalescer struct {
	pending map[headVoteKey]*chain.HeadVoteUpdate
	met     map[headVoteKey]struct{}
}

func newHeadVoteCoalescer() *headVoteCoalescer {
	return &headVoteCoalescer{
		pending: make(map[headVoteKey]*chain.HeadVoteUpdate, 4),
		met:     make(map[headVoteKey]struct{}, 16),
	}
}

// add records an update and reports whether it must be sent right away:
// the first update of a curve that meets the threshold carries the marker
// the graph draws, so it is not delayed to the next flush.
func (c *headVoteCoalescer) add(update *chain.HeadVoteUpdate) bool {
	key := headVoteKey{slot: update.Slot, root: update.BlockRoot}

	if update.ThresholdMet {
		if _, seen := c.met[key]; !seen {
			c.met[key] = struct{}{}
			delete(c.pending, key)
			c.prune(update.Slot)

			return true
		}
	}

	c.pending[key] = update

	return false
}

// drain returns the pending updates and clears them.
func (c *headVoteCoalescer) drain() []*chain.HeadVoteUpdate {
	if len(c.pending) == 0 {
		return nil
	}

	out := make([]*chain.HeadVoteUpdate, 0, len(c.pending))
	for key, update := range c.pending {
		out = append(out, update)
		delete(c.pending, key)
	}

	return out
}

// prune forgets threshold markers of slots that no longer receive votes.
func (c *headVoteCoalescer) prune(current phase0.Slot) {
	for key := range c.met {
		if key.slot+8 < current {
			delete(c.met, key)
		}
	}
}

// roundPct trims a percentage to two decimals: the graph cannot show more,
// and full float64 precision triples the size of every update on the wire.
func roundPct(v float64) float64 {
	return math.Round(v*100) / 100
}
