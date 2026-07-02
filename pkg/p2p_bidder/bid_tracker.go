package p2p_bidder

import (
	"sync"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
)

// BidTracker tracks bids observed on the p2p network for competition analysis.
type BidTracker struct {
	slotBids      map[phase0.Slot]*SlotBids
	ourBuilderIdx uint64
	mu            sync.RWMutex

	log logrus.FieldLogger
}

// NewBidTracker creates a new bid tracker.
func NewBidTracker(ourBuilderIdx uint64, log logrus.FieldLogger) *BidTracker {
	return &BidTracker{
		slotBids:      make(map[phase0.Slot]*SlotBids, 64),
		ourBuilderIdx: ourBuilderIdx,
		log:           log.WithField("component", "bid-tracker"),
	}
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

// SetBuilderIndex updates the builder index.
func (t *BidTracker) SetBuilderIndex(index uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.ourBuilderIdx = index
}
