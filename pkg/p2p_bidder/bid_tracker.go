package p2p_bidder

import (
	"sync"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder_keys"
)

// BidTracker tracks bids observed on the p2p network for competition analysis.
type BidTracker struct {
	slotBids map[phase0.Slot]*SlotBids
	registry *builder_keys.Registry
	mu       sync.RWMutex

	log logrus.FieldLogger
}

// NewBidTracker creates a new bid tracker. The key registry identifies which
// gossiped bids are ours: with several managed keys bidding the same slot, any
// of their builder indices must be excluded from competitor comparisons.
func NewBidTracker(registry *builder_keys.Registry, log logrus.FieldLogger) *BidTracker {
	return &BidTracker{
		slotBids: make(map[phase0.Slot]*SlotBids, 64),
		registry: registry,
		log:      log.WithField("component", "bid-tracker"),
	}
}

// IsOurs reports whether a gossiped bid's builder index belongs to the managed
// key set.
func (t *BidTracker) IsOurs(builderIndex uint64) bool {
	return t.registry != nil && t.registry.ByBuilderIndex(builderIndex) != nil
}

// TrackBid adds a bid to the tracker.
func (t *BidTracker) TrackBid(bid *ExecutionPayloadBid, isOurs bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	slotBids, ok := t.slotBids[bid.Slot]
	if !ok {
		slotBids = NewSlotBids(bid.Slot)
		t.slotBids[bid.Slot] = slotBids
	}

	tracked := &TrackedBid{
		Bid:          bid,
		BuilderIndex: bid.BuilderIndex,
		IsOurs:       isOurs,
	}

	slotBids.Bids[bid.BuilderIndex] = tracked

	if isOurs {
		slotBids.OurBid = tracked
	}

	// Update highest bid
	if slotBids.HighestBid == nil || bid.Value > slotBids.HighestBid.Bid.Value {
		slotBids.HighestBid = tracked
	}

	t.log.WithFields(logrus.Fields{
		"slot":          bid.Slot,
		"builder_index": bid.BuilderIndex,
		"value":         bid.Value,
		"is_ours":       isOurs,
	}).Debug("Tracked bid")
}

// GetHighestBid returns the highest bid for a slot.
func (t *BidTracker) GetHighestBid(slot phase0.Slot) *TrackedBid {
	t.mu.RLock()
	defer t.mu.RUnlock()

	slotBids, ok := t.slotBids[slot]
	if !ok {
		return nil
	}

	return slotBids.HighestBid
}

// GetHighestCompetitorBid returns the highest tracked bid value (gwei) for
// the slot excluding every key of ours, and whether any competitor bid is
// known. Unlike GetHighestBid it can never report one of our own bids back to
// us — which matters once several managed keys bid the same slot.
// A non-zero parentHash restricts the comparison to bids committing to the
// same execution parent (bids on other forks are not competing).
func (t *BidTracker) GetHighestCompetitorBid(
	slot phase0.Slot, parentHash phase0.Hash32,
) (uint64, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	slotBids, ok := t.slotBids[slot]
	if !ok {
		return 0, false
	}

	var highest uint64

	found := false

	for builderIndex, tracked := range slotBids.Bids {
		if tracked.IsOurs || t.IsOurs(builderIndex) {
			continue
		}

		if parentHash != (phase0.Hash32{}) && tracked.Bid.ParentBlockHash != parentHash {
			continue
		}

		if !found || tracked.Bid.Value > highest {
			highest = tracked.Bid.Value
			found = true
		}
	}

	return highest, found
}

// GetOurBid returns our bid for a slot.
func (t *BidTracker) GetOurBid(slot phase0.Slot) *TrackedBid {
	t.mu.RLock()
	defer t.mu.RUnlock()

	slotBids, ok := t.slotBids[slot]
	if !ok {
		return nil
	}

	return slotBids.OurBid
}

// GetSlotBids returns all bids for a slot.
func (t *BidTracker) GetSlotBids(slot phase0.Slot) *SlotBids {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.slotBids[slot]
}

// Cleanup removes old slot data.
func (t *BidTracker) Cleanup(olderThan phase0.Slot) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for slot := range t.slotBids {
		if slot < olderThan {
			delete(t.slotBids, slot)
		}
	}
}
