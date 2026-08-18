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
	"github.com/ethpandaops/buildoor/pkg/builder_keys"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
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
	registry              *builder_keys.Registry
	scheduler             *Scheduler
	bidCreator            *BidCreator
	bidTracker            *BidTracker
	clClient              *beacon.Client
	chainSvc              chain.Service
	propPrefsStore        *memstore.Store[phase0.Slot, *gloasspec.SignedProposerPreferences]
	planSvc               *action_plan.PlanService
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
	registry *builder_keys.Registry,
	propPrefsStore *memstore.Store[phase0.Slot, *gloasspec.SignedProposerPreferences],
	planSvc *action_plan.PlanService,
	log logrus.FieldLogger,
) (*Service, error) {
	serviceLog := log.WithField("component", "p2p-bidder")

	s := &Service{
		registry:              registry,
		clClient:              clClient,
		chainSvc:              chainSvc,
		propPrefsStore:        propPrefsStore,
		planSvc:               planSvc,
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

	// Determine the initial registration state from the primary key's on-chain entry
	pubkey := s.GetBuilderPubkey()

	if s.chainSvc.GetCurrentFork() < version.DataVersionGloas {
		s.log.Info("No builders in beacon state (pre-Gloas), waiting for registration")
		s.registrationState.Store(RegistrationStateWaitingGloas)
	} else if builderInfo := s.chainSvc.GetBuilderByPubkey(pubkey); builderInfo == nil {
		s.log.Info("Builder not found in beacon state")
		s.registrationState.Store(RegistrationStateUnregistered)
	} else {
		s.registrationState.Store(s.computeRegistrationState(builderInfo))
		s.log.WithFields(logrus.Fields{
			"builder_index":  builderInfo.Index,
			"builder_pubkey": fmt.Sprintf("%x", pubkey[:8]),
			"state":          RegistrationStateName(s.registrationState.Load()),
		}).Info("Builder found in beacon state")
	}

	// Initialize components
	s.bidTracker = NewBidTracker(s.registry, s.log)
	s.bidCreator = NewBidCreator(
		s.clClient,
		s.chainSvc,
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
		s.registry,
		s.propPrefsStore,
		s.planSvc,
		builderSvc.GetConfig(),
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

	// Bid submissions run detached from the scheduler's tick, so they are
	// awaited separately.
	if s.scheduler != nil {
		s.scheduler.Wait()
	}

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

	// Head-change events reopen bidding for slots whose closing block was
	// reorged out (a nil channel simply never fires).
	var headChangeCh <-chan *chain.HeadChangeEvent

	if headTracker := s.chainSvc.GetHeadTracker(); headTracker != nil {
		headChangeSub := headTracker.SubscribeHeadChanges()
		defer headChangeSub.Unsubscribe()

		headChangeCh = headChangeSub.Channel()
	}

	for {
		select {
		case <-s.ctx.Done():
			return

		case event := <-headSub.Channel():
			s.handleHeadEvent(event)

		case event := <-bidSub.Channel():
			s.handleBidEvent(event)

		case epochStats, ok := <-epochSub.Channel():
			if ok {
				s.RefreshRegistrationState()

				// Prune per-slot bid state that left the retention window.
				if firstSlot := epochStats.Epoch * phase0.Epoch(s.chainSvc.GetChainSpec().SlotsPerEpoch); firstSlot > 64 {
					s.scheduler.Cleanup(phase0.Slot(firstSlot) - 64)
					s.bidTracker.Cleanup(phase0.Slot(firstSlot) - 64)
				}
			}

		case change := <-headChangeCh:
			s.scheduler.OnHeadChange(s.ctx, change)

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
	isOurs := s.bidTracker.IsOurs(event.BuilderIndex)

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
//
// A deposit only says something about the key it funds. Once any key of the
// fleet is active the builder can bid, so a deposit for some other key must not
// pull the reported state back to pending — the scheduler gates bidding on it,
// and a fleet ramping toward its target deposits continuously, which would
// otherwise suppress bidding for the whole ramp.
func (s *Service) SetRegistrationPending() {
	if s.registry != nil && s.registry.AnyActive() {
		return
	}

	s.registrationState.Store(RegistrationStatePending)
	s.log.Info("Builder deposit submitted, waiting for beacon chain inclusion")
}

// SetBuilderRegistered re-evaluates the reported registration state when the
// lifecycle manager detects a registration. The builder index itself lives on
// the key registry; this only drives the status reporting.
func (s *Service) SetBuilderRegistered(index uint64) {
	// Determine the correct state based on finalization
	info := s.chainSvc.GetBuilderByPubkey(s.GetBuilderPubkey())
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

	info := s.chainSvc.GetBuilderByPubkey(s.GetBuilderPubkey())
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

// GetBuilderIndex returns the primary key's on-chain builder index (0 when it is
// not registered). Status reporting only — bids carry the index of the key that
// signed them.
func (s *Service) GetBuilderIndex() uint64 {
	index, _ := s.registry.Primary().BuilderIndex()

	return index
}

// GetBuilderPubkey returns the primary key's public key.
func (s *Service) GetBuilderPubkey() phase0.BLSPubKey {
	return s.registry.Primary().Pubkey()
}

// Registry returns the managed builder key set.
func (s *Service) Registry() *builder_keys.Registry {
	return s.registry
}
