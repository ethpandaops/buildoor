// Package p2p_bidder implements the active p2p bidding flow of ePBS (bid
// windows, bid submission, competitor tracking, registration state).
package p2p_bidder

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	gloasspec "github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// Registration state constants for the p2p bidder service.
const (
	RegistrationStateUnknown             int32 = 0 // Not checked yet
	RegistrationStatePending             int32 = 1 // Deposit submitted, waiting for inclusion in beacon state
	RegistrationStateRegistered          int32 = 2 // Builder registered and deposit epoch finalized
	RegistrationStateWaitingGloas        int32 = 3 // Waiting for Gloas fork activation
	RegistrationStatePendingFinalization int32 = 4 // Builder in beacon state but deposit epoch not finalized
	RegistrationStateExiting             int32 = 5 // Exit submitted, withdrawable epoch set but not reached
	RegistrationStateExited              int32 = 6 // Withdrawable epoch passed, builder has exited
	RegistrationStateUnregistered        int32 = 7 // Builder not in beacon state and no deposit in progress
)

// RegistrationStateName returns the string name for a registration state.
func RegistrationStateName(state int32) string {
	switch state {
	case RegistrationStateUnknown:
		return "unknown"
	case RegistrationStatePending:
		return "pending"
	case RegistrationStateRegistered:
		return "registered"
	case RegistrationStateWaitingGloas:
		return "waiting_gloas"
	case RegistrationStatePendingFinalization:
		return "pending_finalization"
	case RegistrationStateExiting:
		return "exiting"
	case RegistrationStateExited:
		return "exited"
	case RegistrationStateUnregistered:
		return "unregistered"
	default:
		return "unknown"
	}
}

// Bid submission statuses reported via BidSubmissionEvent.Status.
const (
	// BidStatusSubmitted means the bid was constructed and gossiped.
	BidStatusSubmitted = "submitted"
	// BidStatusConstructed means the bid was built and signed, but the network
	// submission failed.
	BidStatusConstructed = "constructed"
	// BidStatusFailed means bid construction itself failed.
	BidStatusFailed = "failed"
)

// BidSubmissionEvent represents a bid submission attempt (success or failure).
type BidSubmissionEvent struct {
	Slot      phase0.Slot
	BlockHash [32]byte
	Value     uint64
	BidCount  int
	Success   bool
	Warning   string // Non-fatal warning (e.g. "no proposer preferences")
	Error     string

	// Status is one of BidStatusSubmitted/BidStatusConstructed/BidStatusFailed
	// for submission attempts (empty for pre-construction skip events).
	Status string
	// SignedBid is the constructed signed bid; nil when construction failed
	// (or for pre-construction skip events).
	SignedBid *eth2all.SignedExecutionPayloadBid
	// CompetitorHighGwei is the highest competitor bid known for the slot at
	// fire time (our own builder index excluded); nil when none is known.
	CompetitorHighGwei *uint64
}

// Service is the p2p bidder orchestrator that handles time-scheduled bidding.
// It submits bids during the slot's bid window, tracks competitor bids from
// the gossip stream, and maintains the builder's registration state. Once a
// slot's block is produced (head event), the bidder is done with that slot —
// reveals, inclusion tracking, and payment accounting live in the shared
// payload_bidder services.
type Service struct {
	signer                *payload_bidder.Signer
	blsSigner             *signer.BLSSigner
	scheduler             *Scheduler
	bidCreator            *BidCreator
	bidTracker            *BidTracker
	clClient              *beacon.Client
	chainSvc              chain.Service
	propPrefsStore        *memstore.Store[phase0.Slot, *gloasspec.SignedProposerPreferences]
	planSvc               *action_plan.PlanService
	builderIndex          uint64
	builderPubkey         phase0.BLSPubKey
	bidSubmissionDispatch *utils.Dispatcher[*BidSubmissionEvent]
	builderSvc            *payload_builder.Service

	enabled           atomic.Bool
	registrationState atomic.Int32
	ctx               context.Context
	cancel            context.CancelFunc
	log               logrus.FieldLogger
	wg                sync.WaitGroup
}

// NewService creates a new p2p bidder service. propPrefsStore is the shared
// per-slot proposer preferences store (owned by
// payload_bidder.ProposerPreferencesService); it gates bidding — slots without
// a cached preference are skipped. It may be nil, in which case no bids are
// submitted. planSvc is the mandatory per-slot action plan service — the
// single scheduling/settings authority: the scheduler freezes each slot's
// plan on first evaluation and the frozen snapshot alone decides whether and
// how the slot is bid on (a plan may activate bidding for a slot even when
// ePBS is globally disabled).
func NewService(
	clClient *beacon.Client,
	chainSvc chain.Service,
	blsSigner *signer.BLSSigner,
	propPrefsStore *memstore.Store[phase0.Slot, *gloasspec.SignedProposerPreferences],
	planSvc *action_plan.PlanService,
	log logrus.FieldLogger,
) (*Service, error) {
	serviceLog := log.WithField("component", "p2p-bidder")

	// Create the shared payload bidder signer
	epbsSigner := payload_bidder.NewSigner(blsSigner)

	s := &Service{
		signer:                epbsSigner,
		blsSigner:             blsSigner,
		clClient:              clClient,
		chainSvc:              chainSvc,
		propPrefsStore:        propPrefsStore,
		planSvc:               planSvc,
		builderPubkey:         blsSigner.PublicKey(),
		bidSubmissionDispatch: &utils.Dispatcher[*BidSubmissionEvent]{},
		log:                   serviceLog,
	}

	// BidTracker, Scheduler, and BidCreator are created in Start after we have
	// the chain spec and genesis info

	return s, nil
}

// SetEnabled sets the enabled state of the p2p bidder service. The flag is
// status reporting only (WebUI/API); the per-slot bid decision comes solely
// from the action plan's frozen snapshots.
func (s *Service) SetEnabled(enabled bool) {
	s.enabled.Store(enabled)
}

// IsEnabled returns whether the p2p bidder service is enabled (status
// reporting only; not consulted by the bid scheduler).
func (s *Service) IsEnabled() bool {
	return s.enabled.Load()
}

// SubscribeBidSubmissions subscribes to bid submission events. Blocking
// subscriptions never drop events (authoritative consumers, e.g. the slot
// results tracker); non-blocking ones drop on a full buffer (e.g. the WebUI
// SSE bridge).
func (s *Service) SubscribeBidSubmissions(capacity int, blocking bool) *utils.Subscription[*BidSubmissionEvent] {
	return s.bidSubmissionDispatch.Subscribe(capacity, blocking)
}

// FireBidSubmission fires a bid submission event.
func (s *Service) FireBidSubmission(event *BidSubmissionEvent) {
	s.bidSubmissionDispatch.Fire(event)
}

// Start starts the p2p bidder service.
func (s *Service) Start(ctx context.Context, builderSvc *payload_builder.Service) error {
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.builderSvc = builderSvc

	// Load builder index from chain service and determine initial registration state
	if s.chainSvc.GetCurrentFork() < version.DataVersionGloas {
		s.log.Info("No builders in beacon state (pre-Gloas), waiting for registration")
		s.builderIndex = 0
		s.registrationState.Store(RegistrationStateWaitingGloas)
	} else if builderInfo := s.chainSvc.GetBuilderByPubkey(s.builderPubkey); builderInfo == nil {
		s.log.Info("Builder not found in beacon state")
		s.builderIndex = 0
		s.registrationState.Store(RegistrationStateUnregistered)
	} else {
		s.builderIndex = builderInfo.Index
		s.registrationState.Store(s.computeRegistrationState(builderInfo))
		s.log.WithFields(logrus.Fields{
			"builder_index":  s.builderIndex,
			"builder_pubkey": fmt.Sprintf("%x", s.builderPubkey[:8]),
			"state":          RegistrationStateName(s.registrationState.Load()),
		}).Info("Builder found in beacon state")
	}

	// Initialize components
	s.bidTracker = NewBidTracker(s.builderIndex, s.log)
	s.bidCreator = NewBidCreator(
		s.signer,
		s.clClient,
		s.chainSvc,
		s.builderIndex,
		s.log,
	)
	// The scheduler skips bidding for slots without cached proposer preferences:
	// the BN's gossip validator silently rejects such bids.
	s.scheduler = NewScheduler(
		s.chainSvc,
		s.bidCreator,
		s.bidTracker,
		builderSvc.GetPayloadCache(),
		s,
		s.blsSigner,
		s.propPrefsStore,
		s.planSvc,
		s.log,
	)

	// Start the main event loop
	s.wg.Add(1)

	go s.run()

	s.log.Info("p2p bidder service started")

	return nil
}

// Stop stops the p2p bidder service.
func (s *Service) Stop() {
	s.log.Info("Stopping p2p bidder service")

	if s.cancel != nil {
		s.cancel()
	}

	s.wg.Wait()

	s.log.Info("p2p bidder service stopped")
}

// run is the main event loop.
func (s *Service) run() {
	defer s.wg.Done()

	headSub := s.clClient.Events().SubscribeHead()
	bidSub := s.clClient.Events().SubscribeBids()
	epochSub := s.chainSvc.SubscribeEpochStats()
	ticker := time.NewTicker(10 * time.Millisecond)

	defer headSub.Unsubscribe()
	defer bidSub.Unsubscribe()
	defer epochSub.Unsubscribe()
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return

		case event := <-headSub.Channel():
			s.handleHeadEvent(event)

		case event := <-bidSub.Channel():
			s.handleBidEvent(event)

		case _, ok := <-epochSub.Channel():
			if ok {
				s.RefreshRegistrationState()
			}

		case <-ticker.C:
			// The enable policy is per slot: the scheduler resolves it from
			// the frozen action plan (a plan may activate bidding for a slot
			// even when ePBS is globally disabled). Registration stays a hard
			// availability gate.
			if s.IsRegistered() {
				s.scheduler.ProcessTick(s.ctx)
			}
		}
	}
}

// handleHeadEvent closes bidding for the slot — the block has been produced.
func (s *Service) handleHeadEvent(event *beacon.HeadEvent) {
	s.log.WithFields(logrus.Fields{
		"slot": event.Slot,
		"root": fmt.Sprintf("%x", event.Block[:8]),
	}).Info("Head event received")

	// Close bidding for this slot - block already produced
	s.scheduler.OnHeadEvent(event)
}

// handleBidEvent processes a bid event from the event stream.
func (s *Service) handleBidEvent(event *beacon.BidEvent) {
	isOurs := event.BuilderIndex == s.builderIndex

	bid := &ExecutionPayloadBid{
		Slot:             event.Slot,
		ParentBlockHash:  event.ParentBlockHash,
		ParentBlockRoot:  event.ParentBlockRoot,
		BlockHash:        event.BlockHash,
		FeeRecipient:     event.FeeRecipient,
		GasLimit:         event.GasLimit,
		BuilderIndex:     event.BuilderIndex,
		Value:            event.Value,
		ExecutionPayment: event.ExecutionPayment,
	}

	s.bidTracker.TrackBid(bid, isOurs)

	s.log.WithFields(logrus.Fields{
		"slot":          event.Slot,
		"builder_index": event.BuilderIndex,
		"value":         event.Value,
		"is_ours":       isOurs,
	}).Debug("Bid event received")
}

// GetRegistrationState returns the current registration state.
func (s *Service) GetRegistrationState() int32 {
	return s.registrationState.Load()
}

// IsRegistered returns whether the builder has a valid index and its deposit is finalized.
func (s *Service) IsRegistered() bool {
	return s.registrationState.Load() == RegistrationStateRegistered
}

// IsActive returns whether the builder can actively participate (registered or pending finalization).
func (s *Service) IsActive() bool {
	state := s.registrationState.Load()
	return state == RegistrationStateRegistered || state == RegistrationStatePendingFinalization
}

// SetRegistrationPending marks the builder as having a deposit in flight.
// Called by the lifecycle manager when a deposit is submitted.
func (s *Service) SetRegistrationPending() {
	s.registrationState.Store(RegistrationStatePending)
	s.log.Info("Builder deposit submitted, waiting for beacon chain inclusion")
}

// SetBuilderRegistered updates the builder index when the lifecycle manager detects registration.
// It sets the appropriate state based on finalization status.
// Called by the lifecycle manager's registration callback.
func (s *Service) SetBuilderRegistered(index uint64) {
	s.builderIndex = index

	if s.bidCreator != nil {
		s.bidCreator.SetBuilderIndex(index)
	}

	if s.bidTracker != nil {
		s.bidTracker.SetBuilderIndex(index)
	}

	// Determine the correct state based on finalization
	info := s.chainSvc.GetBuilderByPubkey(s.builderPubkey)
	if info != nil {
		s.registrationState.Store(s.computeRegistrationState(info))
	} else {
		s.registrationState.Store(RegistrationStatePendingFinalization)
	}

	s.log.WithFields(logrus.Fields{
		"builder_index": index,
		"state":         RegistrationStateName(s.registrationState.Load()),
	}).Info("Builder registration detected, updating state")
}

// computeRegistrationState determines the registration state from beacon chain builder info.
func (s *Service) computeRegistrationState(info *chain.BuilderInfo) int32 {
	if info.WithdrawableEpoch != chain.FarFutureEpoch {
		// Exit has been initiated
		finalizedEpoch := s.chainSvc.GetFinalizedEpoch()
		if info.WithdrawableEpoch <= uint64(finalizedEpoch) {
			return RegistrationStateExited
		}

		return RegistrationStateExiting
	}

	// Check if deposit epoch is finalized
	finalizedEpoch := s.chainSvc.GetFinalizedEpoch()
	if info.DepositEpoch < uint64(finalizedEpoch) {
		return RegistrationStateRegistered
	}

	return RegistrationStatePendingFinalization
}

// RefreshRegistrationState re-evaluates the registration state from the chain service.
// Called periodically to detect state transitions (e.g. finalization, exit).
func (s *Service) RefreshRegistrationState() {
	currentState := s.registrationState.Load()

	// States that don't need refresh
	if currentState == RegistrationStateWaitingGloas || currentState == RegistrationStateUnknown {
		return
	}

	info := s.chainSvc.GetBuilderByPubkey(s.builderPubkey)
	if info == nil {
		// Builder not in state — keep current state if pending (deposit submitted),
		// otherwise mark as unregistered
		if currentState != RegistrationStatePending && currentState != RegistrationStateUnregistered {
			s.registrationState.Store(RegistrationStateUnregistered)
			s.log.Info("Builder no longer found in beacon state")
		}

		return
	}

	newState := s.computeRegistrationState(info)
	if newState != currentState {
		s.registrationState.Store(newState)
		s.log.WithFields(logrus.Fields{
			"old_state": RegistrationStateName(currentState),
			"new_state": RegistrationStateName(newState),
		}).Info("Builder registration state changed")
	}
}

// GetBidTracker returns the bid tracker.
func (s *Service) GetBidTracker() *BidTracker {
	return s.bidTracker
}

// GetBuilderIndex returns the builder index.
func (s *Service) GetBuilderIndex() uint64 {
	return s.builderIndex
}

// GetBuilderPubkey returns the builder public key.
func (s *Service) GetBuilderPubkey() phase0.BLSPubKey {
	return s.builderPubkey
}
