// Package epbs implements ePBS-specific bid management and tracking logic.
package epbs

import (
	"sync"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
)

// WonBid records a bid that won a slot.
type WonBid struct {
	Slot      phase0.Slot
	Value     uint64 // Gwei
	Confirmed bool
}

// BidTracker tracks bids for competition analysis.
type BidTracker struct {
	slotBids      map[phase0.Slot]*SlotBids
	ourBuilderIdx uint64
	mu            sync.RWMutex
	wonBids       []*WonBid
	wonBidsMu     sync.Mutex
	log           logrus.FieldLogger
}

// NewBidTracker creates a new bid tracker.
func NewBidTracker(ourBuilderIdx uint64, log logrus.FieldLogger) *BidTracker {
	return &BidTracker{
		slotBids:      make(map[phase0.Slot]*SlotBids, 64),
		ourBuilderIdx: ourBuilderIdx,
		wonBids:       make([]*WonBid, 0, 32),
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

// MarkWinningBid marks the winning bid for a slot.
func (t *BidTracker) MarkWinningBid(slot phase0.Slot, builderIndex uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	slotBids, ok := t.slotBids[slot]
	if !ok {
		return
	}

	if bid, exists := slotBids.Bids[builderIndex]; exists {
		slotBids.WinningBid = bid

		if bid.IsOurs {
			t.wonBidsMu.Lock()
			t.wonBids = append(t.wonBids, &WonBid{
				Slot:  slot,
				Value: bid.Bid.Value,
			})
			t.wonBidsMu.Unlock()
		}
	}
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

// RecordWonBid records a bid that was won.
func (t *BidTracker) RecordWonBid(slot phase0.Slot, value uint64) {
	t.wonBidsMu.Lock()
	defer t.wonBidsMu.Unlock()

	t.wonBids = append(t.wonBids, &WonBid{
		Slot:      slot,
		Value:     value,
		Confirmed: false,
	})
}

// GetTotalPendingPayments returns the sum of unconfirmed won bid values.
func (t *BidTracker) GetTotalPendingPayments() uint64 {
	t.wonBidsMu.Lock()
	defer t.wonBidsMu.Unlock()

	var total uint64

	for _, bid := range t.wonBids {
		if !bid.Confirmed {
			total += bid.Value
		}
	}

	return total
}

// ConfirmWonBid marks a won bid as confirmed.
func (t *BidTracker) ConfirmWonBid(slot phase0.Slot) {
	t.wonBidsMu.Lock()
	defer t.wonBidsMu.Unlock()

	for _, bid := range t.wonBids {
		if bid.Slot == slot {
			bid.Confirmed = true

			break
		}
	}
}

// SetBuilderIndex updates the builder index.
func (t *BidTracker) SetBuilderIndex(index uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.ourBuilderIdx = index
}
