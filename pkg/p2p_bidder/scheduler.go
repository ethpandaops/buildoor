package p2p_bidder

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"sync"
	"time"

	gloasspec "github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
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

	// Frozen is the slot's immutable action-plan snapshot, resolved on the
	// first scheduler evaluation of the slot (nil until then).
	Frozen *action_plan.FrozenPlan
}

// Scheduler handles time-based bid scheduling.
// It uses a simple loop that checks current time and triggers actions.
type Scheduler struct {
	chainSvc       chain.Service
	bidCreator     *BidCreator
	bidTracker     *BidTracker
	payloadCache   *payload_builder.PayloadCache
	service        *Service // Reference to parent service for firing events
	blsSigner      *signer.BLSSigner
	propPrefsStore *memstore.Store[phase0.Slot, *gloasspec.SignedProposerPreferences]
	planSvc        *action_plan.PlanService // per-slot scheduling/settings authority
	log            logrus.FieldLogger

	// Simple state tracking per slot
	slotStates map[phase0.Slot]*SlotState
	mu         sync.Mutex
}

// NewScheduler creates a new scheduler. planSvc is the mandatory per-slot
// action plan service: every bid setting the scheduler acts on comes from its
// frozen slot snapshots, never from the live config.
func NewScheduler(
	chainSvc chain.Service,
	bidCreator *BidCreator,
	bidTracker *BidTracker,
	payloadCache *payload_builder.PayloadCache,
	service *Service,
	blsSigner *signer.BLSSigner,
	propPrefsStore *memstore.Store[phase0.Slot, *gloasspec.SignedProposerPreferences],
	planSvc *action_plan.PlanService,
	log logrus.FieldLogger,
) *Scheduler {
	return &Scheduler{
		chainSvc:       chainSvc,
		bidCreator:     bidCreator,
		bidTracker:     bidTracker,
		payloadCache:   payloadCache,
		service:        service,
		blsSigner:      blsSigner,
		propPrefsStore: propPrefsStore,
		planSvc:        planSvc,
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

// effectiveBidSettings returns the effective bid parameters for the slot, or
// nil when bidding is suppressed for it. The frozen per-slot snapshot
// (resolved on the first evaluation of the slot and cached on its state) is
// the sole enable/parameter authority — it already encodes the global
// epbs_enabled flag at freeze time or a per-slot plan override, so the
// service enable flag is NOT consulted here.
func (s *Scheduler) effectiveBidSettings(slot phase0.Slot) *action_plan.ResolvedBidSettings {
	s.mu.Lock()
	frozen := s.getSlotState(slot).Frozen
	s.mu.Unlock()

	if frozen == nil {
		// First evaluation of the slot: freeze the plan (idempotent) and cache
		// the snapshot for every later tick of this slot.
		frozen = s.planSvc.Freeze(slot)

		s.mu.Lock()
		s.getSlotState(slot).Frozen = frozen
		s.mu.Unlock()
	}

	return frozen.Bid
}

// checkSlotForBidding checks if we should bid for this slot.
func (s *Scheduler) checkSlotForBidding(ctx context.Context, slot phase0.Slot, now time.Time, msRelativeToSlot int64) {
	bidSettings := s.effectiveBidSettings(slot)
	if bidSettings == nil {
		// Bidding is suppressed for this slot (by plan, global disable, or a
		// pre-Gloas target fork).
		return
	}

	// Are we in bid period for this slot?
	// msRelativeToSlot is negative if we're before the slot starts
	if msRelativeToSlot < bidSettings.StartMs || msRelativeToSlot >= bidSettings.EndMs {
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
	// once per slot (this runs on a 10ms tick). A plan may bypass the gate
	// (ignore_missing_prefs): the bid then commits to the payload's own fee
	// recipient, which BidCreator uses anyway.
	prefsMissing := s.propPrefsStore == nil || !s.propPrefsStore.Has(slot)
	prefsBypassed := prefsMissing && bidSettings.IgnoreMissingPrefs

	if prefsMissing && !prefsBypassed {
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
	if bidSettings.IntervalMs > 0 {
		if time.Since(state.LastBidTime) < time.Duration(bidSettings.IntervalMs)*time.Millisecond {
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

	// Calculate bid value (all gwei, overflow-clamped).
	// ValueGwei, when set, is an absolute base (per-slot custom value or the
	// global bid value override, resolved at freeze time) replacing the
	// max(blockValue, BidMinAmount) + BidSubsidy formula. The subsidy pads the
	// formula bid so it clears the proposer BN's local-build threshold.
	var bidBase uint64

	if bidSettings.ValueGwei != nil {
		bidBase = *bidSettings.ValueGwei
	} else {
		bidBase = max(weiToGweiClamped(payload.BlockValue), bidSettings.MinGwei)
		bidBase = s.addGweiClamped(slot, bidBase, bidSettings.SubsidyGwei)
	}

	// Re-bid increase applies in interval mode regardless of the base source.
	bidValue := bidBase
	if bidSettings.IntervalMs > 0 && state.BidCount > 0 {
		increase := s.mulGweiClamped(slot, uint64(state.BidCount), bidSettings.IncreaseGwei) //nolint:gosec // BidCount >= 0
		bidValue = s.addGweiClamped(slot, bidValue, increase)
	}

	s.mu.Unlock()

	s.log.WithFields(logrus.Fields{
		"slot":         slot,
		"bid_value":    bidValue,
		"bid_count":    state.BidCount,
		"block_hash":   fmt.Sprintf("%x", payload.BlockHash[:8]),
		"ms_into_slot": msRelativeToSlot,
	}).Info("Creating and submitting bid")

	// Submit bid, applying the slot's frozen bid transform if any.
	var bidTransform string
	if state.Frozen != nil && state.Frozen.Transforms != nil {
		bidTransform = state.Frozen.Transforms.Bid
	}

	signedBid, err := s.bidCreator.CreateAndSubmitBid(ctx, payload, bidValue, bidTransform)

	// Update state regardless of success - we don't want to spam on failure
	s.mu.Lock()
	state.LastBidTime = now
	state.LastBidHash = payload.BlockHash
	state.BidCount++
	bidCount := state.BidCount
	s.mu.Unlock()

	event := &BidSubmissionEvent{
		Slot:      slot,
		BlockHash: payload.BlockHash,
		Value:     bidValue,
		BidCount:  bidCount,
		SignedBid: signedBid,
	}

	if prefsBypassed {
		event.Warning = "no proposer preferences for slot — bid sent anyway (ignore_missing_prefs)"
	}

	if high, ok := s.bidTracker.GetHighestCompetitorBid(slot, s.bidCreator.GetBuilderIndex()); ok {
		event.CompetitorHighGwei = &high
	}

	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Error("Failed to submit bid")

		// Constructed-but-not-submitted bids carry the signed bid object so
		// consumers can record exactly what was built.
		event.Error = err.Error()
		event.Status = BidStatusFailed

		if signedBid != nil {
			event.Status = BidStatusConstructed
		}

		if s.service != nil {
			s.service.FireBidSubmission(event)
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
	event.Success = true
	event.Status = BidStatusSubmitted

	if s.service != nil {
		s.service.FireBidSubmission(event)
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

// weiToGweiClamped converts a wei amount to gwei, clamping to MaxUint64 when
// the result does not fit (and to 0 for a nil value).
func weiToGweiClamped(wei *big.Int) uint64 {
	if wei == nil {
		return 0
	}

	gwei := new(big.Int).Div(wei, big.NewInt(1_000_000_000))
	if !gwei.IsUint64() {
		return math.MaxUint64
	}

	return gwei.Uint64()
}

// addGweiClamped adds two gwei amounts, clamping to MaxUint64 on overflow
// instead of wrapping silently.
func (s *Scheduler) addGweiClamped(slot phase0.Slot, a, b uint64) uint64 {
	sum, carry := bits.Add64(a, b, 0)
	if carry != 0 {
		s.log.WithFields(logrus.Fields{
			"slot": slot,
			"a":    a,
			"b":    b,
		}).Warn("Bid value addition overflowed, clamping to MaxUint64")

		return math.MaxUint64
	}

	return sum
}

// mulGweiClamped multiplies two gwei amounts, clamping to MaxUint64 on
// overflow instead of wrapping silently.
func (s *Scheduler) mulGweiClamped(slot phase0.Slot, a, b uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	if hi != 0 {
		s.log.WithFields(logrus.Fields{
			"slot": slot,
			"a":    a,
			"b":    b,
		}).Warn("Bid value multiplication overflowed, clamping to MaxUint64")

		return math.MaxUint64
	}

	return lo
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

// GetBidTracker returns the bid tracker.
func (s *Scheduler) GetBidTracker() *BidTracker {
	return s.bidTracker
}
