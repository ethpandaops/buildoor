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
	Skipped     bool   // request arrived after the slot's reveal deadline
	Error       string // failure reason (when Success is false)
	Attempt     int    // 1-based
	MaxAttempts int

	// Withheld reveals (configured per-slot fault injection); the builder
	// identity fields are populated so the event doubles as the receipt that
	// the configured fault was applied.
	Withheld      bool   // reveal intentionally withheld by a slot action
	Action        string // the configured action (RevealActionWithhold)
	BuilderIndex  uint64
	BuilderPubkey string
}

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
// Builder API modules and their enable flags.
type RevealService struct {
	cfg          *config.Config // read cfg.EPBS.RevealTime live, never cache
	signer       *Signer
	publisher    envelopePublisher
	chainSvc     chain.Service
	builderSvc   *payload_builder.Service // reveal success/failure stats
	payments     *PaymentTracker          // optional; nil-guarded
	slotActions  *SlotActionsStore        // optional; nil-guarded (per-slot fault injection)
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
	attempts    int
	nextAttempt time.Time
	done        bool
}

// NewRevealService creates a new reveal service. slotActions may be nil (no
// per-slot fault injection).
func NewRevealService(
	cfg *config.Config,
	signer *Signer,
	publisher envelopePublisher,
	chainSvc chain.Service,
	builderSvc *payload_builder.Service,
	payments *PaymentTracker,
	slotActions *SlotActionsStore,
	log logrus.FieldLogger,
) *RevealService {
	return &RevealService{
		cfg:         cfg,
		signer:      signer,
		publisher:   publisher,
		chainSvc:    chainSvc,
		builderSvc:  builderSvc,
		payments:    payments,
		slotActions: slotActions,
		requests:    make(chan *RevealRequest, 16),
		pending:     make(map[phase0.Slot]*revealState, 8),
		log:         log.WithField("component", "reveal-service"),
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
func (s *RevealService) SubscribeResults(capacity int) *utils.Subscription[*RevealResult] {
	return s.results.Subscribe(capacity, false)
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
// slots already scheduled (from either flow) are dropped; requests arriving
// after the slot's end are recorded as skipped — past the slot the 75% payload
// deadline has long passed and a reveal is worthless.
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

	// Per-slot fault injection: a configured "withhold" action suppresses the
	// envelope publication entirely. The done marker keeps the slot's dedup
	// semantics (exactly one withheld event, no timer, no retries); the
	// payload stays untouched in the payload cache for inspection, and the
	// pending payment is left to expire like any other missed reveal.
	if action, ok := s.slotActionFor(slot); ok && action.Reveal == RevealActionWithhold {
		s.pending[slot] = &revealState{req: req, done: true}

		s.results.Fire(&RevealResult{
			Slot:          slot,
			Transport:     req.Transport,
			Withheld:      true,
			Action:        RevealActionWithhold,
			BuilderIndex:  s.builderIndex.Load(),
			BuilderPubkey: s.signer.Pubkey().String(),
		})

		s.log.WithFields(logrus.Fields{
			"slot":       slot,
			"transport":  req.Transport,
			"action":     RevealActionWithhold,
			"block_hash": fmt.Sprintf("%x", req.Payload.BlockHash[:8]),
		}).Warn("Intentionally withholding payload envelope reveal (configured slot action)")

		return
	}

	now := time.Now()
	slotStart := s.chainSvc.SlotToTime(slot)
	due := slotStart.Add(time.Duration(s.cfg.EPBS.RevealTime) * time.Millisecond)
	deadline := slotStart.Add(s.chainSvc.GetChainSpec().SecondsPerSlot)

	if now.After(deadline) {
		s.pending[slot] = &revealState{req: req, done: true}

		s.results.Fire(&RevealResult{
			Slot:        slot,
			Transport:   req.Transport,
			Skipped:     true,
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

		if err := s.publish(state.req); err != nil {
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
		})

		s.log.WithFields(logrus.Fields{
			"slot":       slot,
			"block_hash": fmt.Sprintf("%x", state.req.Payload.BlockHash[:8]),
			"transport":  state.req.Transport,
		}).Info("Payload revealed")
	}

	s.pruneDone(now)
}

// handlePublishFailure surfaces a failed reveal attempt and either schedules a
// retry or gives up once the retry budget is spent.
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

// slotActionFor returns the configured action for a slot; nil-guards the
// optional store.
func (s *RevealService) slotActionFor(slot phase0.Slot) (*SlotAction, bool) {
	if s.slotActions == nil {
		return nil, false
	}

	action, ok := s.slotActions.Get(slot)
	if !ok || action == nil {
		return nil, false
	}

	return action, true
}

// publish builds and signs the payload's envelope and submits it (with blobs /
// KZG proofs) to the beacon node under a bounded timeout.
func (s *RevealService) publish(req *RevealRequest) error {
	forkVersion, err := s.chainSvc.GetForkVersion()
	if err != nil {
		return fmt.Errorf("failed to get current fork version: %w", err)
	}

	signedEnvelope, blobs, cellProofs, err := BuildSignedEnvelope(req.Payload, RevealContext{
		BuilderIndex:          s.builderIndex.Load(),
		BeaconBlockRoot:       req.BlockInfo.Root,
		ParentBeaconBlockRoot: req.BlockInfo.ParentRoot,
	}, s.signer, forkVersion, s.chainSvc.GetGenesis().GenesisValidatorsRoot)
	if err != nil {
		return fmt.Errorf("failed to build signed envelope: %w", err)
	}

	if len(blobs) > 0 {
		s.log.WithFields(logrus.Fields{
			"blob_count":      len(blobs),
			"kzg_proof_count": len(cellProofs),
		}).Info("Including blobs and kzg proofs with envelope publish")
	}

	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()

	if err := s.publisher.SubmitExecutionPayloadEnvelope(ctx, signedEnvelope, blobs, cellProofs); err != nil {
		return fmt.Errorf("failed to submit envelope: %w", err)
	}

	return nil
}
