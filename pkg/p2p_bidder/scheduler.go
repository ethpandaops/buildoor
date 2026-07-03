package p2p_bidder

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	gloasspec "github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// SlotState tracks the bidding state for a single slot.
type SlotState struct {
	LastBidTime      time.Time
	LastBidHash      phase0.Hash32
	BidCount         int
	BidsClosed       bool // Block received, no more bids possible
	NoPrefsWarnedFor bool // Missing-preferences skip already reported for this slot
}

// Scheduler handles time-based bid scheduling.
// It uses a simple loop that checks current time and triggers actions.
type Scheduler struct {
	cfg            *config.EPBSConfig
	chainSvc       chain.Service
	bidCreator     *BidCreator
	bidTracker     *BidTracker
	payloadCache   *payload_builder.PayloadCache
	service        *Service // Reference to parent service for firing events
	blsSigner      *signer.BLSSigner
	propPrefsStore *memstore.Store[phase0.Slot, *gloasspec.SignedProposerPreferences]
	log            logrus.FieldLogger

	// Simple state tracking per slot
	slotStates map[phase0.Slot]*SlotState
	mu         sync.Mutex
}

// NewScheduler creates a new scheduler.
func NewScheduler(
	cfg *config.EPBSConfig,
	chainSvc chain.Service,
	bidCreator *BidCreator,
	bidTracker *BidTracker,
	payloadCache *payload_builder.PayloadCache,
	service *Service,
	blsSigner *signer.BLSSigner,
	propPrefsStore *memstore.Store[phase0.Slot, *gloasspec.SignedProposerPreferences],
	log logrus.FieldLogger,
) *Scheduler {
	return &Scheduler{
		cfg:            cfg,
		chainSvc:       chainSvc,
		bidCreator:     bidCreator,
		bidTracker:     bidTracker,
		payloadCache:   payloadCache,
		service:        service,
		blsSigner:      blsSigner,
		propPrefsStore: propPrefsStore,
		slotStates:     make(map[phase0.Slot]*SlotState),
		log:            log.WithField("component", "scheduler"),
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

// OnHeadEvent closes bidding for the slot — once a block is produced, no more bids can make it.
func (s *Scheduler) OnHeadEvent(event *beacon.HeadEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	slotState := s.getSlotState(event.Slot)
	if !slotState.BidsClosed {
		slotState.BidsClosed = true
		s.log.WithField("slot", event.Slot).Debug("Bidding closed for slot (block received)")
	}
}

// ProcessTick is called frequently to check if any bids are due.
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
	// genesis) — the bid windows are slot-relative.
	msIntoSlot := (elapsed % s.chainSvc.GetChainSpec().SecondsPerSlot).Milliseconds()

	// ePBS bids are only valid from the Gloas fork onwards.
	if s.chainSvc.GetCurrentFork() < version.DataVersionGloas {
		return
	}

	// Don't bid if the builder is not active on-chain.
	if !chain.IsBuilderActive(s.chainSvc.GetBuilderByPubkey(s.blsSigner.PublicKey()), uint64(s.chainSvc.GetFinalizedEpoch())) {
		return
	}

	// Check slots that might need bidding (current slot + next slot for negative bid start times)
	s.checkSlotForBidding(ctx, currentSlot, now, msIntoSlot)
	s.checkSlotForBidding(ctx, currentSlot+1, now, msIntoSlot-int64(s.chainSvc.GetChainSpec().SecondsPerSlot.Milliseconds()))
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

	// Bidding is gated on the proposer's gossip preferences: without them we
	// don't know the fee recipient to commit to. The cache is empty right
	// after a restart and refills from gossip within roughly an epoch, so this
	// is expected transiently — but it must be visible, not silent. Report
	// once per slot (this runs on a 10ms tick).
	if s.propPrefsStore == nil || !s.propPrefsStore.Has(slot) {
		s.mu.Lock()
		state := s.getSlotState(slot)
		alreadyWarned := state.NoPrefsWarnedFor
		state.NoPrefsWarnedFor = true
		s.mu.Unlock()

		if !alreadyWarned {
			s.log.WithFields(logrus.Fields{
				"slot":       slot,
				"block_hash": fmt.Sprintf("%x", payload.BlockHash[:8]),
			}).Warn("No proposer preferences for slot — skipping bids " +
				"(cache refills from gossip within ~1 epoch after a restart)")

			if s.service != nil {
				s.service.FireBidSubmission(&BidSubmissionEvent{
					Slot:      slot,
					BlockHash: payload.BlockHash,
					Success:   false,
					Warning:   "no proposer preferences for slot — bid skipped",
				})
			}
		}

		return
	}

	s.mu.Lock()
	state := s.getSlotState(slot)

	// Check if we should bid
	// - Not if bidding is closed (block already received)
	// - Not if we bid too recently (respect interval)
	// - Not if payload hasn't changed and we already bid (single bid mode)
	if state.BidsClosed {
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

// GetBidTracker returns the bid tracker.
func (s *Scheduler) GetBidTracker() *BidTracker {
	return s.bidTracker
}
