package payload_bidder

import (
	"sync"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
)

// PendingPayment records an unrevealed won bid that may be deducted later.
type PendingPayment struct {
	KeyIndex uint64
	Slot     phase0.Slot
	Epoch    phase0.Epoch
	Value    uint64 // Gwei
	// Disputed marks a payment whose winning block was reorged out: it no
	// longer counts toward the pending total unless the block returns.
	Disputed bool
}

// keyPayments is one builder key's payment accounting.
type keyPayments struct {
	// balanceAdjustment bridges the gap between an operation and the epoch
	// snapshot reflecting it: positive = deposits/topups, negative = revealed
	// bid payments. It holds only deltas from the current snapshot epoch;
	// ReconcileToEpoch drops it once the snapshot advances (the snapshot then
	// accounts for those deltas). adjustmentEpoch is the latest epoch a delta
	// was anchored to.
	balanceAdjustment int64
	adjustmentEpoch   phase0.Epoch

	// pending holds unrevealed won bids, kept for 2 epochs. Only these count as
	// "pending" in the UI and for topup checks.
	pending map[phase0.Slot]*PendingPayment
}

// PaymentTracker tracks payment obligations and live balance adjustments per
// builder key, across both bid flows (p2p and Builder API). Fed by the
// InclusionTracker (won bids) and RevealService (reveals); consumed by the
// lifecycle manager (top-ups), the key registry (effective balances) and the
// WebUI. Passive and thread-safe: it runs no goroutine of its own.
//
// Accounting is per key because a payment is owed by the key whose bid won —
// with several managed keys bidding, charging the wrong one would let an
// underfunded key keep bidding while a funded one looks broke.
type PaymentTracker struct {
	mu   sync.Mutex
	keys map[uint64]*keyPayments

	chainSvc chain.Service
	log      logrus.FieldLogger
}

// NewPaymentTracker creates a new payment tracker.
func NewPaymentTracker(chainSvc chain.Service, log logrus.FieldLogger) *PaymentTracker {
	return &PaymentTracker{
		keys:     make(map[uint64]*keyPayments, 8),
		chainSvc: chainSvc,
		log:      log.WithField("component", "payment-tracker"),
	}
}

// forKey returns the key's accounting, creating it on first use. Callers must
// hold mu.
func (t *PaymentTracker) forKey(keyIndex uint64) *keyPayments {
	entry, ok := t.keys[keyIndex]
	if !ok {
		entry = &keyPayments{pending: make(map[phase0.Slot]*PendingPayment, 8)}
		t.keys[keyIndex] = entry
	}

	return entry
}

// RecordWonBid records a won bid as a pending payment (unrevealed) against the
// key whose bid was included. If we later reveal, MarkRevealed moves it from
// pending to a balance deduction; otherwise it stays pending for 2 epochs and
// then expires.
func (t *PaymentTracker) RecordWonBid(keyIndex uint64, slot phase0.Slot, value uint64) {
	epoch := t.chainSvc.GetEpochOfSlot(slot)

	t.mu.Lock()

	t.forKey(keyIndex).pending[slot] = &PendingPayment{
		KeyIndex: keyIndex,
		Slot:     slot,
		Epoch:    epoch,
		Value:    value,
	}

	t.mu.Unlock()

	t.log.WithFields(logrus.Fields{
		"key_index": keyIndex,
		"slot":      slot,
		"epoch":     epoch,
		"value":     value,
	}).Info("Recorded won bid as pending payment")
}

// MarkRevealed moves a won bid from pending to an immediate balance deduction on
// the key that owes it.
func (t *PaymentTracker) MarkRevealed(keyIndex uint64, slot phase0.Slot) {
	slotEpoch := t.chainSvc.GetEpochOfSlot(slot)

	t.mu.Lock()

	entry := t.forKey(keyIndex)

	payment, ok := entry.pending[slot]
	if !ok {
		t.mu.Unlock()
		return
	}

	value := payment.Value
	delete(entry.pending, slot)

	// Deduct from live balance, anchored to this slot's epoch so the
	// reconciler keeps the delta until the snapshot advances past it.
	entry.balanceAdjustment -= int64(value)
	anchorEpoch(entry, slotEpoch)

	t.mu.Unlock()

	t.log.WithFields(logrus.Fields{
		"key_index": keyIndex,
		"slot":      slot,
		"value":     value,
	}).Info("Revealed bid: deducted from live balance")
}

// AddDeposit credits a deposit/topup to the key's live balance adjustment,
// anchored to the current epoch. The credit is reconciled away by
// ReconcileToEpoch once the authoritative snapshot advances past that epoch.
func (t *PaymentTracker) AddDeposit(keyIndex, amount uint64) {
	currentEpoch := t.chainSvc.GetCurrentEpoch()

	t.mu.Lock()

	entry := t.forKey(keyIndex)
	entry.balanceAdjustment += int64(amount)
	anchorEpoch(entry, currentEpoch)

	t.mu.Unlock()

	t.log.WithFields(logrus.Fields{
		"key_index": keyIndex,
		"amount":    amount,
	}).Info("Deposit added to live balance")
}

// anchorEpoch advances the adjustment's anchor epoch so the reconciler keeps the
// current delta through opEpoch.
func anchorEpoch(entry *keyPayments, opEpoch phase0.Epoch) {
	if opEpoch > entry.adjustmentEpoch {
		entry.adjustmentEpoch = opEpoch
	}
}

// GetBalanceAdjustment returns the key's cumulative balance adjustment since the
// last state refresh. It implements builder_keys.BalanceAdjuster.
func (t *PaymentTracker) GetBalanceAdjustment(keyIndex uint64) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.keys[keyIndex]
	if !ok {
		return 0
	}

	return entry.balanceAdjustment
}

// ReconcileToEpoch drops each key's local adjustment once the authoritative
// builder snapshot advances past the epoch the adjustment is anchored to: the
// newer snapshot already accounts for every reveal/top-up from earlier epochs.
// Deltas anchored to the snapshot's own epoch are retained (not yet reflected).
// Safe to call every refresh; a no-op until the epoch advances.
func (t *PaymentTracker) ReconcileToEpoch(snapshotEpoch phase0.Epoch) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, entry := range t.keys {
		if snapshotEpoch <= entry.adjustmentEpoch {
			continue
		}

		entry.balanceAdjustment = 0
		entry.adjustmentEpoch = snapshotEpoch
	}
}

// GetTotalPendingPayments returns the sum of unrevealed won bid obligations
// across all keys (disputed payments — winning block reorged out — excluded).
func (t *PaymentTracker) GetTotalPendingPayments() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	var total uint64

	for keyIndex := range t.keys {
		total += t.pendingForKeyLocked(keyIndex)
	}

	return total
}

// GetPendingPayments returns one key's unrevealed won bid obligations.
func (t *PaymentTracker) GetPendingPayments(keyIndex uint64) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.pendingForKeyLocked(keyIndex)
}

// pendingForKeyLocked sums a key's undisputed pending payments. Callers must
// hold mu.
func (t *PaymentTracker) pendingForKeyLocked(keyIndex uint64) uint64 {
	entry, ok := t.keys[keyIndex]
	if !ok {
		return 0
	}

	var total uint64

	for _, payment := range entry.pending {
		if payment.Disputed {
			continue
		}

		total += payment.Value
	}

	return total
}

// SetPaymentDisputed flags (or clears) a pending payment whose winning block
// was reorged out. An already settled payment (revealed and deducted) cannot
// be rolled back locally — the on-chain payment quorum decides its fate — so
// the dispute is only logged in that case.
func (t *PaymentTracker) SetPaymentDisputed(keyIndex uint64, slot phase0.Slot, disputed bool) {
	t.mu.Lock()

	entry, hasKey := t.keys[keyIndex]

	var payment *PendingPayment
	if hasKey {
		payment = entry.pending[slot]
	}

	if payment != nil {
		payment.Disputed = disputed
	}

	t.mu.Unlock()

	logCtx := t.log.WithFields(logrus.Fields{
		"key_index": keyIndex,
		"slot":      slot,
		"disputed":  disputed,
	})

	switch {
	case payment != nil:
		logCtx.Info("Updated pending payment dispute state (reorg)")
	case disputed:
		logCtx.Warn("Winning block reorged out after the payment settled locally — " +
			"the on-chain payment quorum decides the final outcome")
	}
}

// PruneExpiredPayments removes pending payments older than 2 epochs.
func (t *PaymentTracker) PruneExpiredPayments(currentEpoch phase0.Epoch) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for keyIndex, entry := range t.keys {
		for slot, payment := range entry.pending {
			if currentEpoch <= payment.Epoch+1 {
				continue
			}

			t.log.WithFields(logrus.Fields{
				"key_index":     keyIndex,
				"slot":          slot,
				"payment_epoch": payment.Epoch,
				"current_epoch": currentEpoch,
				"value":         payment.Value,
			}).Debug("Pruning expired pending payment")

			delete(entry.pending, slot)
		}
	}
}
