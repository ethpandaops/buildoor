package epbs

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// variantBidState tracks bid pacing state for one (slot, variant) pair so
// FULL and EMPTY don't share a single LastBidTime/LastBidHash counter.
type variantBidState struct {
	LastBidTime time.Time
	LastBidHash phase0.Hash32
	BidCount    int
}

// SlotState tracks the state for a single slot's bidding/revealing.
type SlotState struct {
	// Aggregated fields kept for backwards-compat with WebUI/event consumers.
	LastBidTime time.Time     // most-recent bid time across variants
	LastBidHash phase0.Hash32 // most-recent bid hash across variants
	BidCount    int           // total bids submitted across variants

	// Per-variant pacing state so FULL and EMPTY can be bid independently.
	BidStates map[builder.PayloadVariant]*variantBidState

	BidsClosed      bool                   // Block received, no more bids possible
	BidIncluded     bool                   // Our bid was picked
	IncludedInBlock phase0.Root            // Block that included our bid
	BidVariant      builder.PayloadVariant // Variant that was included (only valid when BidIncluded)
	Revealed        bool
}

func (state *SlotState) variantState(variant builder.PayloadVariant) *variantBidState {
	if state.BidStates == nil {
		state.BidStates = make(map[builder.PayloadVariant]*variantBidState, 2)
	}
	vs, ok := state.BidStates[variant]
	if !ok {
		vs = &variantBidState{}
		state.BidStates[variant] = vs
	}
	return vs
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
		state = &SlotState{}
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
		Variant:           event.Variant,
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

	// Check whether the new block's execution payload matches any of our bids
	// across any (slot, variant). The first match wins — we record which variant
	// got included so the reveal step can fetch the correct payload.
	for slot, state := range s.slotStates {
		if state.BidIncluded {
			continue
		}

		for variant, vs := range state.BidStates {
			if vs.LastBidHash == (phase0.Hash32{}) {
				continue
			}
			if vs.LastBidHash != blockInfo.ExecutionBlockHash {
				continue
			}

			state.BidIncluded = true
			state.BidVariant = variant
			state.IncludedInBlock = event.Block

			s.log.WithFields(logrus.Fields{
				"slot":       slot,
				"variant":    variant.String(),
				"block_root": fmt.Sprintf("%x", event.Block[:8]),
			}).Info("Our bid was included!")

			break
		}

		if state.BidIncluded {
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

// checkSlotForBidding checks if we should bid for this slot. When both FULL and
// EMPTY payloads exist, a bid is submitted for each — the proposer picks whichever
// matches reality. Per-variant bid pacing is tracked on SlotState.BidStates.
func (s *Scheduler) checkSlotForBidding(ctx context.Context, slot phase0.Slot, now time.Time, msRelativeToSlot int64) {
	if msRelativeToSlot < s.cfg.BidStartTime || msRelativeToSlot >= s.cfg.BidEndTime {
		return
	}

	payloads := s.payloadCache.GetAllForSlot(slot)
	if len(payloads) == 0 {
		return
	}

	for _, payload := range payloads {
		s.maybeBidForVariant(ctx, slot, payload, now, msRelativeToSlot)
	}
}

// maybeBidForVariant evaluates and possibly submits a bid for one specific
// (slot, variant) payload. Per-variant pacing is tracked on SlotState.BidStates.
func (s *Scheduler) maybeBidForVariant(
	ctx context.Context,
	slot phase0.Slot,
	payload *builder.PayloadReadyEvent,
	now time.Time,
	msRelativeToSlot int64,
) {
	s.mu.Lock()
	state := s.getSlotState(slot)

	if state.BidsClosed || state.BidIncluded {
		s.mu.Unlock()
		return
	}

	vs := state.variantState(payload.Variant)

	if s.cfg.BidInterval > 0 {
		if time.Since(vs.LastBidTime) < time.Duration(s.cfg.BidInterval)*time.Millisecond {
			s.mu.Unlock()
			return
		}
	} else {
		// Single bid mode - only bid if payload changed or never bid
		if vs.BidCount > 0 && vs.LastBidHash == payload.BlockHash {
			s.mu.Unlock()
			return
		}
	}

	bidBase := payload.BlockValue
	if s.cfg.BidMinAmount > bidBase {
		bidBase = s.cfg.BidMinAmount
	}

	bidValue := bidBase
	if s.cfg.BidInterval > 0 && vs.BidCount > 0 {
		bidValue = bidBase + uint64(vs.BidCount)*s.cfg.BidIncrease
	}

	bidValue += s.cfg.P2PBidSubsidy

	s.mu.Unlock()

	hasPrefs := s.hasProposerPreferences != nil && s.hasProposerPreferences(slot)

	s.log.WithFields(logrus.Fields{
		"slot":                     slot,
		"variant":                  payload.Variant.String(),
		"bid_value":                bidValue,
		"bid_count":                vs.BidCount,
		"block_hash":               fmt.Sprintf("%x", payload.BlockHash[:8]),
		"ms_into_slot":             msRelativeToSlot,
		"has_proposer_preferences": hasPrefs,
	}).Info("Creating and submitting bid")

	if !hasPrefs {
		s.log.WithFields(logrus.Fields{
			"slot":    slot,
			"variant": payload.Variant.String(),
		}).Warn("No proposer preferences for slot — bid will likely be rejected by beacon node")
	}

	err := s.bidCreator.CreateAndSubmitBid(ctx, payload, bidValue)

	// Update per-variant state regardless of success.
	s.mu.Lock()
	vs.LastBidTime = now
	vs.LastBidHash = payload.BlockHash
	vs.BidCount++
	state.LastBidTime = now
	state.LastBidHash = payload.BlockHash
	state.BidCount++
	bidCount := vs.BidCount
	s.mu.Unlock()

	if err != nil {
		s.log.WithError(err).WithFields(logrus.Fields{
			"slot":    slot,
			"variant": payload.Variant.String(),
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
		"slot":       slot,
		"variant":    payload.Variant.String(),
		"bid_value":  bidValue,
		"bid_count":  bidCount,
		"block_hash": fmt.Sprintf("%x", payload.BlockHash[:8]),
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
	bidVariant := state.BidVariant
	s.mu.Unlock()

	s.log.WithFields(logrus.Fields{
		"slot":         slot,
		"variant":      bidVariant.String(),
		"ms_into_slot": msIntoSlot,
	}).Info("Revealing payload")

	// Get payload for reveal — must match the variant that was included.
	payload := s.payloadStore.GetByVariant(slot, bidVariant)
	if payload == nil {
		s.log.WithFields(logrus.Fields{
			"slot":    slot,
			"variant": bidVariant.String(),
		}).Error("No payload found for reveal")
		return
	}

	s.log.WithFields(logrus.Fields{
		"slot":         slot,
		"block_root":   fmt.Sprintf("%x", blockRoot[:8]),
		"block_hash":   fmt.Sprintf("%x", payload.BlockHash[:8]),
		"ms_into_slot": msIntoSlot,
	}).Info("Submitting reveal")

	err := s.revealHandler.SubmitReveal(ctx, payload, blockRoot)
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

// MarkBidIncluded marks a bid as included for a slot. The variant indicates
// which variant of our payload was selected — required so the reveal step
// pulls the correct payload from the store.
func (s *Scheduler) MarkBidIncluded(slot phase0.Slot, blockRoot phase0.Root, variant builder.PayloadVariant) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.getSlotState(slot)
	state.BidIncluded = true
	state.BidVariant = variant
	state.IncludedInBlock = blockRoot
}

// GetBidTracker returns the bid tracker.
func (s *Scheduler) GetBidTracker() *BidTracker {
	return s.bidTracker
}
