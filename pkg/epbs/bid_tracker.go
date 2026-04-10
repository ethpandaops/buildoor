// Package epbs implements ePBS-specific bid management and tracking logic.
package epbs

import (
	"sync"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// PendingPayment records a payment obligation from a won bid.
type PendingPayment struct {
	Slot     phase0.Slot
	Epoch    phase0.Epoch
	Value    uint64 // Gwei
	Revealed bool   // True if we revealed the payload (immediate deduction from live balance)
}

// BidTracker tracks bids for competition analysis and pending payment obligations.
type BidTracker struct {
	slotBids      map[phase0.Slot]*SlotBids
	ourBuilderIdx uint64
	mu            sync.RWMutex

	// Pending payments from won bids, keyed by slot for fast lookup.
	pendingPayments map[phase0.Slot]*PendingPayment
	pendingMu       sync.Mutex

	chainSpec *beacon.ChainSpec
	log       logrus.FieldLogger
}

// NewBidTracker creates a new bid tracker.
func NewBidTracker(ourBuilderIdx uint64, chainSpec *beacon.ChainSpec, log logrus.FieldLogger) *BidTracker {
	return &BidTracker{
		slotBids:        make(map[phase0.Slot]*SlotBids, 64),
		ourBuilderIdx:   ourBuilderIdx,
		pendingPayments: make(map[phase0.Slot]*PendingPayment, 16),
		chainSpec:       chainSpec,
		log:             log.WithField("component", "bid-tracker"),
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

// RecordWonBid records a pending payment from a won bid.
// Called when our payload is included in a beacon block.
// The payment remains pending (delayed) until MarkRevealed is called or it expires after 2 epochs.
func (t *BidTracker) RecordWonBid(slot phase0.Slot, value uint64) {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()

	epoch := phase0.Epoch(uint64(slot) / t.chainSpec.SlotsPerEpoch)

	t.pendingPayments[slot] = &PendingPayment{
		Slot:  slot,
		Epoch: epoch,
		Value: value,
	}

	t.log.WithFields(logrus.Fields{
		"slot":  slot,
		"epoch": epoch,
		"value": value,
	}).Info("Recorded won bid as pending payment")
}

// MarkRevealed marks a won bid as revealed (immediate payment).
// When revealed and confirmed by the next block, the payment is deducted
// directly from the live balance. We remove it from pending since the
// state refresh will reflect the deduction.
func (t *BidTracker) MarkRevealed(slot phase0.Slot) {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()

	if p, ok := t.pendingPayments[slot]; ok {
		p.Revealed = true

		t.log.WithFields(logrus.Fields{
			"slot":  slot,
			"value": p.Value,
		}).Info("Won bid marked as revealed (immediate payment)")
	}
}

// GetTotalPendingPayments returns the sum of delayed (unrevealed) payment obligations.
// Revealed payments are excluded since they are deducted directly from live balance.
func (t *BidTracker) GetTotalPendingPayments() uint64 {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()

	var total uint64

	for _, p := range t.pendingPayments {
		if !p.Revealed {
			total += p.Value
		}
	}

	return total
}

// PruneExpiredPayments removes pending payments older than 2 epochs.
// Delayed payments are only relevant for the current and next epoch.
// After that the beacon state reflects the actual deduction (or skip).
func (t *BidTracker) PruneExpiredPayments(currentEpoch phase0.Epoch) {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()

	for slot, p := range t.pendingPayments {
		// Revealed payments: remove immediately since state already reflects them
		if p.Revealed {
			delete(t.pendingPayments, slot)
			continue
		}

		// Delayed payments: keep for current epoch and next epoch (2 epoch window)
		if currentEpoch > p.Epoch+1 {
			t.log.WithFields(logrus.Fields{
				"slot":          slot,
				"payment_epoch": p.Epoch,
				"current_epoch": currentEpoch,
				"value":         p.Value,
			}).Debug("Pruning expired pending payment")

			delete(t.pendingPayments, slot)
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

// SetBuilderIndex updates the builder index.
func (t *BidTracker) SetBuilderIndex(index uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.ourBuilderIdx = index
}
