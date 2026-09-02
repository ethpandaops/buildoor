// Package tx_plan_verifier closes the loop of the testing build source: for
// every included payload that carries a tx plan it fetches the canonical
// block from the EL and checks that the block holds exactly the planned
// transactions, in order. A mismatch is loud: error log, metric, and the
// slot result's tx_plan status.
package tx_plan_verifier

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/metrics"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/slot_results"
)

// blockWait bounds how long the verifier waits for the EL to know an
// included block (Gloas reveals land a few seconds after the block).
const blockWait = 30 * time.Second

// Counters is the process-lifetime tally the API serves.
type Counters struct {
	Checked  uint64 `json:"checked"`
	Match    uint64 `json:"match"`
	Mismatch uint64 `json:"mismatch"`
	NotFound uint64 `json:"block_not_found"`
	Missed   uint64 `json:"missed"`
	Orphaned uint64 `json:"orphaned"`
}

// Verifier subscribes to inclusion events and verifies tx plans.
type Verifier struct {
	inclusion *payload_bidder.InclusionTracker
	builder   *payload_builder.TestingBuilder
	results   *slot_results.Tracker
	log       logrus.FieldLogger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	checked, match, mismatch, notFound, missed, orphaned atomic.Uint64

	mu      sync.Mutex
	planned map[phase0.Slot]bool // slots with a plan, for verdict events
}

// New creates a verifier over the inclusion tracker's events.
func New(
	inclusion *payload_bidder.InclusionTracker,
	builder *payload_builder.TestingBuilder,
	results *slot_results.Tracker,
	log logrus.FieldLogger,
) *Verifier {
	return &Verifier{
		inclusion: inclusion,
		builder:   builder,
		results:   results,
		log:       log.WithField("component", "tx-plan-verifier"),
		planned:   make(map[phase0.Slot]bool),
	}
}

// Start begins consuming inclusion events.
func (v *Verifier) Start(ctx context.Context) {
	v.ctx, v.cancel = context.WithCancel(ctx)

	includedSub := v.inclusion.SubscribeIncluded(16, true)
	statusSub := v.inclusion.SubscribePayloadStatus(16, true)

	v.wg.Add(1)

	go func() {
		defer v.wg.Done()
		defer includedSub.Unsubscribe()
		defer statusSub.Unsubscribe()

		for {
			select {
			case <-v.ctx.Done():
				return
			case event, ok := <-includedSub.Channel():
				if !ok {
					return
				}

				v.handleIncluded(event)
			case event, ok := <-statusSub.Channel():
				if !ok {
					return
				}

				v.handleStatus(event)
			}
		}
	}()
}

// Stop ends the verifier.
func (v *Verifier) Stop() {
	if v.cancel != nil {
		v.cancel()
	}

	v.wg.Wait()
}

// Counters returns the process-lifetime verification tally.
func (v *Verifier) Counters() Counters {
	return Counters{
		Checked:  v.checked.Load(),
		Match:    v.match.Load(),
		Mismatch: v.mismatch.Load(),
		NotFound: v.notFound.Load(),
		Missed:   v.missed.Load(),
		Orphaned: v.orphaned.Load(),
	}
}

func (v *Verifier) handleIncluded(event *payload_bidder.PayloadIncludedEvent) {
	payload := event.Payload
	if payload == nil || payload.TxPlan == nil || payload.Attributes == nil {
		return
	}

	slot := payload.Attributes.ProposalSlot

	v.mu.Lock()
	v.planned[slot] = true
	v.mu.Unlock()

	// Verification waits on the EL and must not block the event loop.
	v.wg.Add(1)

	go func() {
		defer v.wg.Done()
		v.verify(slot, common.Hash(payload.BlockHash), payload.TxPlan.Hashes)
	}()
}

func (v *Verifier) verify(slot phase0.Slot, blockHash common.Hash, expected []common.Hash) {
	ctx, cancel := context.WithTimeout(v.ctx, blockWait)
	defer cancel()

	v.checked.Add(1)

	check, err := v.builder.VerifyBlock(ctx, blockHash, expected)
	if err != nil {
		v.notFound.Add(1)
		metrics.TestingPlanChecks.WithLabelValues(string(slot_results.TxPlanBlockNotFound)).Inc()
		v.results.RecordTxPlanCheck(slot, slot_results.TxPlanBlockNotFound, 0, -1, err.Error())

		v.log.WithError(err).WithFields(logrus.Fields{
			"slot":       slot,
			"block_hash": blockHash.Hex(),
			"expected":   len(expected),
		}).Error("TX PLAN CHECK FAILED: included block not served by the EL")

		return
	}

	fields := logrus.Fields{
		"slot":       slot,
		"block_hash": blockHash.Hex(),
		"expected":   len(expected),
		"actual":     check.ActualCount,
	}

	if !check.Match {
		v.mismatch.Add(1)
		metrics.TestingPlanChecks.WithLabelValues(string(slot_results.TxPlanMismatch)).Inc()
		v.results.RecordTxPlanCheck(slot, slot_results.TxPlanMismatch, check.ActualCount, check.FirstMismatch, check.Detail)

		fields["first_mismatch"] = check.FirstMismatch
		fields["detail"] = check.Detail
		v.log.WithFields(fields).Error("TX PLAN CHECK FAILED: included block does not hold the planned transactions in order")

		return
	}

	v.match.Add(1)
	metrics.TestingPlanChecks.WithLabelValues(string(slot_results.TxPlanMatch)).Inc()
	v.results.RecordTxPlanCheck(slot, slot_results.TxPlanMatch, check.ActualCount, -1, "")
	v.log.WithFields(fields).Info("Tx plan check passed: included block holds exactly the planned transactions")
}

// handleStatus mirrors a Gloas payload verdict onto the plan: a missed or
// orphaned payload means the planned transactions never landed.
func (v *Verifier) handleStatus(event *payload_bidder.PayloadStatusEvent) {
	v.mu.Lock()
	planned := v.planned[event.Slot]
	v.mu.Unlock()

	if !planned {
		return
	}

	switch event.Verdict {
	case payload_bidder.PayloadVerdictMissed:
		v.missed.Add(1)
		metrics.TestingPlanChecks.WithLabelValues(string(slot_results.TxPlanMissed)).Inc()
		v.results.RecordTxPlanCheck(event.Slot, slot_results.TxPlanMissed, 0, -1,
			"won slot but the next block builds on an older payload")
		v.log.WithField("slot", event.Slot).Error("TX PLAN CHECK FAILED: payload missed, planned transactions did not land")
	case payload_bidder.PayloadVerdictOrphaned:
		v.orphaned.Add(1)
		metrics.TestingPlanChecks.WithLabelValues(string(slot_results.TxPlanOrphaned)).Inc()
		v.results.RecordTxPlanCheck(event.Slot, slot_results.TxPlanOrphaned, 0, -1,
			"won block was reorged out")
		v.log.WithField("slot", event.Slot).Error("TX PLAN CHECK FAILED: block orphaned, planned transactions did not land")
	}
}
