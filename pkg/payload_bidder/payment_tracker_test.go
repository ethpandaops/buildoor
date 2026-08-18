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

	tracker.RecordWonBid(0, 100, 1000)
	tracker.RecordWonBid(0, 101, 500)
	assert.Equal(t, uint64(1500), tracker.GetTotalPendingPayments())
	assert.Equal(t, int64(0), tracker.GetBalanceAdjustment(0))

	// Revealing moves the payment from pending to a balance deduction.
	tracker.MarkRevealed(0, 100)
	assert.Equal(t, uint64(500), tracker.GetTotalPendingPayments())
	assert.Equal(t, int64(-1000), tracker.GetBalanceAdjustment(0))

	// Revealing an unknown slot is a no-op.
	tracker.MarkRevealed(0, 999)
	assert.Equal(t, uint64(500), tracker.GetTotalPendingPayments())
	assert.Equal(t, int64(-1000), tracker.GetBalanceAdjustment(0))

	// Re-recording a slot overwrites the pending value.
	tracker.RecordWonBid(0, 101, 700)
	assert.Equal(t, uint64(700), tracker.GetTotalPendingPayments())
}

func TestPaymentTracker_DepositsAndDeductions(t *testing.T) {
	tracker := newTestPaymentTracker()

	tracker.AddDeposit(0, 3000)
	assert.Equal(t, int64(3000), tracker.GetBalanceAdjustment(0))

	tracker.RecordWonBid(0, 10, 1000)
	tracker.MarkRevealed(0, 10)
	assert.Equal(t, int64(2000), tracker.GetBalanceAdjustment(0))
}

func TestPaymentTracker_ReconcileToEpoch(t *testing.T) {
	chainSvc := &stubChainService{currentEpoch: 5}

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	tracker := NewPaymentTracker(chainSvc, log)

	// A top-up credit is anchored to the current epoch (5).
	tracker.AddDeposit(0, 50_000_000_000)
	assert.Equal(t, int64(50_000_000_000), tracker.GetBalanceAdjustment(0))

	// Reconciling to the same epoch keeps it (the snapshot does not yet
	// reflect the in-epoch top-up).
	tracker.ReconcileToEpoch(5)
	assert.Equal(t, int64(50_000_000_000), tracker.GetBalanceAdjustment(0),
		"same-epoch reconcile must retain the in-epoch delta")

	// Once the authoritative snapshot advances, the credit is dropped — it is
	// now reflected in (or superseded by) the snapshot balance. This is what
	// prevents an unlanded top-up from inflating the balance forever.
	tracker.ReconcileToEpoch(6)
	assert.Equal(t, int64(0), tracker.GetBalanceAdjustment(0),
		"advancing the snapshot epoch must drop the stale credit")

	// A reveal deduction in the new epoch anchors to that epoch and survives
	// same-epoch reconciles.
	chainSvc.currentEpoch = 6
	tracker.RecordWonBid(0, 6*32, 1000)
	tracker.MarkRevealed(0, 6*32)
	assert.Equal(t, int64(-1000), tracker.GetBalanceAdjustment(0))

	tracker.ReconcileToEpoch(6)
	assert.Equal(t, int64(-1000), tracker.GetBalanceAdjustment(0))

	tracker.ReconcileToEpoch(7)
	assert.Equal(t, int64(0), tracker.GetBalanceAdjustment(0))
}

func TestPaymentTracker_PruneExpiredPayments(t *testing.T) {
	tracker := newTestPaymentTracker()

	// stubChainService maps slot -> epoch via slot/32.
	tracker.RecordWonBid(0, 32, 100) // epoch 1
	tracker.RecordWonBid(0, 64, 200) // epoch 2

	// Payments stay pending through payment epoch + 1.
	tracker.PruneExpiredPayments(phase0.Epoch(2))
	assert.Equal(t, uint64(300), tracker.GetTotalPendingPayments())

	// Epoch 3 expires the epoch-1 payment (3 > 1+1) but keeps epoch 2.
	tracker.PruneExpiredPayments(phase0.Epoch(3))
	assert.Equal(t, uint64(200), tracker.GetTotalPendingPayments())

	tracker.PruneExpiredPayments(phase0.Epoch(4))
	assert.Equal(t, uint64(0), tracker.GetTotalPendingPayments())

	// Pruning never touches the balance adjustment.
	assert.Equal(t, int64(0), tracker.GetBalanceAdjustment(0))
}

// Payments are owed per key: charging the wrong one would let an underfunded key
// keep bidding while a funded one looks broke.
func TestPaymentTracker_PerKeyAccounting(t *testing.T) {
	tracker := newTestPaymentTracker()

	tracker.RecordWonBid(0, 100, 1000)
	tracker.RecordWonBid(3, 101, 500)

	assert.Equal(t, uint64(1500), tracker.GetTotalPendingPayments())
	assert.Equal(t, uint64(1000), tracker.GetPendingPayments(0))
	assert.Equal(t, uint64(500), tracker.GetPendingPayments(3))
	assert.Equal(t, uint64(0), tracker.GetPendingPayments(7))

	// Revealing key 3's win deducts from key 3 only.
	tracker.MarkRevealed(3, 101)
	assert.Equal(t, int64(0), tracker.GetBalanceAdjustment(0))
	assert.Equal(t, int64(-500), tracker.GetBalanceAdjustment(3))
	assert.Equal(t, uint64(1000), tracker.GetTotalPendingPayments())

	// The same slot can be owed by two keys (both bid it; only one won on
	// chain), and disputing one must not touch the other.
	tracker.RecordWonBid(5, 200, 700)
	tracker.RecordWonBid(6, 200, 800)
	tracker.SetPaymentDisputed(5, 200, true)

	assert.Equal(t, uint64(0), tracker.GetPendingPayments(5))
	assert.Equal(t, uint64(800), tracker.GetPendingPayments(6))
}

// TestPaymentTracker_MarkRevealedBeforeRecordWonBid: MarkRevealed and
// RecordWonBid are fed by independent goroutines with no happens-before edge
// (the Builder API even requests reveals at block submission, before the
// head event), so either order is possible. A MarkRevealed arriving first
// must not drop the deduction: it defers it, and the later RecordWonBid
// applies it immediately without ever treating the bid as pending.
func TestPaymentTracker_MarkRevealedBeforeRecordWonBid(t *testing.T) {
	tracker := newTestPaymentTracker()

	const keyIndex = uint64(3)

	const slot = phase0.Slot(200)

	const value = uint64(5000)

	// The reveal completes before the win is even recorded.
	tracker.MarkRevealed(keyIndex, slot)
	assert.Equal(t, int64(0), tracker.GetBalanceAdjustment(keyIndex),
		"the value isn't known yet, so no deduction can apply until RecordWonBid runs")
	assert.Equal(t, uint64(0), tracker.GetTotalPendingPayments())

	// The delayed head event now lands and the win is recorded.
	tracker.RecordWonBid(keyIndex, slot, value)

	assert.Equal(t, -int64(value), tracker.GetBalanceAdjustment(keyIndex),
		"the deduction must still apply once the won bid is recorded")
	assert.Equal(t, uint64(0), tracker.GetTotalPendingPayments(),
		"a bid revealed before it was recorded must never sit in pending")
}

// TestPaymentTracker_EarlyRevealIsPerKey confirms an early reveal recorded
// against one key never satisfies another key's won bid for the same slot.
func TestPaymentTracker_EarlyRevealIsPerKey(t *testing.T) {
	tracker := newTestPaymentTracker()

	tracker.MarkRevealed(1, 300)
	tracker.RecordWonBid(2, 300, 700)

	assert.Equal(t, int64(0), tracker.GetBalanceAdjustment(2),
		"key 2's bid was not revealed; it must stay pending")
	assert.Equal(t, uint64(700), tracker.GetTotalPendingPayments())
	assert.Equal(t, int64(0), tracker.GetBalanceAdjustment(1))
}

// TestPaymentTracker_EarlyRevealNeverRecordedIsPruned confirms an early
// reveal whose matching won bid never arrives is cleared by the same
// two-epoch prune pass as expired pending payments, instead of deducting
// from a balance forever later.
func TestPaymentTracker_EarlyRevealNeverRecordedIsPruned(t *testing.T) {
	tracker := newTestPaymentTracker()

	const keyIndex = uint64(0)

	const slot = phase0.Slot(64) // epoch 2 on the stub chain

	tracker.MarkRevealed(keyIndex, slot)

	epoch := tracker.chainSvc.GetEpochOfSlot(slot)
	tracker.PruneExpiredPayments(epoch + 2)

	// A won-bid report arriving after the prune window must be treated as a
	// fresh pending payment, not matched to the long-gone reveal.
	tracker.RecordWonBid(keyIndex, slot, 900)
	assert.Equal(t, int64(0), tracker.GetBalanceAdjustment(keyIndex))
	assert.Equal(t, uint64(900), tracker.GetTotalPendingPayments())
}
