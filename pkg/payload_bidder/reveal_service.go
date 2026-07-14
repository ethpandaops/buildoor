package payload_bidder

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// envelopePublisher publishes a signed envelope to the beacon node
// (implemented by *beacon.Client; interface for testability).
type envelopePublisher interface {
	SubmitExecutionPayloadEnvelope(ctx context.Context, envelope *eth2all.SignedExecutionPayloadEnvelope,
		blobs [][]byte, kzgProofs [][]byte) error
}

// RevealRequest asks the RevealService to publish a payload's envelope at the
// configured reveal time. Both flows submit these; the service dedupes by slot.
type RevealRequest struct {
	Payload   *payload_builder.Payload
	BlockInfo *beacon.BlockInfo // root + parent root of the committing beacon block
	Transport payload_builder.BidTransport
}

// RevealResult reports the outcome of a reveal attempt.
type RevealResult struct {
	Slot        phase0.Slot
	Transport   payload_builder.BidTransport
	Success     bool
	Skipped     bool   // reveal was skipped without publishing (see SkipReason)
	SkipReason  string `json:"skip_reason,omitempty"` // RevealSkipReasonPlanDisabled | RevealSkipReasonLate
	Error       string // failure reason (when Success is false)
	Attempt     int    // 1-based
	MaxAttempts int

	// Envelope is the signed envelope built for the attempt. It is set from
	// construction onward on every attempt (including failed publishes) and
	// nil on skips and pre-construction failures.
	Envelope *eth2all.SignedExecutionPayloadEnvelope `json:"-"`
}

// Skip reasons carried by RevealResult.SkipReason on skipped reveals.
const (
	// RevealSkipReasonPlanDisabled marks a reveal suppressed by the slot's
	// frozen action plan.
	RevealSkipReasonPlanDisabled = "plan_disabled"
	// RevealSkipReasonLate marks a reveal request that arrived after the
	// slot's deadline (only applies without a deadline bypass).
	RevealSkipReasonLate = "late"
)

const (
	// maxRevealAttempts bounds how many times we retry a failed payload reveal.
	maxRevealAttempts = 3
	// revealRetryDelay is the wait between successive reveal attempts.
	revealRetryDelay = 500 * time.Millisecond
)

// RevealService publishes execution payload envelopes at the slot-relative
// reveal time. It runs its own main loop: requests arrive on a channel, due
// times are awaited with a timer (no polling), reveals are deduped per slot
// (the first request wins regardless of transport), and failed publishes are
// retried a bounded number of times. It is independent of the p2p bidder and
// Builder API modules and their enable flags. Each slot's reveal follows the
// slot's frozen action plan (suppression, effective reveal time, deadline
// bypass) — the plan service is the single per-slot settings authority.
type RevealService struct {
	cfg          *config.Config // shared live config (reveal timing resolves via planSvc.Freeze)
	signer       *Signer
	publisher    envelopePublisher
	chainSvc     chain.Service
	builderSvc   *payload_builder.Service // reveal success/failure stats
	payments     *PaymentTracker          // optional; nil-guarded
	planSvc      *action_plan.PlanService // per-slot scheduling/settings authority; required
	builderIndex atomic.Uint64

	requests chan *RevealRequest
	results  utils.Dispatcher[*RevealResult]
	pending  map[phase0.Slot]*revealState // owned by the run loop; no mutex

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	log    logrus.FieldLogger
}

// revealState is the run-loop-owned schedule/retry state for one slot's reveal.
type revealState struct {
	req         *RevealRequest
	envelope    *eth2all.SignedExecutionPayloadEnvelope // built on the first attempt, reused on retries
	blobs       [][]byte
	proofs      [][]byte
	attempts    int
	nextAttempt time.Time
	done        bool
}

// NewRevealService creates a new reveal service. planSvc is required — every
// reveal decision resolves the slot's frozen action plan; passing nil is a
// programming error.
func NewRevealService(
	cfg *config.Config,
	signer *Signer,
	publisher envelopePublisher,
	chainSvc chain.Service,
	builderSvc *payload_builder.Service,
	payments *PaymentTracker,
	planSvc *action_plan.PlanService,
	log logrus.FieldLogger,
) *RevealService {
	return &RevealService{
		cfg:        cfg,
		signer:     signer,
		publisher:  publisher,
		chainSvc:   chainSvc,
		builderSvc: builderSvc,
		payments:   payments,
		planSvc:    planSvc,
		requests:   make(chan *RevealRequest, 16),
		pending:    make(map[phase0.Slot]*revealState, 8),
		log:        log.WithField("component", "reveal-service"),
	}
}

// Start starts the reveal service's main loop.
func (s *RevealService) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	s.wg.Add(1)

	go s.run()

	s.log.Info("Reveal service started")

	return nil
}

// Stop stops the reveal service and waits for the main loop to exit.
func (s *RevealService) Stop() {
	if s.cancel != nil {
		s.cancel()
	}

	s.wg.Wait()

	s.log.Info("Reveal service stopped")
}

// SetBuilderIndex updates the builder index used when signing envelopes.
func (s *RevealService) SetBuilderIndex(index uint64) {
	s.builderIndex.Store(index)
}

// SubscribeResults subscribes to reveal results (consumed by the WebUI).
func (s *RevealService) SubscribeResults(capacity int, blocking bool) *utils.Subscription[*RevealResult] {
	return s.results.Subscribe(capacity, blocking)
}

// RequestReveal enqueues a reveal request; non-blocking: a full queue is
// logged and dropped (at most one reveal per slot, so the 16 buffer is
// generous).
func (s *RevealService) RequestReveal(req *RevealRequest) {
	select {
	case s.requests <- req:
	default:
		s.log.WithField("queue_len", len(s.requests)).Warn("Reveal request queue full, dropping request")
	}
}

// run is the main loop: schedule incoming requests, publish due reveals, and
// keep the timer armed to the earliest pending attempt.
func (s *RevealService) run() {
	defer s.wg.Done()

	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case req := <-s.requests:
			s.schedule(req)
		case <-timer.C:
			s.processDue(time.Now())
		}

		s.rearm(timer)
	}
}

// schedule registers a reveal request for its slot's reveal time. Requests for
// slots already scheduled (from either flow) are dropped. The slot's frozen
// action plan decides the effective reveal time, whether the reveal is
// suppressed entirely, and whether the late-arrival deadline check is
// bypassed; without a bypass, requests arriving after the slot's end are
// recorded as skipped — past the slot the 75% payload deadline has long
// passed and a reveal is worthless.
func (s *RevealService) schedule(req *RevealRequest) {
	if req == nil || req.Payload == nil || req.BlockInfo == nil {
		s.log.Warn("Dropping invalid reveal request (missing payload or block info)")
		return
	}

	slot := req.Payload.Attributes.ProposalSlot

	if _, exists := s.pending[slot]; exists {
		s.log.WithFields(logrus.Fields{
			"slot":      slot,
			"transport": req.Transport,
		}).Debug("Duplicate reveal request for slot, ignoring")

		return
	}

	frozen := s.planSvc.Freeze(slot)

	if frozen.Reveal.Suppressed {
		// Keep the dedupe entry marked done so a second RequestReveal for
		// the slot cannot publish either.
		s.pending[slot] = &revealState{req: req, done: true}

		s.results.Fire(&RevealResult{
			Slot:        slot,
			Transport:   req.Transport,
			Skipped:     true,
			SkipReason:  RevealSkipReasonPlanDisabled,
			MaxAttempts: maxRevealAttempts,
		})

		s.log.WithFields(logrus.Fields{
			"slot":      slot,
			"transport": req.Transport,
		}).Info("Reveal suppressed by the slot's action plan")

		return
	}

	now := time.Now()
	slotStart := s.chainSvc.SlotToTime(slot)
	due := slotStart.Add(time.Duration(frozen.Reveal.RevealTimeMs) * time.Millisecond)
	deadline := slotStart.Add(s.chainSvc.GetChainSpec().SecondsPerSlot)

	if !frozen.Reveal.BypassDeadline && now.After(deadline) {
		s.pending[slot] = &revealState{req: req, done: true}

		s.results.Fire(&RevealResult{
			Slot:        slot,
			Transport:   req.Transport,
			Skipped:     true,
			SkipReason:  RevealSkipReasonLate,
			MaxAttempts: maxRevealAttempts,
		})

		s.log.WithFields(logrus.Fields{
			"slot":      slot,
			"transport": req.Transport,
			"deadline":  deadline,
		}).Warn("Reveal request arrived after slot end, skipping reveal")

		return
	}

	nextAttempt := due
	if nextAttempt.Before(now) {
		nextAttempt = now
	}

	s.pending[slot] = &revealState{req: req, nextAttempt: nextAttempt}

	s.log.WithFields(logrus.Fields{
		"slot":      slot,
		"transport": req.Transport,
		"due_in":    time.Until(nextAttempt),
	}).Debug("Scheduled payload reveal")
}

// processDue publishes every pending reveal whose attempt time has come,
// handling success bookkeeping and bounded retries, then prunes stale entries.
func (s *RevealService) processDue(now time.Time) {
	for slot, state := range s.pending {
		if state.done || state.nextAttempt.After(now) {
			continue
		}

		state.attempts++

		s.log.WithFields(logrus.Fields{
			"slot":         slot,
			"transport":    state.req.Transport,
			"attempt":      state.attempts,
			"max_attempts": maxRevealAttempts,
		}).Info("Revealing payload")

		if state.envelope == nil {
			envelope, blobs, proofs, err := s.buildEnvelope(state.req)
			if err != nil {
				s.handlePublishFailure(slot, state, now, err)
				continue
			}

			state.envelope, state.blobs, state.proofs = envelope, blobs, proofs
		}

		if err := s.publish(state.envelope, state.blobs, state.proofs); err != nil {
			s.handlePublishFailure(slot, state, now, err)
			continue
		}

		state.done = true

		state.req.Payload.MarkRevealed(payload_builder.RevealRecord{
			Transport:       state.req.Transport,
			BeaconBlockRoot: state.req.BlockInfo.Root,
			At:              now,
		})

		if s.payments != nil {
			s.payments.MarkRevealed(slot)
		}

		s.builderSvc.IncrementRevealsSuccess()

		s.results.Fire(&RevealResult{
			Slot:        slot,
			Transport:   state.req.Transport,
			Success:     true,
			Attempt:     state.attempts,
			MaxAttempts: maxRevealAttempts,
			Envelope:    state.envelope,
		})

		s.log.WithFields(logrus.Fields{
			"slot":       slot,
			"block_hash": fmt.Sprintf("%x", state.req.Payload.BlockHash[:8]),
			"transport":  state.req.Transport,
		}).Info("Payload revealed")
	}

	s.pruneDone(now)
}

// handlePublishFailure surfaces a failed reveal attempt (envelope construction
// or network publish) and either schedules a retry or gives up once the retry
// budget is spent. The fired result carries the built envelope when
// construction succeeded (nil on pre-construction failures).
func (s *RevealService) handlePublishFailure(slot phase0.Slot, state *revealState, now time.Time, err error) {
	s.log.WithError(err).WithFields(logrus.Fields{
		"slot":         slot,
		"attempt":      state.attempts,
		"max_attempts": maxRevealAttempts,
	}).Error("Failed to submit reveal")

	s.results.Fire(&RevealResult{
		Slot:        slot,
		Transport:   state.req.Transport,
		Success:     false,
		Error:       err.Error(),
		Attempt:     state.attempts,
		MaxAttempts: maxRevealAttempts,
		Envelope:    state.envelope,
	})

	if state.attempts >= maxRevealAttempts {
		state.done = true
		s.builderSvc.IncrementRevealsFailed()
		s.log.WithField("slot", slot).Error("Giving up on reveal after max attempts")

		return
	}

	state.nextAttempt = now.Add(revealRetryDelay)
}

// pruneDone drops finished entries once their slot is more than 2 slots old;
// until then they stay as slot-dedup markers.
func (s *RevealService) pruneDone(now time.Time) {
	slotDuration := s.chainSvc.GetChainSpec().SecondsPerSlot

	for slot, state := range s.pending {
		if !state.done {
			continue
		}

		if now.After(s.chainSvc.SlotToTime(slot).Add(2 * slotDuration)) {
			delete(s.pending, slot)
		}
	}
}

// rearm resets the timer to the earliest pending attempt (or an hour when
// idle). Must be called after every loop iteration that may have changed the
// pending set.
func (s *RevealService) rearm(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}

	next := time.Hour
	now := time.Now()

	for _, state := range s.pending {
		if state.done {
			continue
		}

		if wait := state.nextAttempt.Sub(now); wait < next {
			next = wait
		}
	}

	if next < 0 {
		next = 0
	}

	timer.Reset(next)
}

// buildEnvelope constructs and signs the payload's envelope for the request's
// target slot and returns it together with the blobs / KZG proofs to publish
// alongside. Signing resolves the fork active at the TARGET slot (never the
// current fork), so deliberately late reveals crossing a slot/fork boundary
// are still signed under the fork the slot belongs to.
func (s *RevealService) buildEnvelope(req *RevealRequest) (
	envelope *eth2all.SignedExecutionPayloadEnvelope, blobs, proofs [][]byte, err error,
) {
	slot := req.Payload.Attributes.ProposalSlot
	fork := s.chainSvc.ActiveForkAtEpoch(s.chainSvc.GetEpochOfSlot(slot))

	forkVersion, err := s.chainSvc.GetChainSpec().GetForkVersion(fork)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get fork version for slot %d (%s): %w", slot, fork, err)
	}

	// The slot's frozen plan may carry a jq transform applied to the envelope
	// message before signing (idempotent Freeze).
	var envelopeTransform string
	if t := s.planSvc.Freeze(slot).Transforms; t != nil {
		envelopeTransform = t.Envelope
	}

	ctx, cancel := context.WithTimeout(s.ctx, transformTimeout)
	defer cancel()

	envelope, blobs, proofs, err = BuildSignedEnvelope(ctx, req.Payload, RevealContext{
		BuilderIndex:          s.builderIndex.Load(),
		BeaconBlockRoot:       req.BlockInfo.Root,
		ParentBeaconBlockRoot: req.BlockInfo.ParentRoot,
		Transform:             envelopeTransform,
	}, s.signer, forkVersion, s.chainSvc.GetGenesis().GenesisValidatorsRoot)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to build signed envelope: %w", err)
	}

	return envelope, blobs, proofs, nil
}

// publish submits a built signed envelope (with blobs / KZG proofs) to the
// beacon node under a bounded timeout.
func (s *RevealService) publish(envelope *eth2all.SignedExecutionPayloadEnvelope,
	blobs, proofs [][]byte) error {
	if len(blobs) > 0 {
		s.log.WithFields(logrus.Fields{
			"blob_count":      len(blobs),
			"kzg_proof_count": len(proofs),
		}).Info("Including blobs and kzg proofs with envelope publish")
	}

	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()

	if err := s.publisher.SubmitExecutionPayloadEnvelope(ctx, envelope, blobs, proofs); err != nil {
		return fmt.Errorf("failed to submit envelope: %w", err)
	}

	return nil
}
