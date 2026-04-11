// Package epbs implements ePBS-specific bid management and tracking logic.
package epbs

import (
	"sync"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// PendingPayment records an unrevealed won bid that may be deducted later.
type PendingPayment struct {
	Slot  phase0.Slot
	Epoch phase0.Epoch
	Value uint64 // Gwei
}

// BidTracker tracks bids for competition analysis and balance adjustments.
type BidTracker struct {
	slotBids      map[phase0.Slot]*SlotBids
	ourBuilderIdx uint64
	mu            sync.RWMutex

	// Balance adjustments since last chain state refresh.
	// Positive = deposits/topups, negative = revealed bid payments.
	// Reset to 0 when the chain state refreshes (epoch boundary).
	balanceAdjustment int64
	adjustmentMu      sync.Mutex

	// Pending payments: unrevealed won bids, pending for 2 epochs.
	// Only these count as "pending" in the UI and for topup checks.
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

// RecordWonBid records a won bid as a pending payment (unrevealed).
// Called when our bid is included in a beacon block.
// If we later reveal, call MarkRevealed to move it from pending to a balance deduction.
// If we don't reveal, it stays pending for 2 epochs then expires.
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

// MarkRevealed moves a won bid from pending to an immediate balance deduction.
// The payment is removed from pending and subtracted from the balance adjustment.
func (t *BidTracker) MarkRevealed(slot phase0.Slot) {
	t.pendingMu.Lock()
	p, ok := t.pendingPayments[slot]
	if !ok {
		t.pendingMu.Unlock()
		return
	}

	value := p.Value
	delete(t.pendingPayments, slot)
	t.pendingMu.Unlock()

	// Deduct from live balance
	t.adjustmentMu.Lock()
	t.balanceAdjustment -= int64(value)
	t.adjustmentMu.Unlock()

	t.log.WithFields(logrus.Fields{
		"slot":  slot,
		"value": value,
	}).Info("Revealed bid: deducted from live balance")
}

// AddDeposit adds a deposit/topup amount to the balance adjustment.
// Topups take effect immediately (no finalization delay).
func (t *BidTracker) AddDeposit(amount uint64) {
	t.adjustmentMu.Lock()
	t.balanceAdjustment += int64(amount)
	t.adjustmentMu.Unlock()

	t.log.WithField("amount", amount).Info("Deposit added to live balance")
}

// GetBalanceAdjustment returns the cumulative balance adjustment since last state refresh.
func (t *BidTracker) GetBalanceAdjustment() int64 {
	t.adjustmentMu.Lock()
	defer t.adjustmentMu.Unlock()

	return t.balanceAdjustment
}

// ResetBalanceAdjustment resets the adjustment to 0.
// Called when the chain state refreshes and the balance is up to date.
func (t *BidTracker) ResetBalanceAdjustment() {
	t.adjustmentMu.Lock()
	t.balanceAdjustment = 0
	t.adjustmentMu.Unlock()
}

// GetTotalPendingPayments returns the sum of unrevealed won bid obligations.
func (t *BidTracker) GetTotalPendingPayments() uint64 {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()

	var total uint64

	for _, p := range t.pendingPayments {
		total += p.Value
	}

	return total
}

// PruneExpiredPayments removes pending payments older than 2 epochs.
func (t *BidTracker) PruneExpiredPayments(currentEpoch phase0.Epoch) {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()

	for slot, p := range t.pendingPayments {
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
