package slot_results

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/p2p_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

const (
	// updateFireInterval coalesces SSE update events per slot during bursts
	// (p2p interval bidding): the first attempt fires immediately, later ones
	// at most once per interval. The record itself always grows.
	updateFireInterval = time.Second

	// maxSlotCatchUp bounds how many missed slot ticks the baseline loop
	// processes after a stall or clock jump.
	maxSlotCatchUp = 4
)

// Tracker owns the per-slot outcome history and the artifact store. It is
// the single sink for build/bid/submission/reveal/inclusion signals — event
// subscriptions for the in-process services, direct recorder calls for the
// request-scoped Builder API handlers — and prunes both stores to their
// retention windows on epoch transitions.
type Tracker struct {
	cfg      *config.Config
	chainSvc chain.Service
	stateDB  *db.Database
	planSvc  *action_plan.PlanService

	builderSvc       *payload_builder.Service
	epbsSvc          *p2p_bidder.Service // may be nil pre-Gloas
	revealSvc        *payload_bidder.RevealService
	inclusionTracker *payload_bidder.InclusionTracker

	store     *memstore.Store[phase0.Slot, *SlotResult]
	artifacts *ArtifactStore

	mu        sync.Mutex
	lastFired map[phase0.Slot]time.Time // update-event coalescing per slot

	updates utils.Dispatcher[*SlotResult]

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	log    logrus.FieldLogger
}

// NewTracker creates the slot results tracker. stateDB must be non-nil (a
// disabled database is fine); epbsSvc and revealSvc may be nil when the Gloas
// fork is not scheduled.
func NewTracker(cfg *config.Config, chainSvc chain.Service, stateDB *db.Database,
	planSvc *action_plan.PlanService, builderSvc *payload_builder.Service,
	epbsSvc *p2p_bidder.Service, revealSvc *payload_bidder.RevealService,
	inclusionTracker *payload_bidder.InclusionTracker, log logrus.FieldLogger) *Tracker {
	trackerLog := log.WithField("component", "slot-results")

	return &Tracker{
		cfg:              cfg,
		chainSvc:         chainSvc,
		stateDB:          stateDB,
		planSvc:          planSvc,
		builderSvc:       builderSvc,
		epbsSvc:          epbsSvc,
		revealSvc:        revealSvc,
		inclusionTracker: inclusionTracker,
		store:            memstore.New[phase0.Slot, *SlotResult](),
		artifacts:        NewArtifactStore(stateDB, trackerLog),
		lastFired:        make(map[phase0.Slot]time.Time, 8),
		log:              trackerLog,
	}
}

// SetPersistence migrates any legacy won_blocks namespace into slot results,
// then attaches the kv_store persistence and rehydrates prior history.
func (t *Tracker) SetPersistence(ctx context.Context, stateDB *db.Database) {
	migrateWonBlocks(stateDB, t.chainSvc.GetChainSpec().SlotsPerEpoch, t.log)

	t.store.SetPersistence(ctx, db.NewKVPersistence(stateDB, Namespace, ResultCodec{}), t.log)
}

// Start subscribes to all outcome sources and launches the tracker loop and
// the artifact writer.
func (t *Tracker) Start(ctx context.Context) error {
	t.ctx, t.cancel = context.WithCancel(ctx)

	t.artifacts.Start(t.ctx)

	// Authoritative outcome sources subscribe blocking: the tracker loop only
	// does in-memory clone-mutate-put work, so producers never stall
	// meaningfully, and history never silently loses events.
	readySub := t.builderSvc.SubscribePayloadReady(16, true)
	startedSub := t.builderSvc.SubscribePayloadBuildStarted(16, true)
	failedSub := t.builderSvc.SubscribePayloadBuildFailed(16, true)
	skippedSub := t.builderSvc.SubscribeBuildSkipped(16, true)
	epochSub := t.chainSvc.SubscribeEpochStats()

	var bidSub *utils.Subscription[*p2p_bidder.BidSubmissionEvent]
	if t.epbsSvc != nil {
		bidSub = t.epbsSvc.SubscribeBidSubmissions(64, true)
	}

	var revealSub *utils.Subscription[*payload_bidder.RevealResult]
	if t.revealSvc != nil {
		revealSub = t.revealSvc.SubscribeResults(16, true)
	}

	includedSub := t.inclusionTracker.SubscribeIncluded(16, true)
	payloadStatusSub := t.inclusionTracker.SubscribePayloadStatus(16, true)

	t.wg.Add(1)

	go t.run(readySub, startedSub, failedSub, skippedSub, bidSub, revealSub,
		includedSub, payloadStatusSub, epochSub)

	t.log.Info("Slot results tracker started")

	return nil
}

// Stop terminates the tracker loop, drains the artifact writer and flushes
// the result store. Must be called before the state-db closes.
func (t *Tracker) Stop() {
	if t.cancel != nil {
		t.cancel()
	}

	t.wg.Wait()
	t.artifacts.Stop()
	t.store.Stop()

	t.log.Info("Slot results tracker stopped")
}

//nolint:gocyclo // single select loop over all outcome sources
func (t *Tracker) run(
	readySub *utils.Subscription[*payload_builder.Payload],
	startedSub *utils.Subscription[*payload_builder.PayloadBuildStartedEvent],
	failedSub *utils.Subscription[*payload_builder.PayloadBuildFailedEvent],
	skippedSub *utils.Subscription[*payload_builder.BuildSkippedEvent],
	bidSub *utils.Subscription[*p2p_bidder.BidSubmissionEvent],
	revealSub *utils.Subscription[*payload_bidder.RevealResult],
	includedSub *utils.Subscription[*payload_bidder.PayloadIncludedEvent],
	payloadStatusSub *utils.Subscription[*payload_bidder.PayloadStatusEvent],
	epochSub *utils.Subscription[*chain.EpochStats],
) {
	defer t.wg.Done()
	defer readySub.Unsubscribe()
	defer startedSub.Unsubscribe()
	defer failedSub.Unsubscribe()
	defer skippedSub.Unsubscribe()
	defer includedSub.Unsubscribe()
	defer payloadStatusSub.Unsubscribe()
	defer epochSub.Unsubscribe()

	// nil subscriptions must never fire in the select; use closed-nil-safe
	// channel indirection.
	var bidCh <-chan *p2p_bidder.BidSubmissionEvent
	if bidSub != nil {
		defer bidSub.Unsubscribe()
		bidCh = bidSub.Channel()
	}

	var revealCh <-chan *payload_bidder.RevealResult
	if revealSub != nil {
		defer revealSub.Unsubscribe()
		revealCh = revealSub.Channel()
	}

	// Slot clock for baseline materialization: makes "planned/active but
	// nothing happened" observable.
	lastTickedSlot := t.chainSvc.GetCurrentSlot()
	slotTimer := time.NewTimer(t.durationToNextSlot())

	defer slotTimer.Stop()

	for {
		select {
		case <-t.ctx.Done():
			return

		case payload, ok := <-readySub.Channel():
			if !ok {
				return
			}

			t.handlePayloadReady(payload)

		case event, ok := <-startedSub.Channel():
			if !ok {
				return
			}

			t.handleBuildStarted(event)

		case event, ok := <-failedSub.Channel():
			if !ok {
				return
			}

			t.handleBuildFailed(event)

		case event, ok := <-skippedSub.Channel():
			if !ok {
				return
			}

			t.handleBuildSkipped(event)

		case event, ok := <-bidCh:
			if !ok {
				return
			}

			t.handleBidSubmission(event)

		case result, ok := <-revealCh:
			if !ok {
				return
			}

			t.handleRevealResult(result)

		case event, ok := <-includedSub.Channel():
			if !ok {
				return
			}

			t.handleIncluded(event)

		case event, ok := <-payloadStatusSub.Channel():
			if !ok {
				return
			}

			t.handlePayloadStatus(event)

		case epochStats, ok := <-epochSub.Channel():
			if !ok {
				return
			}

			t.pruneForEpoch(epochStats.Epoch)

		case <-slotTimer.C:
			currentSlot := t.chainSvc.GetCurrentSlot()

			// Bounded catch-up: a stall or clock jump must not generate
			// unbounded baseline history.
			firstSlot := lastTickedSlot + 1
			if currentSlot > firstSlot+maxSlotCatchUp {
				firstSlot = currentSlot - maxSlotCatchUp
			}

			for slot := firstSlot; slot <= currentSlot; slot++ {
				t.materializeBaseline(slot)
				t.finalizeWaitingBaseline(slot - 1)
			}

			if currentSlot > lastTickedSlot {
				lastTickedSlot = currentSlot
			}

			slotTimer.Reset(t.durationToNextSlot())
		}
	}
}

// durationToNextSlot computes the wait until the next slot boundary,
// re-derived every tick so clock jumps self-correct.
func (t *Tracker) durationToNextSlot() time.Duration {
	nextSlotTime := t.chainSvc.SlotToTime(t.chainSvc.GetCurrentSlot() + 1)

	wait := time.Until(nextSlotTime)
	if wait < 10*time.Millisecond {
		wait = 10 * time.Millisecond
	}

	return wait
}

// materializeBaseline creates a baseline record at slot start for slots with
// an explicit plan or any effectively active service, so slots where nothing
// arrives afterwards (no attributes, no requests) remain explainable.
func (t *Tracker) materializeBaseline(slot phase0.Slot) {
	frozen := t.planSvc.Freeze(slot)
	if !frozen.Build.PlanInvolved {
		return
	}

	t.upsert(slot, func(result *SlotResult) {
		if result.Build != nil {
			return
		}

		if frozen.Build.Build {
			result.Build = &BuildOutcome{Status: BuildStatusWaitingAttributes, At: time.Now()}
		} else {
			result.Build = &BuildOutcome{
				Status:     BuildStatusSkipped,
				SkipReason: frozen.Build.SkipReason,
				At:         time.Now(),
			}
		}
	})
}

// finalizeWaitingBaseline flips a still-waiting baseline to no_attributes
// once its slot has passed.
func (t *Tracker) finalizeWaitingBaseline(slot phase0.Slot) {
	if existing, ok := t.store.Get(slot); !ok ||
		existing.Build == nil || existing.Build.Status != BuildStatusWaitingAttributes {
		return
	}

	t.upsert(slot, func(result *SlotResult) {
		if result.Build != nil && result.Build.Status == BuildStatusWaitingAttributes {
			result.Build.Status = BuildStatusNoAttributes
			result.Build.At = time.Now()
		}
	})
}

func (t *Tracker) handlePayloadReady(payload *payload_builder.Payload) {
	if payload == nil || payload.Attributes == nil {
		return
	}

	slot := payload.Attributes.ProposalSlot

	numTxs := 0
	forkVersion := version.DataVersionUnknown

	if payload.ExecutionPayload != nil {
		numTxs = len(payload.ExecutionPayload.Transactions)
		forkVersion = payload.ExecutionPayload.Version
	}

	numBlobs := 0
	if payload.BlobsBundle != nil {
		numBlobs = len(payload.BlobsBundle.Commitments)
	}

	blockValue := "0"
	if payload.BlockValue != nil {
		blockValue = payload.BlockValue.String()
	}

	t.upsert(slot, func(result *SlotResult) {
		result.Build = &BuildOutcome{
			Status:          BuildStatusReady,
			BlockHash:       fmt.Sprintf("%#x", payload.BlockHash),
			BlockValueWei:   blockValue,
			NumTransactions: numTxs,
			NumBlobs:        numBlobs,
			FeeRecipient:    payload.FeeRecipient.Hex(),
			At:              payload.ReadyAt,
		}
	})

	if t.cfg.SlotArtifactCaptureEnabled && payload.ExecutionPayload != nil {
		if err := t.artifacts.StorePayload(slot, forkVersion, payload.ExecutionPayload); err != nil {
			t.log.WithError(err).WithField("slot", slot).Warn("Failed to store payload artifact")
		}
	}
}

func (t *Tracker) handleBuildStarted(event *payload_builder.PayloadBuildStartedEvent) {
	t.upsert(event.Slot, func(result *SlotResult) {
		// Never regress a ready/failed outcome to started (events may race).
		if result.Build != nil && result.Build.Status != BuildStatusWaitingAttributes &&
			result.Build.Status != BuildStatusNoAttributes {
			return
		}

		result.Build = &BuildOutcome{Status: BuildStatusStarted, At: event.StartedAt}
	})
}

func (t *Tracker) handleBuildFailed(event *payload_builder.PayloadBuildFailedEvent) {
	t.upsert(event.Slot, func(result *SlotResult) {
		result.Build = &BuildOutcome{
			Status: BuildStatusFailed,
			Error:  event.Error,
			At:     event.FailedAt,
		}
	})
}

func (t *Tracker) handleBuildSkipped(event *payload_builder.BuildSkippedEvent) {
	t.upsert(event.Slot, func(result *SlotResult) {
		if result.Build != nil && result.Build.Status != BuildStatusWaitingAttributes {
			return
		}

		result.Build = &BuildOutcome{
			Status:     BuildStatusSkipped,
			SkipReason: event.Reason,
			At:         time.Now(),
		}
	})
}

func (t *Tracker) handleBidSubmission(event *p2p_bidder.BidSubmissionEvent) {
	attempt := BidAttempt{
		Transport:          string(payload_builder.BidTransportP2P),
		TotalValueGwei:     event.Value,
		CompetitorHighGwei: event.CompetitorHighGwei,
		Error:              event.Error,
		At:                 time.Now(),
	}

	switch event.Status {
	case p2p_bidder.BidStatusSubmitted:
		attempt.Status = BidStatusSubmitted
	case p2p_bidder.BidStatusConstructed:
		attempt.Status = BidStatusConstructed
	case p2p_bidder.BidStatusFailed:
		attempt.Status = BidStatusFailed
	default:
		// Pre-construction skip (e.g. missing proposer preferences).
		attempt.Status = BidStatusSuppressed

		if attempt.Error == "" {
			attempt.Error = event.Warning
		}
	}

	if t.cfg.SlotArtifactCaptureEnabled && event.SignedBid != nil {
		idx, err := t.artifacts.StoreBid(event.Slot, event.SignedBid.Version, event.SignedBid,
			BidArtifactMeta{
				Transport:      string(payload_builder.BidTransportP2P),
				TotalValueGwei: event.Value,
				At:             time.Now().UnixMilli(),
			})
		if err != nil {
			t.log.WithError(err).WithField("slot", event.Slot).Warn("Failed to store bid artifact")

			if attempt.Error == "" {
				attempt.Error = err.Error()
			}
		} else {
			attempt.ArtifactIndex = &idx
		}
	}

	t.appendBid(event.Slot, attempt)
}

func (t *Tracker) handleRevealResult(result *payload_bidder.RevealResult) {
	attempt := RevealAttempt{
		Transport:  string(result.Transport),
		SkipReason: result.SkipReason,
		Error:      result.Error,
		Attempt:    result.Attempt,
		At:         time.Now(),
	}

	switch {
	case result.Skipped && result.SkipReason == payload_bidder.RevealSkipReasonPlanDisabled:
		attempt.Status = RevealStatusSuppressed
	case result.Skipped:
		attempt.Status = RevealStatusSkipped
	case result.Success:
		attempt.Status = RevealStatusPublished
	default:
		attempt.Status = RevealStatusFailed
	}

	if t.cfg.SlotArtifactCaptureEnabled && result.Envelope != nil {
		if err := t.artifacts.StoreEnvelope(result.Slot, result.Envelope.Version,
			result.Envelope); err != nil {
			t.log.WithError(err).WithField("slot", result.Slot).
				Warn("Failed to store envelope artifact")
		}
	}

	t.upsert(result.Slot, func(slotResult *SlotResult) {
		appendCapped(&slotResult.RevealAttempts, attempt, "reveal_attempts", slotResult)
	})
}

func (t *Tracker) handleIncluded(event *payload_bidder.PayloadIncludedEvent) {
	if event.WonBlock == nil {
		return
	}

	won := event.WonBlock

	slot := phase0.Slot(won.Slot)

	// Pre-Gloas the payload is embedded in the winning block, so inclusion IS
	// canonical; from Gloas on the verdict arrives with the follow-up block.
	payloadStatus := PayloadStatusCanonical
	if t.chainSvc.ActiveForkAtEpoch(t.chainSvc.GetEpochOfSlot(slot)) >= version.DataVersionGloas {
		payloadStatus = PayloadStatusPending
	}

	t.upsert(slot, func(result *SlotResult) {
		result.Inclusion = &InclusionResult{
			Source:          won.Source,
			BlockHash:       won.BlockHash,
			NumTransactions: won.NumTransactions,
			NumBlobs:        won.NumBlobs,
			ValueWei:        won.ValueWei,
			ValueETH:        won.ValueETH,
			Timestamp:       time.UnixMilli(won.Timestamp),
			PayloadStatus:   payloadStatus,
		}
	})
}

// handlePayloadStatus records the canonical verdict for a won slot's payload,
// derived from the canonical chain's ancestry. Reorg-revised verdicts simply
// overwrite the previous status (the event fires once per change).
func (t *Tracker) handlePayloadStatus(event *payload_bidder.PayloadStatusEvent) {
	t.upsert(event.Slot, func(result *SlotResult) {
		if result.Inclusion == nil {
			// A verdict without a recorded inclusion (e.g. record pruned or
			// created fresh by this upsert) has nothing to attach to.
			return
		}

		result.Inclusion.PayloadStatus = PayloadStatus(event.Verdict)
		result.Inclusion.PayloadCheckSlot = event.NextBlockSlot
	})
}

// RecordBuilderAPIBid records a Builder API bid outcome (implements the
// builderapi.SlotResultRecorder contract). The signed bid object, when
// non-nil, is captured as a bid artifact.
func (t *Tracker) RecordBuilderAPIBid(slot phase0.Slot, forkName string, signedBid any,
	totalValueGwei, executionPaymentGwei uint64, status, errMsg string) {
	attempt := BidAttempt{
		Status:               BidStatus(status),
		Transport:            string(payload_builder.BidTransportBuilderAPI),
		TotalValueGwei:       totalValueGwei,
		ExecutionPaymentGwei: executionPaymentGwei,
		Error:                errMsg,
		At:                   time.Now(),
	}

	marshaler, isMarshaler := signedBid.(sszMarshaler)
	if t.cfg.SlotArtifactCaptureEnabled && isMarshaler {
		fork, err := version.DataVersionFromString(forkName)
		if err != nil {
			fork = version.DataVersionUnknown
		}

		idx, err := t.artifacts.StoreBid(slot, fork, marshaler, BidArtifactMeta{
			Transport:            string(payload_builder.BidTransportBuilderAPI),
			TotalValueGwei:       totalValueGwei,
			ExecutionPaymentGwei: executionPaymentGwei,
			At:                   time.Now().UnixMilli(),
		})
		if err != nil {
			t.log.WithError(err).WithField("slot", slot).Warn("Failed to store bid artifact")

			if attempt.Error == "" {
				attempt.Error = err.Error()
			}
		} else {
			attempt.ArtifactIndex = &idx
		}
	}

	t.appendBid(slot, attempt)
}

// RecordBlockSubmission records a proposer block submission outcome
// (implements the builderapi.SlotResultRecorder contract).
func (t *Tracker) RecordBlockSubmission(slot phase0.Slot, dialect, status, errMsg string) {
	t.upsert(slot, func(result *SlotResult) {
		submission := BlockSubmission{
			Dialect: dialect,
			Status:  SubmissionStatus(status),
			Error:   errMsg,
			At:      time.Now(),
		}
		appendCapped(&result.BlockSubmissions, submission, "block_submissions", result)
	})
}

func (t *Tracker) appendBid(slot phase0.Slot, attempt BidAttempt) {
	t.upsert(slot, func(result *SlotResult) {
		appendCapped(&result.Bids, attempt, "bids", result)
	})
}

// appendCapped appends an attempt honoring the per-kind retention cap.
func appendCapped[T any](list *[]T, item T, kind string, result *SlotResult) {
	if len(*list) >= maxAttemptsPerKind {
		if result.DroppedAttempts == nil {
			result.DroppedAttempts = make(map[string]int, 3)
		}

		result.DroppedAttempts[kind]++

		return
	}

	*list = append(*list, item)
}

// upsert is the single mutation path: get-or-create the slot's record
// (stamping epoch/fork and freezing the applied plan on creation), apply the
// mutation on a clone, store it and fire the update dispatcher (coalesced).
func (t *Tracker) upsert(slot phase0.Slot, mutate func(*SlotResult)) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var result *SlotResult
	if existing, ok := t.store.Get(slot); ok {
		result = existing.Clone()
	} else {
		frozen := t.planSvc.Freeze(slot)
		result = &SlotResult{
			Slot:        slot,
			Epoch:       uint64(t.chainSvc.GetEpochOfSlot(slot)),
			Fork:        frozen.Fork,
			AppliedPlan: frozen,
		}
	}

	mutate(result)
	result.UpdatedAt = time.Now()

	t.store.Put(slot, result)
	t.fireUpdate(slot, result)
}

// fireUpdate emits the updated record to subscribers, coalescing bursts per
// slot. Callers must hold t.mu.
func (t *Tracker) fireUpdate(slot phase0.Slot, result *SlotResult) {
	now := time.Now()
	if last, ok := t.lastFired[slot]; ok && now.Sub(last) < updateFireInterval {
		return
	}

	t.lastFired[slot] = now

	// Bound the coalescing map.
	if len(t.lastFired) > 64 {
		for firedSlot := range t.lastFired {
			if firedSlot+64 < slot {
				delete(t.lastFired, firedSlot)
			}
		}
	}

	t.updates.Fire(result.Clone())
}

// Get returns a deep copy of the slot's result, or nil when none exists.
func (t *Tracker) Get(slot phase0.Slot) *SlotResult {
	result, ok := t.store.Get(slot)
	if !ok {
		return nil
	}

	return result.Clone()
}

// GetRange returns deep copies of all results within [minSlot, maxSlot],
// slot-ascending.
func (t *Tracker) GetRange(minSlot, maxSlot phase0.Slot) []*SlotResult {
	entries := t.store.Entries()
	results := make([]*SlotResult, 0, len(entries))

	for slot, result := range entries {
		if slot >= minSlot && slot <= maxSlot {
			results = append(results, result.Clone())
		}
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Slot < results[j].Slot })

	return results
}

// GetWonBlocks returns the included-slot view in the legacy Bids Won wire
// shape: slot-descending, offset/limit paginated, with the total count of
// included slots. History length follows the result retention window.
func (t *Tracker) GetWonBlocks(offset, limit int) ([]*payload_bidder.WonBlock, int) {
	entries := t.store.Entries()
	included := make([]*SlotResult, 0, len(entries))

	for _, result := range entries {
		if result.Inclusion != nil {
			included = append(included, result)
		}
	}

	sort.Slice(included, func(i, j int) bool { return included[i].Slot > included[j].Slot })

	total := len(included)
	if offset < 0 {
		offset = 0
	}

	if offset >= total {
		return []*payload_bidder.WonBlock{}, total
	}

	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}

	wonBlocks := make([]*payload_bidder.WonBlock, 0, end-offset)
	for _, result := range included[offset:end] {
		wonBlocks = append(wonBlocks, &payload_bidder.WonBlock{
			Source:          result.Inclusion.Source,
			Slot:            uint64(result.Slot),
			BlockHash:       result.Inclusion.BlockHash,
			NumTransactions: result.Inclusion.NumTransactions,
			NumBlobs:        result.Inclusion.NumBlobs,
			ValueWei:        result.Inclusion.ValueWei,
			ValueETH:        result.Inclusion.ValueETH,
			Timestamp:       result.Inclusion.Timestamp.UnixMilli(),
		})
	}

	return wonBlocks, total
}

// Artifacts exposes the artifact store (WebUI artifact endpoints).
func (t *Tracker) Artifacts() *ArtifactStore {
	return t.artifacts
}

// SubscribeUpdates subscribes to slot result updates (non-blocking delivery;
// intended for the SSE bridge — the REST range endpoints are the source of
// truth).
func (t *Tracker) SubscribeUpdates(capacity int) *utils.Subscription[*SlotResult] {
	return t.updates.Subscribe(capacity, false)
}

// pruneForEpoch drops result summaries and artifacts outside their (separate)
// retention windows.
func (t *Tracker) pruneForEpoch(epoch phase0.Epoch) {
	slotsPerEpoch := t.chainSvc.GetChainSpec().SlotsPerEpoch

	if retention := t.cfg.SlotResultRetentionEpochs; retention > 0 && uint64(epoch) > retention {
		cutoff := phase0.Slot((uint64(epoch) - retention) * slotsPerEpoch)

		pruned := t.store.Prune(func(slot phase0.Slot) bool { return slot < cutoff })
		if pruned > 0 {
			t.log.WithFields(logrus.Fields{
				"epoch":  epoch,
				"cutoff": cutoff,
				"pruned": pruned,
			}).Debug("Pruned slot results")
		}

		t.mu.Lock()
		for slot := range t.lastFired {
			if slot < cutoff {
				delete(t.lastFired, slot)
			}
		}
		t.mu.Unlock()
	}

	if retention := t.cfg.SlotArtifactRetentionEpochs; retention > 0 && uint64(epoch) > retention {
		cutoff := phase0.Slot((uint64(epoch) - retention) * slotsPerEpoch)
		t.artifacts.PruneBefore(cutoff)
	}
}
