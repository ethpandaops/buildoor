package epbs

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/proposerpreferences"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

const (
	// maxRevealAttempts bounds how many times we retry a failed payload reveal
	// before giving up, so a persistent failure doesn't spin the tick loop and
	// spam the beacon node.
	maxRevealAttempts = 3
	// revealRetryDelay is the wait between successive reveal attempts.
	revealRetryDelay = 500 * time.Millisecond
)

// SlotState tracks the state for a single slot's bidding/revealing.
type SlotState struct {
	LastBidTime       time.Time
	LastBidHash       phase0.Hash32
	BidCount          int
	BidsClosed        bool              // Block received, no more bids possible
	BidIncluded       bool              // Our bid was picked
	IncludedInBlock   *beacon.BlockInfo // Block that included our bid
	Revealed          bool
	RevealAttempts    int       // Number of reveal attempts made (success or failure)
	LastRevealAttempt time.Time // Time of the most recent reveal attempt
}

// Scheduler handles time-based bid and reveal scheduling.
// It uses a simple loop that checks current time and triggers actions.
type Scheduler struct {
	cfg           *config.EPBSConfig
	chainSvc      chain.Service
	bidCreator    *BidCreator
	revealHandler *RevealHandler
	bidTracker    *BidTracker
	payloadStore  *PayloadStore
	payloadCache  *payload_builder.PayloadCache
	service       *Service // Reference to parent service for firing events
	blsSigner     *signer.BLSSigner
	propPrefCache *proposerpreferences.Cache
	log           logrus.FieldLogger

	// Simple state tracking per slot
	slotStates map[phase0.Slot]*SlotState
	mu         sync.Mutex
}

// NewScheduler creates a new scheduler.
func NewScheduler(
	cfg *config.EPBSConfig,
	chainSvc chain.Service,
	bidCreator *BidCreator,
	revealHandler *RevealHandler,
	bidTracker *BidTracker,
	payloadStore *PayloadStore,
	payloadCache *payload_builder.PayloadCache,
	service *Service,
	blsSigner *signer.BLSSigner,
	propPrefCache *proposerpreferences.Cache,
	log logrus.FieldLogger,
) *Scheduler {
	return &Scheduler{
		cfg:           cfg,
		chainSvc:      chainSvc,
		bidCreator:    bidCreator,
		revealHandler: revealHandler,
		bidTracker:    bidTracker,
		payloadStore:  payloadStore,
		payloadCache:  payloadCache,
		service:       service,
		blsSigner:     blsSigner,
		propPrefCache: propPrefCache,
		slotStates:    make(map[phase0.Slot]*SlotState),
		log:           log.WithField("component", "scheduler"),
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

// OnPayloadReady stores the payload (by reference) for later reveal.
func (s *Scheduler) OnPayloadReady(payload *payload_builder.Payload) {
	s.payloadStore.Store(payload)
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
	genesisTime := s.chainSvc.GetGenesis().GenesisTime
	if now.Before(genesisTime) {
		return
	}

	elapsed := now.Sub(genesisTime)
	currentSlot := s.chainSvc.TimeToSlot(now)
	// msIntoSlot is the offset within the current slot (not the time since
	// genesis) — the bid and reveal windows are slot-relative.
	msIntoSlot := (elapsed % s.chainSvc.GetChainSpec().SecondsPerSlot).Milliseconds()

	// ePBS bids are only valid from the Gloas fork onwards.
	if s.chainSvc.GetCurrentFork() < version.DataVersionGloas {
		return
	}

	// Don't bid if the builder is not active on-chain.
	if !chain.IsBuilderActive(s.chainSvc.GetBuilderByPubkey(s.blsSigner.PublicKey()), uint64(s.chainSvc.GetFinalizedEpoch())) {
		// Still check reveals — we may have bids from before deactivation.
		s.checkSlotForReveal(ctx, currentSlot, now, msIntoSlot)
		return
	}

	// Check slots that might need bidding (current slot + next slot for negative bid start times)
	s.checkSlotForBidding(ctx, currentSlot, now, msIntoSlot)
	s.checkSlotForBidding(ctx, currentSlot+1, now, msIntoSlot-int64(s.chainSvc.GetChainSpec().SecondsPerSlot.Milliseconds()))

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

	if s.propPrefCache == nil || !s.propPrefCache.Has(slot) {
		s.log.WithField("slot", slot).Debug("No proposer preferences for slot yet — skipping bid")
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

	// Calculate bid value.
	// Start from block value with BidMinAmount as a floor.
	// BlockValue is in wei; BidMinAmount/BidIncrease/BidSubsidy are in gwei.
	bidBase := new(big.Int).Div(payload.BlockValue, big.NewInt(1_000_000_000)).Uint64()
	if s.cfg.BidMinAmount > bidBase {
		bidBase = s.cfg.BidMinAmount
	}

	bidValue := bidBase
	if s.cfg.BidInterval > 0 && state.BidCount > 0 {
		bidValue = bidBase + uint64(state.BidCount)*s.cfg.BidIncrease
	}

	// Bid subsidy: the proposer's BN rejects our bid and self-builds when its local EL
	// build is worth more than ours. The subsidy pads the bid to clear that threshold.
	// Configurable via --epbs-bid-subsidy (gwei).
	bidValue += s.cfg.BidSubsidy

	s.mu.Unlock()

	s.log.WithFields(logrus.Fields{
		"slot":         slot,
		"bid_value":    bidValue,
		"bid_count":    state.BidCount,
		"block_hash":   fmt.Sprintf("%x", payload.BlockHash[:8]),
		"ms_into_slot": msRelativeToSlot,
	}).Info("Creating and submitting bid")

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

	// Increment stats (count each bid submission)
	if s.service != nil && s.service.builderSvc != nil {
		s.service.builderSvc.IncrementBidsSubmitted()
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

	if !state.BidIncluded || state.Revealed || state.IncludedInBlock == nil {
		s.mu.Unlock()
		return
	}

	// Stop once we've exhausted our retry budget — don't spin the tick loop.
	if state.RevealAttempts >= maxRevealAttempts {
		s.mu.Unlock()
		return
	}

	// Space retries out so a failing reveal doesn't hammer the beacon node.
	if state.RevealAttempts > 0 && now.Sub(state.LastRevealAttempt) < revealRetryDelay {
		s.mu.Unlock()
		return
	}

	blockInfo := state.IncludedInBlock
	state.RevealAttempts++
	state.LastRevealAttempt = now
	attempt := state.RevealAttempts
	s.mu.Unlock()

	s.log.WithFields(logrus.Fields{
		"slot":         slot,
		"ms_into_slot": msIntoSlot,
		"attempt":      attempt,
		"max_attempts": maxRevealAttempts,
	}).Info("Revealing payload")

	// fireRevealFailure logs the error, surfaces it to the UI, and notes when the
	// retry budget is exhausted.
	fireRevealFailure := func(err error) {
		s.log.WithError(err).WithFields(logrus.Fields{
			"slot":         slot,
			"attempt":      attempt,
			"max_attempts": maxRevealAttempts,
		}).Error("Failed to submit reveal")

		if s.service != nil {
			s.service.FireReveal(&RevealEvent{
				Slot:        slot,
				Success:     false,
				Error:       err.Error(),
				Attempt:     attempt,
				MaxAttempts: maxRevealAttempts,
			})
		}

		// Count one logical failure per slot once the retry budget is spent.
		if attempt >= maxRevealAttempts {
			s.log.WithField("slot", slot).Error("Giving up on reveal after max attempts")

			if s.service != nil && s.service.builderSvc != nil {
				s.service.builderSvc.IncrementRevealsFailed()
			}
		}
	}

	// Get payload for reveal
	payload := s.payloadStore.Get(slot)
	if payload == nil {
		fireRevealFailure(fmt.Errorf("no payload found for reveal"))
		return
	}

	s.log.WithFields(logrus.Fields{
		"slot":         slot,
		"block_root":   fmt.Sprintf("%x", blockInfo.Root[:8]),
		"parent_root":  fmt.Sprintf("%x", blockInfo.ParentRoot[:8]),
		"block_hash":   fmt.Sprintf("%x", payload.BlockHash[:8]),
		"ms_into_slot": msIntoSlot,
		"attempt":      attempt,
	}).Info("Submitting reveal")

	err := s.revealHandler.SubmitReveal(ctx, payload, blockInfo)
	if err != nil {
		fireRevealFailure(err)
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
func (s *Scheduler) UpdateConfig(cfg *config.EPBSConfig) {
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
