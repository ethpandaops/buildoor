package payload_bidder

import (
	"sync"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
)

// PendingPayment records an unrevealed won bid that may be deducted later.
type PendingPayment struct {
	Slot  phase0.Slot
	Epoch phase0.Epoch
	Value uint64 // Gwei
}

// PaymentTracker tracks the builder's payment obligations and live balance
// adjustments across both bid flows (p2p and Builder API). Fed by the
// InclusionTracker (won bids) and RevealService (reveals); consumed by the
// lifecycle manager (top-ups) and the WebUI. Passive and thread-safe: it runs
// no goroutine of its own.
type PaymentTracker struct {
	// balanceAdjustment bridges the gap between an operation and the epoch
	// snapshot reflecting it: positive = deposits/topups, negative = revealed
	// bid payments. It holds only deltas from the current snapshot epoch;
	// ReconcileToEpoch drops it once the snapshot advances (the snapshot then
	// accounts for those deltas). adjustmentEpoch is the latest epoch a delta
	// was anchored to.
	balanceAdjustment int64
	adjustmentEpoch   phase0.Epoch
	adjustmentMu      sync.Mutex

	// Pending payments: unrevealed won bids, pending for 2 epochs.
	// Only these count as "pending" in the UI and for topup checks.
	pendingPayments map[phase0.Slot]*PendingPayment
	pendingMu       sync.Mutex

	chainSvc chain.Service
	log      logrus.FieldLogger
}

// NewPaymentTracker creates a new payment tracker.
func NewPaymentTracker(chainSvc chain.Service, log logrus.FieldLogger) *PaymentTracker {
	return &PaymentTracker{
		pendingPayments: make(map[phase0.Slot]*PendingPayment, 16),
		chainSvc:        chainSvc,
		log:             log.WithField("component", "payment-tracker"),
	}
}

// RecordWonBid records a won bid as a pending payment (unrevealed).
// Called when our bid is included in a beacon block.
// If we later reveal, call MarkRevealed to move it from pending to a balance deduction.
// If we don't reveal, it stays pending for 2 epochs then expires.
func (t *PaymentTracker) RecordWonBid(slot phase0.Slot, value uint64) {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()

	epoch := t.chainSvc.GetEpochOfSlot(slot)

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
func (t *PaymentTracker) MarkRevealed(slot phase0.Slot) {
	t.pendingMu.Lock()
	p, ok := t.pendingPayments[slot]
	if !ok {
		t.pendingMu.Unlock()
		return
	}

	value := p.Value
	delete(t.pendingPayments, slot)
	t.pendingMu.Unlock()

	// Deduct from live balance, anchored to this slot's epoch so the
	// reconciler keeps the delta until the snapshot advances past it.
	t.adjustmentMu.Lock()
	t.balanceAdjustment -= int64(value)
	t.anchorEpochLocked(t.chainSvc.GetEpochOfSlot(slot))
	t.adjustmentMu.Unlock()

	t.log.WithFields(logrus.Fields{
		"slot":  slot,
		"value": value,
	}).Info("Revealed bid: deducted from live balance")
}

// AddDeposit credits a deposit/topup to the live balance adjustment, anchored
// to the current epoch. The credit is reconciled away by ReconcileToEpoch once
// the authoritative snapshot advances past that epoch.
func (t *PaymentTracker) AddDeposit(amount uint64) {
	t.adjustmentMu.Lock()
	t.balanceAdjustment += int64(amount)
	t.anchorEpochLocked(t.chainSvc.GetCurrentEpoch())
	t.adjustmentMu.Unlock()

	t.log.WithField("amount", amount).Info("Deposit added to live balance")
}

// anchorEpochLocked advances the adjustment's anchor epoch so the reconciler
// keeps the current delta through opEpoch. Callers must hold adjustmentMu.
func (t *PaymentTracker) anchorEpochLocked(opEpoch phase0.Epoch) {
	if opEpoch > t.adjustmentEpoch {
		t.adjustmentEpoch = opEpoch
	}
}

// GetBalanceAdjustment returns the cumulative balance adjustment since last state refresh.
func (t *PaymentTracker) GetBalanceAdjustment() int64 {
	t.adjustmentMu.Lock()
	defer t.adjustmentMu.Unlock()

	return t.balanceAdjustment
}

// ReconcileToEpoch drops the local adjustment once the authoritative builder
// snapshot advances past the epoch the adjustment is anchored to: the newer
// snapshot already accounts for every reveal/top-up from earlier epochs.
// Deltas anchored to the snapshot's own epoch are retained (not yet reflected).
// Safe to call every refresh; a no-op until the epoch advances.
func (t *PaymentTracker) ReconcileToEpoch(snapshotEpoch phase0.Epoch) {
	t.adjustmentMu.Lock()
	defer t.adjustmentMu.Unlock()

	if snapshotEpoch <= t.adjustmentEpoch {
		return
	}

	t.balanceAdjustment = 0
	t.adjustmentEpoch = snapshotEpoch
}

// GetTotalPendingPayments returns the sum of unrevealed won bid obligations.
func (t *PaymentTracker) GetTotalPendingPayments() uint64 {
	t.pendingMu.Lock()
	defer t.pendingMu.Unlock()

	var total uint64

	for _, p := range t.pendingPayments {
		total += p.Value
	}

	return total
}

// PruneExpiredPayments removes pending payments older than 2 epochs.
func (t *PaymentTracker) PruneExpiredPayments(currentEpoch phase0.Epoch) {
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
