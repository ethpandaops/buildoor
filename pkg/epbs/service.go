package epbs

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// Registration state constants for the ePBS service.
const (
	RegistrationStateUnknown             int32 = 0 // Not checked yet
	RegistrationStatePending             int32 = 1 // Deposit submitted, waiting for inclusion in beacon state
	RegistrationStateRegistered          int32 = 2 // Builder registered and deposit epoch finalized
	RegistrationStateWaitingGloas        int32 = 3 // Waiting for Gloas fork activation
	RegistrationStatePendingFinalization int32 = 4 // Builder in beacon state but deposit epoch not finalized
	RegistrationStateExiting             int32 = 5 // Exit submitted, withdrawable epoch set but not reached
	RegistrationStateExited              int32 = 6 // Withdrawable epoch passed, builder has exited
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
	default:
		return "unknown"
	}
}

// BidSubmissionEvent represents a bid submission attempt (success or failure).
type BidSubmissionEvent struct {
	Slot      phase0.Slot
	BlockHash [32]byte
	Value     uint64
	BidCount  int
	Success   bool
	Error     string
}

// Service is the main ePBS orchestrator that handles time-scheduled bidding and revealing.
// It subscribes to builder payload events and handles the ePBS protocol.
type Service struct {
	cfg                   *builder.EPBSConfig
	signer                *Signer
	scheduler             *Scheduler
	bidCreator            *BidCreator
	revealHandler         *RevealHandler
	bidTracker            *BidTracker
	payloadStore          *PayloadStore
	clClient              *beacon.Client
	chainSvc              chain.Service
	builderIndex          uint64
	builderPubkey         phase0.BLSPubKey
	payloadSubscription   *utils.Subscription[*builder.PayloadReadyEvent]
	bidSubmissionDispatch *utils.Dispatcher[*BidSubmissionEvent]
	builderSvc            *builder.Service
	enabled               atomic.Bool
	registrationState     atomic.Int32
	ctx                   context.Context
	cancel                context.CancelFunc
	log                   logrus.FieldLogger
	wg                    sync.WaitGroup
}

// NewService creates a new ePBS service.
func NewService(
	cfg *builder.EPBSConfig,
	clClient *beacon.Client,
	chainSvc chain.Service,
	blsSigner *signer.BLSSigner,
	log logrus.FieldLogger,
) (*Service, error) {
	serviceLog := log.WithField("component", "epbs-service")

	// Create ePBS signer wrapper
	epbsSigner := NewSigner(blsSigner)

	s := &Service{
		cfg:                   cfg,
		signer:                epbsSigner,
		clClient:              clClient,
		chainSvc:              chainSvc,
		builderPubkey:         blsSigner.PublicKey(),
		payloadStore:          NewPayloadStore(),
		bidSubmissionDispatch: &utils.Dispatcher[*BidSubmissionEvent]{},
		log:                   serviceLog,
	}

	// BidTracker, Scheduler, BidCreator, and RevealHandler are created in Start
	// after we have the chain spec and genesis info

	return s, nil
}

// SetEnabled sets the enabled state of the ePBS service.
func (s *Service) SetEnabled(enabled bool) {
	s.enabled.Store(enabled)
}

// IsEnabled returns whether the ePBS service is enabled.
func (s *Service) IsEnabled() bool {
	return s.enabled.Load()
}

// SubscribeBidSubmissions subscribes to bid submission events.
func (s *Service) SubscribeBidSubmissions(capacity int) *utils.Subscription[*BidSubmissionEvent] {
	return s.bidSubmissionDispatch.Subscribe(capacity, false)
}

// FireBidSubmission fires a bid submission event.
func (s *Service) FireBidSubmission(event *BidSubmissionEvent) {
	s.bidSubmissionDispatch.Fire(event)
}

// Start starts the ePBS service.
// It subscribes to the builder service's payload ready events.
func (s *Service) Start(ctx context.Context, builderSvc *builder.Service) error {
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.builderSvc = builderSvc

	// Get chain spec and genesis from builder service
	chainSpec := builderSvc.GetChainSpec()
	genesis := builderSvc.GetGenesis()

	if chainSpec == nil || genesis == nil {
		return fmt.Errorf("builder service not initialized (missing chainspec or genesis)")
	}

	// Load builder index from chain service and determine initial registration state
	if !s.chainSvc.HasBuildersLoaded() {
		s.log.Info("No builders in beacon state (pre-Gloas), waiting for registration")
		s.builderIndex = 0
		s.registrationState.Store(RegistrationStateWaitingGloas)
	} else if builderInfo := s.chainSvc.GetBuilderByPubkey(s.builderPubkey); builderInfo == nil {
		s.log.Info("Builder not found in beacon state, waiting for registration")
		s.builderIndex = 0
		s.registrationState.Store(RegistrationStatePending)
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
	s.bidTracker = NewBidTracker(s.builderIndex, chainSpec, s.log)
	s.bidCreator = NewBidCreator(
		s.signer,
		s.clClient,
		genesis,
		chainSpec,
		s.builderIndex,
		s.log,
	)
	s.revealHandler = NewRevealHandler(
		s.signer,
		s.clClient,
		genesis,
		chainSpec,
		s.builderIndex,
		s.log,
	)
	// isBuilderActive checks that the builder's deposit is finalized and it hasn't exited.
	isBuilderActive := func() bool {
		info := s.chainSvc.GetBuilderByPubkey(s.builderPubkey)
		if info == nil {
			return false
		}
		finalizedEpoch := s.chainSvc.GetFinalizedEpoch()
		return info.DepositEpoch < uint64(finalizedEpoch) && info.WithdrawableEpoch == chain.FarFutureEpoch
	}

	s.scheduler = NewScheduler(
		s.cfg,
		chainSpec,
		genesis,
		s.bidCreator,
		s.revealHandler,
		s.bidTracker,
		s.payloadStore,
		builderSvc.GetPayloadCache(),
		s,
		isBuilderActive,
		s.log,
	)

	// Subscribe to builder's payload ready events
	s.payloadSubscription = builderSvc.SubscribePayloadReady(16)

	// Start the main event loop
	s.wg.Add(1)

	go s.run()

	s.log.Info("ePBS service started")

	return nil
}

// Stop stops the ePBS service.
func (s *Service) Stop() {
	s.log.Info("Stopping ePBS service")

	if s.cancel != nil {
		s.cancel()
	}

	if s.payloadSubscription != nil {
		s.payloadSubscription.Unsubscribe()
	}

	s.wg.Wait()

	s.log.Info("ePBS service stopped")
}

// run is the main event loop.
func (s *Service) run() {
	defer s.wg.Done()

	payloadChan := s.payloadSubscription.Channel()
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

		case event := <-payloadChan:
			s.handlePayloadReady(event)

		case event := <-headSub.Channel():
			s.handleHeadEvent(event)

		case event := <-bidSub.Channel():
			s.handleBidEvent(event)

		case epochStats, ok := <-epochSub.Channel():
			if ok {
				s.RefreshRegistrationState()

				// Prune expired pending payments now that a new epoch has started
				if s.bidTracker != nil {
					s.bidTracker.PruneExpiredPayments(epochStats.Epoch)
				}
			}

		case <-ticker.C:
			if s.enabled.Load() && s.IsRegistered() {
				s.scheduler.ProcessTick(s.ctx)
			}
		}
	}
}

// handlePayloadReady processes a payload ready event from the builder.
func (s *Service) handlePayloadReady(event *builder.PayloadReadyEvent) {
	s.log.WithFields(logrus.Fields{
		"slot":        event.Slot,
		"block_hash":  fmt.Sprintf("%x", event.BlockHash[:8]),
		"block_value": event.BlockValue,
	}).Debug("Received payload from builder")

	s.scheduler.OnPayloadReady(event)
}

// handleHeadEvent processes a head event to check if our bid was included.
func (s *Service) handleHeadEvent(event *beacon.HeadEvent) {
	s.log.WithFields(logrus.Fields{
		"slot": event.Slot,
		"root": fmt.Sprintf("%x", event.Block[:8]),
	}).Info("Head event received")

	// Close bidding for this slot - block already produced
	s.scheduler.OnHeadEvent(event, nil)

	// Check if this block contains our payload (async to not block)
	go s.checkForOurPayload(event)
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

// checkForOurPayload checks if the beacon block contains our execution payload.
func (s *Service) checkForOurPayload(event *beacon.HeadEvent) {
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()

	// Fetch the beacon block by its root
	blockInfo, err := s.clClient.GetBlockInfo(ctx, fmt.Sprintf("0x%x", event.Block[:]))
	if err != nil {
		s.log.WithError(err).WithField("slot", event.Slot).Debug("Failed to get block info")
		return
	}

	// Check if this execution block hash matches any of our stored payloads
	payload := s.payloadStore.GetByBlockHash(blockInfo.ExecutionBlockHash)
	if payload == nil {
		// Not our payload
		return
	}

	s.log.WithFields(logrus.Fields{
		"slot":       event.Slot,
		"block_hash": fmt.Sprintf("%x", blockInfo.ExecutionBlockHash[:8]),
		"bid_value":  payload.BidValue,
	}).Info("Our payload was included in a beacon block!")

	// Mark bid as included in scheduler
	s.scheduler.MarkBidIncluded(payload.Slot, event.Block)

	// Record pending payment obligation
	if s.bidTracker != nil && payload.BidValue > 0 {
		s.bidTracker.RecordWonBid(payload.Slot, payload.BidValue)
	}
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

// SetBuilderRegistered updates the builder index when the lifecycle manager detects registration.
// It sets the appropriate state based on finalization status.
// Called by the lifecycle manager's registration callback.
func (s *Service) SetBuilderRegistered(index uint64) {
	s.builderIndex = index

	if s.bidCreator != nil {
		s.bidCreator.SetBuilderIndex(index)
	}

	if s.revealHandler != nil {
		s.revealHandler.SetBuilderIndex(index)
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
		// Builder not in state (yet or removed)
		if currentState != RegistrationStatePending {
			s.registrationState.Store(RegistrationStatePending)
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

// GetPayloadStore returns the payload store.
func (s *Service) GetPayloadStore() *PayloadStore {
	return s.payloadStore
}

// GetBuilderIndex returns the builder index.
func (s *Service) GetBuilderIndex() uint64 {
	return s.builderIndex
}

// GetBuilderPubkey returns the builder public key.
func (s *Service) GetBuilderPubkey() phase0.BLSPubKey {
	return s.builderPubkey
}

// UpdateConfig updates the service configuration at runtime.
func (s *Service) UpdateConfig(cfg *builder.EPBSConfig) {
	s.cfg = cfg
	if s.scheduler != nil {
		s.scheduler.UpdateConfig(cfg)
	}

	s.log.Info("ePBS configuration updated")
}
