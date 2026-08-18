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
