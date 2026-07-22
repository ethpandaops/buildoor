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
// broadcastValidation is the beacon-API broadcast_validation level
// (gossip | consensus | consensus_and_equivocation).
type envelopePublisher interface {
	SubmitExecutionPayloadEnvelope(ctx context.Context, envelope *eth2all.SignedExecutionPayloadEnvelope,
		blobs [][]byte, kzgProofs [][]byte, broadcastValidation string) error
}

// headVoteSource provides head-vote participation for reveal vote gates
// (implemented by *chain.HeadVoteTracker; interface for testability).
type headVoteSource interface {
	SubscribeUpdates() *utils.Subscription[*chain.HeadVoteUpdate]
	GetParticipation(slot phase0.Slot, root phase0.Root) (chain.HeadVoteUpdate, bool)
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
	// RevealSkipReasonDisabled marks a reveal suppressed by the global
	// reveal.enabled switch.
	RevealSkipReasonDisabled = "disabled"
	// RevealSkipReasonLate marks a reveal request that arrived after the
	// slot's deadline (only applies without a deadline bypass).
	RevealSkipReasonLate = "late"
	// RevealSkipReasonVoteGateTimeout marks a vote-gated reveal whose
	// participation threshold was never reached before the slot expired.
	RevealSkipReasonVoteGateTimeout = "vote_gate_timeout"
)

// RevealService publishes execution payload envelopes once the slot's reveal
// gates open. It runs its own main loop: requests arrive on a channel, due
// times are awaited with a timer (no polling), head-vote updates open vote
// gates event-driven, reveals are deduped per slot (the first request wins
// regardless of transport), and failed publishes are retried per the
// configured retry policy. It is independent of the p2p bidder and Builder
// API modules and their enable flags. Each slot's reveal follows the slot's
// frozen action plan (suppression, gate mode, reveal time, vote threshold,
// broadcast validation, deadline bypass) — the plan service is the single
// per-slot settings authority.
type RevealService struct {
	cfg          *config.Config // shared live config (reveal settings resolve via planSvc.Freeze)
	signer       *Signer
	publisher    envelopePublisher
	chainSvc     chain.Service
	builderSvc   *payload_builder.Service // reveal success/failure stats
	payments     *PaymentTracker          // optional; nil-guarded
	planSvc      *action_plan.PlanService // per-slot scheduling/settings authority; required
	votes        headVoteSource           // optional; nil = vote gates can never open
	builderIndex atomic.Uint64

	requests chan *RevealRequest
	results  utils.Dispatcher[*RevealResult]
	pending  map[phase0.Slot]*revealState // owned by the run loop; no mutex

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	log    logrus.FieldLogger
}

// revealState is the run-loop-owned schedule/gate/retry state for one slot's
// reveal.
type revealState struct {
	req      *RevealRequest
	settings *action_plan.ResolvedRevealSettings     // frozen reveal settings for the slot
	envelope *eth2all.SignedExecutionPayloadEnvelope // built on the first attempt, reused on retries
	blobs    [][]byte
	proofs   [][]byte
	attempts int

	voteGateMet bool
	timeDue     time.Time // when the time gate opens (gate modes involving time)
	expiry      time.Time // when an unsatisfied vote gate gives up
	nextAttempt time.Time
	done        bool
}

// voteGated reports whether the state's gate mode involves the vote gate.
func (st *revealState) voteGated() bool {
	switch st.settings.GateMode {
	case config.RevealGateVote, config.RevealGateVoteOrTime, config.RevealGateVoteAndTime:
		return true
	default:
		return false
	}
}

// gateSatisfied reports whether the reveal may be published at the given time.
func (st *revealState) gateSatisfied(now time.Time) bool {
	switch st.settings.GateMode {
	case config.RevealGateVote:
		return st.voteGateMet
	case config.RevealGateVoteOrTime:
		return st.voteGateMet || !now.Before(st.timeDue)
	case config.RevealGateVoteAndTime:
		return st.voteGateMet && !now.Before(st.timeDue)
	default: // config.RevealGateTime
		return !now.Before(st.timeDue)
	}
}

// NewRevealService creates a new reveal service. planSvc is required — every
// reveal decision resolves the slot's frozen action plan; passing nil is a
// programming error. votes is the head-vote tracker backing the vote gates;
// it may be nil (vote gates then never open and expire at the slot end).
func NewRevealService(
	cfg *config.Config,
	signer *Signer,
	publisher envelopePublisher,
	chainSvc chain.Service,
	builderSvc *payload_builder.Service,
	payments *PaymentTracker,
	planSvc *action_plan.PlanService,
	votes headVoteSource,
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
		votes:      votes,
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

// run is the main loop: schedule incoming requests, open vote gates on
// head-vote updates, publish due reveals, and keep the timer armed to the
// earliest pending attempt.
func (s *RevealService) run() {
	defer s.wg.Done()

	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	var voteChan <-chan *chain.HeadVoteUpdate

	if s.votes != nil {
		voteSub := s.votes.SubscribeUpdates()
		defer voteSub.Unsubscribe()

		voteChan = voteSub.Channel()
	}

	for {
		select {
		case <-s.ctx.Done():
			return
		case req := <-s.requests:
			s.schedule(req)
		case update := <-voteChan:
			s.handleVoteUpdate(update)
		case <-timer.C:
			s.processDue(time.Now())
		}

		s.rearm(timer)
	}
}

// handleVoteUpdate opens the vote gate of a pending reveal once the
// committing block's participation reaches the slot's threshold.
func (s *RevealService) handleVoteUpdate(update *chain.HeadVoteUpdate) {
	state, ok := s.pending[update.Slot]
	if !ok || state.done || state.voteGateMet || !state.voteGated() {
		return
	}

	// A state already publishing (or-mode satisfied by the time gate) keeps
	// its retry schedule; just record the gate as met.
	if state.attempts > 0 {
		state.voteGateMet = true
		return
	}

	if update.BlockRoot != state.req.BlockInfo.Root ||
		update.ParticipationPct < float64(state.settings.VoteThresholdPct) {
		return
	}

	state.voteGateMet = true

	next := time.Now()
	if state.settings.GateMode == config.RevealGateVoteAndTime && state.timeDue.After(next) {
		next = state.timeDue
	}

	state.nextAttempt = next

	s.log.WithFields(logrus.Fields{
		"slot":          update.Slot,
		"participation": fmt.Sprintf("%.1f%%", update.ParticipationPct),
		"threshold":     state.settings.VoteThresholdPct,
		"due_in":        time.Until(next),
	}).Info("Reveal vote gate opened")
}

// schedule registers a reveal request under its slot's frozen reveal
// settings. Requests for slots already scheduled (from either flow) are
// dropped. The frozen plan decides suppression, the gate mode (time / vote /
// combinations), the effective reveal time, the vote threshold, broadcast
// validation, the retry policy and whether the late-arrival deadline check
// is bypassed; without a bypass, requests arriving after the slot's end are
// recorded as skipped — past the slot the payload deadline has long passed
// and a reveal is worthless.
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
	settings := frozen.Reveal
	maxAttempts := int(settings.MaxAttempts)

	if settings.Suppressed {
		// Keep the dedupe entry marked done so a second RequestReveal for
		// the slot cannot publish either.
		s.pending[slot] = &revealState{req: req, settings: settings, done: true}

		reason := RevealSkipReasonDisabled
		if frozen.Plan != nil && frozen.Plan.Reveal != nil &&
			frozen.Plan.Reveal.Mode == action_plan.ModeDisabled {
			reason = RevealSkipReasonPlanDisabled
		}

		s.results.Fire(&RevealResult{
			Slot:        slot,
			Transport:   req.Transport,
			Skipped:     true,
			SkipReason:  reason,
			MaxAttempts: maxAttempts,
		})

		s.log.WithFields(logrus.Fields{
			"slot":      slot,
			"transport": req.Transport,
			"reason":    reason,
		}).Info("Reveal suppressed")

		return
	}

	now := time.Now()
	slotStart := s.chainSvc.SlotToTime(slot)
	slotDuration := s.chainSvc.GetChainSpec().SecondsPerSlot
	timeDue := slotStart.Add(time.Duration(settings.RevealTimeMs) * time.Millisecond)
	deadline := slotStart.Add(slotDuration)

	if !settings.BypassDeadline && now.After(deadline) {
		s.pending[slot] = &revealState{req: req, settings: settings, done: true}

		s.results.Fire(&RevealResult{
			Slot:        slot,
			Transport:   req.Transport,
			Skipped:     true,
			SkipReason:  RevealSkipReasonLate,
			MaxAttempts: maxAttempts,
		})

		s.log.WithFields(logrus.Fields{
			"slot":      slot,
			"transport": req.Transport,
			"deadline":  deadline,
		}).Warn("Reveal request arrived after slot end, skipping reveal")

		return
	}

	// Unsatisfied vote gates give up at the slot end (one extra slot with a
	// plan-level deadline bypass, matching the custom-reveal-time clamp).
	expiry := deadline
	if settings.BypassDeadline {
		expiry = deadline.Add(slotDuration)
	}

	state := &revealState{
		req:      req,
		settings: settings,
		timeDue:  timeDue,
		expiry:   expiry,
	}

	if state.voteGated() {
		if s.votes == nil {
			s.log.WithField("slot", slot).Warn(
				"Reveal vote gate configured but head vote tracking is unavailable")
		} else if p, ok := s.votes.GetParticipation(slot, req.BlockInfo.Root); ok &&
			p.ParticipationPct >= float64(settings.VoteThresholdPct) {
			state.voteGateMet = true
		}
	}

	// The first attempt time per gate mode; vote-waiting states park on the
	// expiry (a vote update pulls them forward).
	switch settings.GateMode {
	case config.RevealGateVote:
		state.nextAttempt = expiry
		if state.voteGateMet {
			state.nextAttempt = now
		}
	case config.RevealGateVoteOrTime:
		state.nextAttempt = timeDue
		if state.voteGateMet {
			state.nextAttempt = now
		}
	case config.RevealGateVoteAndTime:
		state.nextAttempt = expiry
		if state.voteGateMet {
			state.nextAttempt = timeDue
		}
	default: // config.RevealGateTime
		state.nextAttempt = timeDue
	}

	if state.nextAttempt.Before(now) {
		state.nextAttempt = now
	}

	s.pending[slot] = state

	s.log.WithFields(logrus.Fields{
		"slot":      slot,
		"transport": req.Transport,
		"gate_mode": settings.GateMode,
		"vote_met":  state.voteGateMet,
		"due_in":    time.Until(state.nextAttempt),
	}).Debug("Scheduled payload reveal")
}

// processDue publishes every pending reveal whose attempt time has come and
// whose gates are open, expires unsatisfied vote gates, handles success
// bookkeeping and bounded retries, then prunes stale entries.
func (s *RevealService) processDue(now time.Time) {
	for slot, state := range s.pending {
		if state.done || state.nextAttempt.After(now) {
			continue
		}

		if !state.gateSatisfied(now) {
			if now.Before(state.expiry) {
				// Due but not publishable yet (races around gate changes):
				// park on the expiry; a vote update pulls it forward.
				state.nextAttempt = state.expiry
				continue
			}

			state.done = true

			s.results.Fire(&RevealResult{
				Slot:        slot,
				Transport:   state.req.Transport,
				Skipped:     true,
				SkipReason:  RevealSkipReasonVoteGateTimeout,
				MaxAttempts: int(state.settings.MaxAttempts),
			})

			s.log.WithFields(logrus.Fields{
				"slot":      slot,
				"threshold": state.settings.VoteThresholdPct,
			}).Warn("Reveal vote gate never opened, withholding payload")

			continue
		}

		state.attempts++

		s.log.WithFields(logrus.Fields{
			"slot":         slot,
			"transport":    state.req.Transport,
			"attempt":      state.attempts,
			"max_attempts": state.settings.MaxAttempts,
		}).Info("Revealing payload")

		if state.envelope == nil {
			envelope, blobs, proofs, err := s.buildEnvelope(state.req)
			if err != nil {
				s.handlePublishFailure(slot, state, now, err)
				continue
			}

			state.envelope, state.blobs, state.proofs = envelope, blobs, proofs
		}

		if err := s.publish(state.envelope, state.blobs, state.proofs,
			state.settings.BroadcastValidation); err != nil {
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
			MaxAttempts: int(state.settings.MaxAttempts),
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
	maxAttempts := int(state.settings.MaxAttempts)

	s.log.WithError(err).WithFields(logrus.Fields{
		"slot":         slot,
		"attempt":      state.attempts,
		"max_attempts": maxAttempts,
	}).Error("Failed to submit reveal")

	s.results.Fire(&RevealResult{
		Slot:        slot,
		Transport:   state.req.Transport,
		Success:     false,
		Error:       err.Error(),
		Attempt:     state.attempts,
		MaxAttempts: maxAttempts,
		Envelope:    state.envelope,
	})

	if state.attempts >= maxAttempts {
		state.done = true
		s.builderSvc.IncrementRevealsFailed()
		s.log.WithField("slot", slot).Error("Giving up on reveal after max attempts")

		return
	}

	state.nextAttempt = now.Add(time.Duration(state.settings.RetryIntervalMs) * time.Millisecond)
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
// beacon node under a bounded timeout, requesting the slot's broadcast
// validation level.
func (s *RevealService) publish(envelope *eth2all.SignedExecutionPayloadEnvelope,
	blobs, proofs [][]byte, broadcastValidation string) error {
	if len(blobs) > 0 {
		s.log.WithFields(logrus.Fields{
			"blob_count":      len(blobs),
			"kzg_proof_count": len(proofs),
		}).Info("Including blobs and kzg proofs with envelope publish")
	}

	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()

	if err := s.publisher.SubmitExecutionPayloadEnvelope(ctx, envelope, blobs, proofs,
		broadcastValidation); err != nil {
		return fmt.Errorf("failed to submit envelope: %w", err)
	}

	return nil
}
