package payload_bidder

import (
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
)

func newTestPaymentTracker() *PaymentTracker {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	return NewPaymentTracker(&stubChainService{}, log)
}

func TestPaymentTracker_RecordAndReveal(t *testing.T) {
	tracker := newTestPaymentTracker()

	tracker.RecordWonBid(100, 1000)
	tracker.RecordWonBid(101, 500)
	assert.Equal(t, uint64(1500), tracker.GetTotalPendingPayments())
	assert.Equal(t, int64(0), tracker.GetBalanceAdjustment())

	// Revealing moves the payment from pending to a balance deduction.
	tracker.MarkRevealed(100)
	assert.Equal(t, uint64(500), tracker.GetTotalPendingPayments())
	assert.Equal(t, int64(-1000), tracker.GetBalanceAdjustment())

	// Revealing an unknown slot is a no-op.
	tracker.MarkRevealed(999)
	assert.Equal(t, uint64(500), tracker.GetTotalPendingPayments())
	assert.Equal(t, int64(-1000), tracker.GetBalanceAdjustment())

	// Re-recording a slot overwrites the pending value.
	tracker.RecordWonBid(101, 700)
	assert.Equal(t, uint64(700), tracker.GetTotalPendingPayments())
}

func TestPaymentTracker_DepositsAndDeductions(t *testing.T) {
	tracker := newTestPaymentTracker()

	tracker.AddDeposit(3000)
	assert.Equal(t, int64(3000), tracker.GetBalanceAdjustment())

	tracker.RecordWonBid(10, 1000)
	tracker.MarkRevealed(10)
	assert.Equal(t, int64(2000), tracker.GetBalanceAdjustment())
}

func TestPaymentTracker_ReconcileToEpoch(t *testing.T) {
	chainSvc := &stubChainService{currentEpoch: 5}

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	tracker := NewPaymentTracker(chainSvc, log)

	// A top-up credit is anchored to the current epoch (5).
	tracker.AddDeposit(50_000_000_000)
	assert.Equal(t, int64(50_000_000_000), tracker.GetBalanceAdjustment())

	// Reconciling to the same epoch keeps it (the snapshot does not yet
	// reflect the in-epoch top-up).
	tracker.ReconcileToEpoch(5)
	assert.Equal(t, int64(50_000_000_000), tracker.GetBalanceAdjustment(),
		"same-epoch reconcile must retain the in-epoch delta")

	// Once the authoritative snapshot advances, the credit is dropped — it is
	// now reflected in (or superseded by) the snapshot balance. This is what
	// prevents an unlanded top-up from inflating the balance forever.
	tracker.ReconcileToEpoch(6)
	assert.Equal(t, int64(0), tracker.GetBalanceAdjustment(),
		"advancing the snapshot epoch must drop the stale credit")

	// A reveal deduction in the new epoch anchors to that epoch and survives
	// same-epoch reconciles.
	chainSvc.currentEpoch = 6
	tracker.RecordWonBid(6*32, 1000)
	tracker.MarkRevealed(6 * 32)
	assert.Equal(t, int64(-1000), tracker.GetBalanceAdjustment())

	tracker.ReconcileToEpoch(6)
	assert.Equal(t, int64(-1000), tracker.GetBalanceAdjustment())

	tracker.ReconcileToEpoch(7)
	assert.Equal(t, int64(0), tracker.GetBalanceAdjustment())
}

// TestPaymentTracker_MarkRevealedBeforeRecordWonBid guards NM-07: MarkRevealed
// and RecordWonBid are fed by independent goroutines with no happens-before
// edge between them, so a delayed head event can deliver them in either
// order. Before the fix, a MarkRevealed that arrived first found no pending
// entry and silently no-op'd, permanently dropping the deduction; the later
// RecordWonBid then created an orphaned pending entry that was never marked
// revealed.
func TestPaymentTracker_MarkRevealedBeforeRecordWonBid(t *testing.T) {
	tracker := newTestPaymentTracker()

	const slot = phase0.Slot(200)
	const value = uint64(5000)

	// The reveal completes before the win is even recorded.
	tracker.MarkRevealed(slot)
	assert.Equal(t, int64(0), tracker.GetBalanceAdjustment(),
		"the value isn't known yet, so no deduction can apply until RecordWonBid runs")
	assert.Equal(t, uint64(0), tracker.GetTotalPendingPayments())

	// The delayed head event now lands and the win is recorded.
	tracker.RecordWonBid(slot, value)

	// The deduction lands immediately -- the bid is never treated as
	// "pending" despite arriving after the reveal, since it was already
	// revealed.
	assert.Equal(t, -int64(value), tracker.GetBalanceAdjustment(),
		"NM-07: the deduction must still apply once the won bid is recorded")
	assert.Equal(t, uint64(0), tracker.GetTotalPendingPayments(),
		"a bid revealed before it was recorded must never sit in pending")
}

// TestPaymentTracker_RecordWonBidBeforeMarkRevealed confirms the common
// ordering (win recorded, then revealed) is unaffected by the NM-07 fix.
func TestPaymentTracker_RecordWonBidBeforeMarkRevealed(t *testing.T) {
	tracker := newTestPaymentTracker()

	const slot = phase0.Slot(201)
	const value = uint64(3000)

	tracker.RecordWonBid(slot, value)
	assert.Equal(t, uint64(value), tracker.GetTotalPendingPayments())
	assert.Equal(t, int64(0), tracker.GetBalanceAdjustment())

	tracker.MarkRevealed(slot)
	assert.Equal(t, uint64(0), tracker.GetTotalPendingPayments())
	assert.Equal(t, -int64(value), tracker.GetBalanceAdjustment())
}

// TestPaymentTracker_EarlyRevealNeverRecordedIsPruned confirms a slot whose
// reveal arrived early but whose matching RecordWonBid never shows up (the
// won-bid report was lost, or the win didn't pan out) doesn't leak forever:
// PruneExpiredPayments clears it the same way it clears an expired pending
// payment.
func TestPaymentTracker_EarlyRevealNeverRecordedIsPruned(t *testing.T) {
	tracker := newTestPaymentTracker()

	tracker.MarkRevealed(32) // epoch 1, per stubChainService's slot/32 mapping
	assert.Len(t, tracker.earlyReveals, 1)

	tracker.PruneExpiredPayments(phase0.Epoch(2))
	assert.Len(t, tracker.earlyReveals, 1, "must stay through payment epoch + 1")

	tracker.PruneExpiredPayments(phase0.Epoch(3))
	assert.Empty(t, tracker.earlyReveals)

	// A RecordWonBid arriving after the marker was pruned falls back to the
	// normal pending path rather than (wrongly) deducting immediately.
	tracker.RecordWonBid(32, 100)
	assert.Equal(t, uint64(100), tracker.GetTotalPendingPayments())
	assert.Equal(t, int64(0), tracker.GetBalanceAdjustment())
}

func TestPaymentTracker_PruneExpiredPayments(t *testing.T) {
	tracker := newTestPaymentTracker()

	// stubChainService maps slot -> epoch via slot/32.
	tracker.RecordWonBid(32, 100) // epoch 1
	tracker.RecordWonBid(64, 200) // epoch 2

	// Payments stay pending through payment epoch + 1.
	tracker.PruneExpiredPayments(phase0.Epoch(2))
	assert.Equal(t, uint64(300), tracker.GetTotalPendingPayments())

	// Epoch 3 expires the epoch-1 payment (3 > 1+1) but keeps epoch 2.
	tracker.PruneExpiredPayments(phase0.Epoch(3))
	assert.Equal(t, uint64(200), tracker.GetTotalPendingPayments())

	tracker.PruneExpiredPayments(phase0.Epoch(4))
	assert.Equal(t, uint64(0), tracker.GetTotalPendingPayments())

	// Pruning never touches the balance adjustment.
	assert.Equal(t, int64(0), tracker.GetBalanceAdjustment())
}
