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

func TestPaymentTracker_DepositsAndReset(t *testing.T) {
	tracker := newTestPaymentTracker()

	tracker.AddDeposit(3000)
	assert.Equal(t, int64(3000), tracker.GetBalanceAdjustment())

	tracker.RecordWonBid(10, 1000)
	tracker.MarkRevealed(10)
	assert.Equal(t, int64(2000), tracker.GetBalanceAdjustment())

	tracker.ResetBalanceAdjustment()
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
