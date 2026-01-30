package epbs

import (
	"context"
	"sync"
	"time"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// SlotState tracks the state for a single slot's bidding/revealing.
type SlotState struct {
	LastBidTime     time.Time
	LastBidHash     phase0.Hash32
	BidCount        int
	BidsClosed      bool        // Block received, no more bids possible
	BidIncluded     bool        // Our bid was picked
	IncludedInBlock phase0.Root // Block that included our bid
	Revealed        bool
}

// Scheduler handles time-based bid and reveal scheduling.
// It uses a simple loop that checks current time and triggers actions.
type Scheduler struct {
	cfg           *builder.EPBSConfig
	chainSpec     *beacon.ChainSpec
	genesis       *beacon.Genesis
	bidCreator    *BidCreator
	revealHandler *RevealHandler
	bidTracker    *BidTracker
	payloadStore  *PayloadStore
	payloadCache  *builder.PayloadCache
	service       *Service // Reference to parent service for firing events
	log           logrus.FieldLogger

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
	log logrus.FieldLogger,
) *Scheduler {
	return &Scheduler{
		cfg:           cfg,
		chainSpec:     chainSpec,
		genesis:       genesis,
		bidCreator:    bidCreator,
		revealHandler: revealHandler,
		bidTracker:    bidTracker,
		payloadStore:  payloadStore,
		payloadCache:  payloadCache,
		service:       service,
		slotStates:    make(map[phase0.Slot]*SlotState),
		log:           log.WithField("component", "scheduler"),
	}
}

// getSlotState returns or creates state for a slot.
func (s *Scheduler) getSlotState(slot phase0.Slot) *SlotState {
	state, ok := s.slotStates[slot]
	if !ok {
		state = &SlotState{}
		s.slotStates[slot] = state
	}

	return state
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

// OnHeadEvent checks if our bid was included and closes bidding for the slot.
func (s *Scheduler) OnHeadEvent(event *beacon.HeadEvent, blockInfo *beacon.BlockInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Close bidding for this slot - block is already produced, no more bids can make it
	slotState := s.getSlotState(event.Slot)
	if !slotState.BidsClosed {
		slotState.BidsClosed = true
		s.log.WithField("slot", event.Slot).Debug("Bidding closed for slot (block received)")
	}

	if blockInfo == nil {
		return
	}

	// Check if this block contains our payload
	for slot, state := range s.slotStates {
		if state.LastBidHash == (phase0.Hash32{}) {
			continue
		}

		if state.LastBidHash == blockInfo.ExecutionBlockHash {
			state.BidIncluded = true
			state.IncludedInBlock = event.Block

			s.log.WithFields(logrus.Fields{
				"slot":       slot,
				"block_root": event.Block[:8],
			}).Info("Our bid was included!")

			break
		}
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

	// Check slots that might need bidding (current and next due to negative start time)
	s.checkSlotForBidding(ctx, currentSlot, now, msIntoSlot)
	s.checkSlotForBidding(ctx, currentSlot+1, now, msIntoSlot-int64(s.chainSpec.SecondsPerSlot.Milliseconds()))

	// Check for reveals
	s.checkSlotForReveal(ctx, currentSlot, now, msIntoSlot)
}

// checkSlotForBidding checks if we should bid for this slot.
func (s *Scheduler) checkSlotForBidding(ctx context.Context, slot phase0.Slot, now time.Time, msRelativeToSlot int64) {
	// Are we in bid period for this slot?
	// msRelativeToSlot is negative if we're before the slot starts
	if msRelativeToSlot < s.cfg.BidStartTime || msRelativeToSlot >= s.cfg.BidEndTime {
		return
	}

	// Get payload from builder cache
	payload := s.payloadCache.Get(slot)
	if payload == nil {
		return
	}

	s.mu.Lock()
	state := s.getSlotState(slot)

	// Check if we should bid
	// - Not if bidding is closed (block already received)
	// - Not if our bid was already included
	// - Not if we bid too recently (respect interval)
	// - Not if payload hasn't changed and we already bid (single bid mode)
	if state.BidsClosed || state.BidIncluded {
		s.mu.Unlock()
		return
	}

	// Check bid interval
	if s.cfg.BidInterval > 0 {
		if time.Since(state.LastBidTime) < time.Duration(s.cfg.BidInterval)*time.Millisecond {
			s.mu.Unlock()
			return
		}
	} else {
		// Single bid mode - only bid if payload changed or never bid
		if state.BidCount > 0 && state.LastBidHash == payload.BlockHash {
			s.mu.Unlock()
			return
		}
	}

	// Calculate bid value
	bidValue := s.cfg.BidMinAmount
	if s.cfg.BidInterval > 0 && state.BidCount > 0 {
		bidValue = s.cfg.BidMinAmount + uint64(state.BidCount)*s.cfg.BidIncrease
	}

	if payload.BlockValue > bidValue {
		bidValue = payload.BlockValue
	}

	s.mu.Unlock()

	// Submit bid
	err := s.bidCreator.CreateAndSubmitBid(ctx, payload, bidValue)

	// Update state regardless of success - we don't want to spam on failure
	s.mu.Lock()
	state.LastBidTime = now
	state.LastBidHash = payload.BlockHash
	state.BidCount++
	bidCount := state.BidCount
	s.mu.Unlock()

	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Error("Failed to submit bid")
		// Fire bid failure event
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

	// Track the bid
	s.bidTracker.TrackBid(&ExecutionPayloadBid{
		Slot:         slot,
		BuilderIndex: s.bidCreator.builderIndex,
		Value:        bidValue,
		BlockHash:    payload.BlockHash,
	}, true)

	// Fire bid success event
	if s.service != nil {
		s.service.FireBidSubmission(&BidSubmissionEvent{
			Slot:      slot,
			BlockHash: payload.BlockHash,
			Value:     bidValue,
			BidCount:  bidCount,
			Success:   true,
		})
	}

	s.log.WithFields(logrus.Fields{
		"slot":       slot,
		"bid_value":  bidValue,
		"bid_count":  bidCount,
		"block_hash": payload.BlockHash[:8],
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

	if !state.BidIncluded || state.Revealed {
		s.mu.Unlock()
		return
	}

	blockRoot := state.IncludedInBlock
	s.mu.Unlock()

	// Get payload for reveal
	payload := s.payloadStore.Get(slot)
	if payload == nil {
		s.log.WithField("slot", slot).Error("No payload found for reveal")
		return
	}

	err := s.revealHandler.SubmitReveal(ctx, payload, blockRoot)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Error("Failed to submit reveal")
		return
	}

	s.mu.Lock()
	state.Revealed = true
	s.mu.Unlock()

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
func (s *Scheduler) MarkBidIncluded(slot phase0.Slot, blockRoot phase0.Root) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.getSlotState(slot)
	state.BidIncluded = true
	state.IncludedInBlock = blockRoot
}

// GetBidTracker returns the bid tracker.
func (s *Scheduler) GetBidTracker() *BidTracker {
	return s.bidTracker
}
