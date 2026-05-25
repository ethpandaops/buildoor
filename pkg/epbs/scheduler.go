package epbs

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// perHashBidState tracks bidding state for a single (slot, blockHash) pair.
type perHashBidState struct {
	LastBidTime time.Time
	BidCount    int
}

// SlotState tracks the state for a single slot's bidding/revealing.
type SlotState struct {
	// BidsByHash holds per-payload bid state. Each entry corresponds to one
	// built payload (primary or fallback) identified by its EL block hash.
	BidsByHash      map[phase0.Hash32]*perHashBidState
	BidsClosed      bool              // Block received, no more bids possible
	BidIncluded     bool              // Our bid was picked
	IncludedInBlock *beacon.BlockInfo // Block that included our bid
	Revealed        bool
}

// Scheduler handles time-based bid and reveal scheduling.
// It uses a simple loop that checks current time and triggers actions.
type Scheduler struct {
	cfg                    *builder.EPBSConfig
	chainSpec              *beacon.ChainSpec
	genesis                *beacon.Genesis
	bidCreator             *BidCreator
	revealHandler          *RevealHandler
	bidTracker             *BidTracker
	payloadStore           *PayloadStore
	payloadCache           *builder.PayloadCache
	service                *Service // Reference to parent service for firing events
	isBuilderActive        func() bool
	hasProposerPreferences func(phase0.Slot) bool
	log                    logrus.FieldLogger

	// Simple state tracking per slot
	slotStates map[phase0.Slot]*SlotState
	mu         sync.Mutex
}

// NewScheduler creates a new scheduler.
func NewScheduler(
	cfg *builder.EPBSConfig,
	chainSpec *beacon.ChainSpec,
	genesis *beacon.Genesis,
	bidCreator *BidCreator,
	revealHandler *RevealHandler,
	bidTracker *BidTracker,
	payloadStore *PayloadStore,
	payloadCache *builder.PayloadCache,
	service *Service,
	isBuilderActive func() bool,
	hasProposerPreferences func(phase0.Slot) bool,
	log logrus.FieldLogger,
) *Scheduler {
	return &Scheduler{
		cfg:                    cfg,
		chainSpec:              chainSpec,
		genesis:                genesis,
		bidCreator:             bidCreator,
		revealHandler:          revealHandler,
		bidTracker:             bidTracker,
		payloadStore:           payloadStore,
		payloadCache:           payloadCache,
		service:                service,
		isBuilderActive:        isBuilderActive,
		hasProposerPreferences: hasProposerPreferences,
		slotStates:             make(map[phase0.Slot]*SlotState),
		log:                    log.WithField("component", "scheduler"),
	}
}

// getSlotState returns or creates state for a slot. Must be called with mu held.
func (s *Scheduler) getSlotState(slot phase0.Slot) *SlotState {
	state, ok := s.slotStates[slot]
	if !ok {
		state = &SlotState{
			BidsByHash: make(map[phase0.Hash32]*perHashBidState),
		}
		s.slotStates[slot] = state
	}

	return state
}

// getSlotStateSafe returns a copy of the slot state, or nil if not found. Thread-safe.
func (s *Scheduler) getSlotStateSafe(slot phase0.Slot) *SlotState {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.slotStates[slot]
	if !ok {
		return nil
	}

	stateCopy := *state

	return &stateCopy
}

// OnPayloadReady stores the payload for reveals.
func (s *Scheduler) OnPayloadReady(event *builder.PayloadReadyEvent) {
	s.payloadStore.Store(&BuiltPayload{
		Slot:              event.Slot,
		BlockHash:         event.BlockHash,
		ParentBlockHash:   event.ParentBlockHash,
		ParentBlockRoot:   event.ParentBlockRoot,
		ExecutionPayload:  event.Payload,
		BlobsBundle:       event.BlobsBundle,
		ExecutionRequests: event.ExecutionRequests,
		BidValue:          event.BlockValue,
		FeeRecipient:      event.FeeRecipient,
		Timestamp:         event.Timestamp,
		PrevRandao:        event.PrevRandao,
		GasLimit:          event.GasLimit,
	})
}

// OnHeadEvent closes bidding for the slot — once a block is produced, no more bids can make it.
// Bid-inclusion marking happens via MarkBidIncluded from the async processHeadBlock path.
func (s *Scheduler) OnHeadEvent(event *beacon.HeadEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	slotState := s.getSlotState(event.Slot)
	if !slotState.BidsClosed {
		slotState.BidsClosed = true
		s.log.WithField("slot", event.Slot).Debug("Bidding closed for slot (block received)")
	}
}

// ProcessTick is called frequently to check if any bids or reveals are due.
func (s *Scheduler) ProcessTick(ctx context.Context) {
	now := time.Now()

	// Calculate current slot and position within slot
	if now.Before(s.genesis.GenesisTime) {
		return
	}

	elapsed := now.Sub(s.genesis.GenesisTime)
	currentSlot := phase0.Slot(elapsed / s.chainSpec.SecondsPerSlot)
	msIntoSlot := (elapsed % s.chainSpec.SecondsPerSlot).Milliseconds()

	// ePBS bids are only valid from the Gloas fork onwards.
	if s.chainSpec.GloasForkEpoch != nil {
		currentEpoch := uint64(currentSlot) / s.chainSpec.SlotsPerEpoch
		if currentEpoch < *s.chainSpec.GloasForkEpoch {
			return
		}
	}

	// Don't bid if the builder is not active on-chain.
	if s.isBuilderActive != nil && !s.isBuilderActive() {
		// Still check reveals — we may have bids from before deactivation.
		s.checkSlotForReveal(ctx, currentSlot, now, msIntoSlot)
		return
	}

	// Check slots that might need bidding (current slot + next slot for negative bid start times)
	s.checkSlotForBidding(ctx, currentSlot, now, msIntoSlot)
	s.checkSlotForBidding(ctx, currentSlot+1, now, msIntoSlot-int64(s.chainSpec.SecondsPerSlot.Milliseconds()))

	// Check for reveals
	s.checkSlotForReveal(ctx, currentSlot, now, msIntoSlot)
}

// checkSlotForBidding checks if we should bid for this slot.
// It iterates all payloads built for the slot (primary + any fallback) and submits
// a bid for each one that has not yet been bid on (or whose interval has elapsed).
func (s *Scheduler) checkSlotForBidding(ctx context.Context, slot phase0.Slot, now time.Time, msRelativeToSlot int64) {
	// Are we in bid period for this slot?
	// msRelativeToSlot is negative if we're before the slot starts
	if msRelativeToSlot < s.cfg.BidStartTime || msRelativeToSlot >= s.cfg.BidEndTime {
		return
	}

	// Get all payloads for this slot (primary + fallback builds)
	payloads := s.payloadCache.GetAllForSlot(slot)
	if len(payloads) == 0 {
		return
	}

	s.mu.Lock()
	state := s.getSlotState(slot)

	if state.BidsClosed || state.BidIncluded {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	hasPrefs := s.hasProposerPreferences != nil && s.hasProposerPreferences(slot)
	if !hasPrefs {
		s.log.WithField("slot", slot).Warn("No proposer preferences for slot — bids will likely be rejected by beacon node")
	}

	for _, payload := range payloads {
		s.tryBidForPayload(ctx, slot, payload, now, msRelativeToSlot, hasPrefs)
	}
}

// tryBidForPayload attempts to submit a bid for a single payload, respecting the
// per-hash bid interval and single-bid constraints.
func (s *Scheduler) tryBidForPayload(
	ctx context.Context,
	slot phase0.Slot,
	payload *builder.PayloadReadyEvent,
	now time.Time,
	msRelativeToSlot int64,
	hasPrefs bool,
) {
	s.mu.Lock()
	state := s.getSlotState(slot)

	hashState, seen := state.BidsByHash[payload.BlockHash]
	if !seen {
		hashState = &perHashBidState{}
		state.BidsByHash[payload.BlockHash] = hashState
	}

	// Check bid interval / single-bid guard for this specific block hash.
	if s.cfg.BidInterval > 0 {
		if time.Since(hashState.LastBidTime) < time.Duration(s.cfg.BidInterval)*time.Millisecond {
			s.mu.Unlock()
			return
		}
	} else if hashState.BidCount > 0 {
		// Single-bid mode: only bid once per block hash.
		s.mu.Unlock()
		return
	}

	// Calculate bid value using the total bid count across all hashes for the escalation logic.
	totalBidCount := 0
	for _, hs := range state.BidsByHash {
		totalBidCount += hs.BidCount
	}

	bidBase := new(big.Int).Div(payload.BlockValue, big.NewInt(1_000_000_000)).Uint64()
	if s.cfg.BidMinAmount > bidBase {
		bidBase = s.cfg.BidMinAmount
	}

	bidValue := bidBase
	if s.cfg.BidInterval > 0 && totalBidCount > 0 {
		bidValue = bidBase + uint64(totalBidCount)*s.cfg.BidIncrease
	}

	bidValue += s.cfg.P2PBidSubsidy
	s.mu.Unlock()

	s.log.WithFields(logrus.Fields{
		"slot":                     slot,
		"bid_value":                bidValue,
		"bid_count":                hashState.BidCount,
		"block_hash":               fmt.Sprintf("%x", payload.BlockHash[:8]),
		"parent_hash":              fmt.Sprintf("%x", payload.ParentBlockHash[:8]),
		"ms_into_slot":             msRelativeToSlot,
		"has_proposer_preferences": hasPrefs,
	}).Info("Creating and submitting bid")

	err := s.bidCreator.CreateAndSubmitBid(ctx, payload, bidValue)

	s.mu.Lock()
	hashState.LastBidTime = now
	hashState.BidCount++
	bidCount := hashState.BidCount
	s.mu.Unlock()

	if err != nil {
		s.log.WithError(err).WithFields(logrus.Fields{
			"slot":       slot,
			"block_hash": fmt.Sprintf("%x", payload.BlockHash[:8]),
		}).Error("Failed to submit bid")

		if s.service != nil {
			s.service.FireBidSubmission(&BidSubmissionEvent{
				Slot:      slot,
				BlockHash: payload.BlockHash,
				Value:     bidValue,
				BidCount:  bidCount,
				Success:   false,
				Error:     err.Error(),
			})
		}

		return
	}

	s.bidTracker.TrackBid(&ExecutionPayloadBid{
		Slot:         slot,
		BuilderIndex: s.bidCreator.builderIndex,
		Value:        bidValue,
		BlockHash:    payload.BlockHash,
	}, true)

	if s.service != nil {
		event := &BidSubmissionEvent{
			Slot:      slot,
			BlockHash: payload.BlockHash,
			Value:     bidValue,
			BidCount:  bidCount,
			Success:   true,
		}
		if !hasPrefs {
			event.Warning = "no proposer preferences — bid likely rejected"
		}
		s.service.FireBidSubmission(event)
	}

	if s.service != nil && s.service.builderSvc != nil {
		s.service.builderSvc.IncrementBidsSubmitted()
	}

	s.log.WithFields(logrus.Fields{
		"slot":        slot,
		"bid_value":   bidValue,
		"bid_count":   bidCount,
		"block_hash":  fmt.Sprintf("%x", payload.BlockHash[:8]),
		"parent_hash": fmt.Sprintf("%x", payload.ParentBlockHash[:8]),
	}).Info("Bid submitted")
}

// checkSlotForReveal checks if we should reveal for this slot.
func (s *Scheduler) checkSlotForReveal(ctx context.Context, slot phase0.Slot, now time.Time, msIntoSlot int64) {
	// Are we past reveal time?
	if msIntoSlot < s.cfg.RevealTime {
		return
	}

	s.mu.Lock()
	state := s.getSlotState(slot)

	if !state.BidIncluded || state.Revealed || state.IncludedInBlock == nil {
		s.mu.Unlock()
		return
	}

	blockInfo := state.IncludedInBlock
	s.mu.Unlock()

	s.log.WithFields(logrus.Fields{
		"slot":         slot,
		"ms_into_slot": msIntoSlot,
	}).Info("Revealing payload")

	// Identify the payload to reveal by looking up the accepted bid's block hash.
	// blockInfo.ExecutionBlockHash = bid.message.block_hash for a Gloas block, which
	// matches exactly the BlockHash field of the BuiltPayload we committed to.
	payload := s.payloadStore.GetByBlockHash(blockInfo.ExecutionBlockHash)
	if payload == nil {
		s.log.WithFields(logrus.Fields{
			"slot":       slot,
			"block_hash": fmt.Sprintf("%x", blockInfo.ExecutionBlockHash[:8]),
		}).Error("No payload found for reveal (block hash not in store)")
		return
	}

	s.log.WithFields(logrus.Fields{
		"slot":         slot,
		"block_root":   fmt.Sprintf("%x", blockInfo.Root[:8]),
		"parent_root":  fmt.Sprintf("%x", blockInfo.ParentRoot[:8]),
		"block_hash":   fmt.Sprintf("%x", payload.BlockHash[:8]),
		"ms_into_slot": msIntoSlot,
	}).Info("Submitting reveal")

	err := s.revealHandler.SubmitReveal(ctx, payload, blockInfo)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Error("Failed to submit reveal")

		if s.service != nil {
			s.service.FireReveal(&RevealEvent{Slot: slot, Success: false})
		}

		return
	}

	s.mu.Lock()
	state.Revealed = true
	s.mu.Unlock()

	// Mark payment as revealed (immediate deduction from live balance)
	s.bidTracker.MarkRevealed(slot)

	// Fire reveal event for UI and increment stats
	if s.service != nil {
		s.service.FireReveal(&RevealEvent{
			Slot:    slot,
			Success: true,
		})

		if s.service.builderSvc != nil {
			s.service.builderSvc.IncrementRevealsSuccess()
		}
	}

	s.log.WithField("slot", slot).Info("Reveal submitted")
}

// Cleanup removes old state.
func (s *Scheduler) Cleanup(olderThan phase0.Slot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for slot := range s.slotStates {
		if slot < olderThan {
			delete(s.slotStates, slot)
		}
	}
}

// UpdateConfig updates the scheduler configuration.
func (s *Scheduler) UpdateConfig(cfg *builder.EPBSConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cfg = cfg
}

// MarkBidIncluded marks a bid as included for a slot.
func (s *Scheduler) MarkBidIncluded(slot phase0.Slot, blockInfo *beacon.BlockInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.getSlotState(slot)
	state.BidIncluded = true
	state.IncludedInBlock = blockInfo
}

// GetBidTracker returns the bid tracker.
func (s *Scheduler) GetBidTracker() *BidTracker {
	return s.bidTracker
}
