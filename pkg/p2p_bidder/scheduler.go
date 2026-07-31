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
	"github.com/ethpandaops/buildoor/pkg/builder_keys"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// SlotState tracks the bidding state for a single slot.
type SlotState struct {
	LastBidTime      time.Time
	LastBidHash      phase0.Hash32
	BidCount         int
	BidsClosed       bool        // Block received, no more bids possible
	ClosedByRoot     phase0.Root // block root that closed bidding (reopens if orphaned)
	NoPrefsWarnedFor bool        // Missing-preferences skip already reported for this slot
	NoKeyWarnedFor   bool        // No-ready-key skip already reported for this slot
	// BidPayloads tracks the last bid time per payload: interval throttling
	// and single-bid dedup are PER PAYLOAD, so multi-candidate bidding
	// ("all") does not starve the other candidates behind one payload's
	// interval gate.
	BidPayloads map[phase0.Hash32]time.Time

	// BidCandidate is the candidate the auto selection committed to on the
	// first bid of the slot (sticky unless candidate switching is enabled).
	BidCandidate    chain.CandidateKey
	BidCandidateSet bool

	// PayloadKeys pins which builder key bids each payload. The pairing is
	// sticky for the slot: an interval re-bid from a different key would be a
	// fresh first-seen bid, leaving the original key's lower bid as the one
	// that actually propagated.
	PayloadKeys map[phase0.Hash32]uint64

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
	registry       *builder_keys.Registry
	propPrefsStore *memstore.Store[phase0.Slot, *gloasspec.SignedProposerPreferences]
	planSvc        *action_plan.PlanService // per-slot scheduling/settings authority
	cfg            *config.Config           // shared config; mutable settings read live
	log            logrus.FieldLogger

	// Simple state tracking per slot
	slotStates map[phase0.Slot]*SlotState
	mu         sync.Mutex
}

// NewScheduler creates a new scheduler. planSvc is the mandatory per-slot
// action plan service: every bid setting the scheduler acts on comes from its
// frozen slot snapshots, never from the live config — except the bid
// candidate selection, which is deliberately live (the whole point is
// choosing at bid time, after the plan froze).
func NewScheduler(
	chainSvc chain.Service,
	bidCreator *BidCreator,
	bidTracker *BidTracker,
	payloadCache *payload_builder.PayloadCache,
	service *Service,
	registry *builder_keys.Registry,
	propPrefsStore *memstore.Store[phase0.Slot, *gloasspec.SignedProposerPreferences],
	planSvc *action_plan.PlanService,
	cfg *config.Config,
	log logrus.FieldLogger,
) *Scheduler {
	return &Scheduler{
		chainSvc:       chainSvc,
		bidCreator:     bidCreator,
		bidTracker:     bidTracker,
		payloadCache:   payloadCache,
		service:        service,
		registry:       registry,
		propPrefsStore: propPrefsStore,
		planSvc:        planSvc,
		cfg:            cfg,
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
		slotState.ClosedByRoot = event.Block
		s.log.WithField("slot", event.Slot).Debug("Bidding closed for slot (block received)")
	}
}

// OnHeadChange reopens bidding for slots whose closing block was reorged out:
// with the block gone, the slot's proposer opportunity is live again for the
// rest of its bid window.
func (s *Scheduler) OnHeadChange(ctx context.Context, change *chain.HeadChangeEvent) {
	if change.ReorgDepth == 0 || change.Old == nil {
		return
	}

	headTracker := s.chainSvc.GetHeadTracker()
	if headTracker == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for slot, state := range s.slotStates {
		if !state.BidsClosed || state.ClosedByRoot == (phase0.Root{}) {
			continue
		}

		if headTracker.IsCanonical(ctx, state.ClosedByRoot) {
			continue
		}

		state.BidsClosed = false
		state.ClosedByRoot = phase0.Root{}

		s.log.WithFields(logrus.Fields{
			"slot": slot,
		}).Info("Reopening bidding for slot (closing block was reorged out)")
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

	// Don't bid unless at least one managed key is active on chain; which key
	// each bid is signed with is decided per bid.
	if !s.registry.AnyActive() {
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

	// Select the candidate payload(s) to bid on. The selection runs at bid
	// time — after builds finished and with the freshest chain view.
	payloads := s.selectBidPayloads(slot, bidSettings)
	if len(payloads) == 0 {
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
				"block_hash": fmt.Sprintf("%x", payloads[0].BlockHash[:8]),
			}).Warn("No proposer preferences for slot — skipping bids " +
				"(cache refills from gossip within ~1 epoch after a restart)")

			if s.service != nil {
				s.service.FireBidSubmission(&BidSubmissionEvent{
					Slot:      slot,
					BlockHash: payloads[0].BlockHash,
					Success:   false,
					Warning:   "no proposer preferences for slot — bid skipped",
				})
			}
		}

		return
	}

	for _, payload := range payloads {
		key := s.assignBidKey(slot, bidSettings, payload)
		if key == nil {
			continue
		}

		s.trySubmitBid(ctx, slot, now, msRelativeToSlot, bidSettings, key, payload, prefsBypassed)
	}
}

// selectBidPayloads returns the built payload(s) the slot's bids commit to,
// per the live bid-candidate setting: a specific candidate, every built
// candidate ("all"), or the auto selection matching the chain view (sticky
// per slot unless candidate switching is enabled).
func (s *Scheduler) selectBidPayloads(
	slot phase0.Slot, bidSettings *action_plan.ResolvedBidSettings,
) []*payload_builder.Payload {
	// The frozen per-slot selection wins; the live config covers snapshots
	// frozen before the setting existed.
	mode := bidSettings.BidCandidate
	if mode == "" {
		mode = s.cfg.EPBS.BidCandidate
	}

	switch {
	case mode == "all":
		return s.payloadCache.GetSlotPayloads(slot)

	case mode != "" && mode != "auto":
		if !chain.IsValidCandidateKey(mode) {
			s.log.WithField("bid_candidate", mode).
				Warn("Unknown bid candidate setting, falling back to auto selection")
			break
		}

		if payload := s.payloadCache.GetCandidate(slot, chain.CandidateKey(mode)); payload != nil {
			return []*payload_builder.Payload{payload}
		}

		return nil
	}

	s.mu.Lock()
	state := s.getSlotState(slot)
	chosen, chosenSet := state.BidCandidate, state.BidCandidateSet
	s.mu.Unlock()

	if chosenSet && !s.cfg.EPBS.BidCandidateSwitch {
		// Sticky: keep bidding the committed candidate (the gossip first-seen
		// rule makes a switched bid unlikely to propagate anyway); fall back
		// to the primary payload when that candidate produced none.
		if payload := s.payloadCache.GetCandidate(slot, chosen); payload != nil {
			return []*payload_builder.Payload{payload}
		}

		if payload := s.payloadCache.Get(slot); payload != nil {
			return []*payload_builder.Payload{payload}
		}

		return nil
	}

	payload := s.preferredPayload(slot)
	if payload == nil {
		return nil
	}

	s.mu.Lock()
	state = s.getSlotState(slot)

	if state.BidCandidateSet && state.BidCandidate != payload.Candidate {
		s.log.WithFields(logrus.Fields{
			"slot": slot,
			"from": state.BidCandidate,
			"to":   payload.Candidate,
		}).Warn("Switching bid candidate mid-slot (chain view changed)")
	}

	state.BidCandidate = payload.Candidate
	state.BidCandidateSet = true
	s.mu.Unlock()

	return []*payload_builder.Payload{payload}
}

// assignBidKey returns the builder key that bids the given payload in this
// slot, selecting one on first use and keeping it for every later bid on that
// payload.
//
// Each key bids at most once per slot: the gossip rules ignore a builder's
// later bids for a slot, so a key already committed to another candidate is
// excluded. That pairing is what turns several built candidates into several
// bids that actually propagate.
func (s *Scheduler) assignBidKey(
	slot phase0.Slot, bidSettings *action_plan.ResolvedBidSettings, payload *payload_builder.Payload,
) *builder_keys.Key {
	s.mu.Lock()
	state := s.getSlotState(slot)

	if keyIndex, ok := state.PayloadKeys[payload.BlockHash]; ok {
		s.mu.Unlock()

		key, err := s.registry.Key(keyIndex)
		if err != nil {
			s.log.WithError(err).WithField("key_index", keyIndex).
				Error("Committed bid key is no longer derivable")

			return nil
		}

		return key
	}

	// Cap the number of distinct keys bidding this slot. Unset means one key
	// per selected candidate payload.
	if limit := bidSettings.BidKeysPerSlot; limit > 0 && uint64(len(state.PayloadKeys)) >= limit {
		s.mu.Unlock()

		return nil
	}

	committed := make(map[uint64]struct{}, len(state.PayloadKeys))
	for _, keyIndex := range state.PayloadKeys {
		committed[keyIndex] = struct{}{}
	}

	s.mu.Unlock()

	required := baseBidValue(bidSettings, payload)

	selected := s.registry.SelectForBid(slot, builder_keys.SelectRequest{
		Strategy:     bidSettings.KeyStrategy,
		RequiredGwei: required,
		Count:        1,
		Exclude:      committed,
	})

	if len(selected) == 0 && len(committed) > 0 {
		// Fewer keys than candidates: reuse an already-committed key rather
		// than dropping the bid. Only the key's first bid propagates under the
		// gossip rules, but bidding several candidates from one key is a
		// deliberate testing scenario (bid_candidate: all).
		selected = s.registry.SelectForBid(slot, builder_keys.SelectRequest{
			Strategy:     bidSettings.KeyStrategy,
			RequiredGwei: required,
			Count:        1,
		})
	}

	if len(selected) == 0 {
		s.mu.Lock()
		state = s.getSlotState(slot)
		alreadyWarned := state.NoKeyWarnedFor
		state.NoKeyWarnedFor = true
		s.mu.Unlock()

		if !alreadyWarned {
			s.log.WithFields(logrus.Fields{
				"slot":           slot,
				"committed_keys": len(committed),
				"required_gwei":  required,
				"strategy":       builder_keys.NormalizedStrategy(bidSettings.KeyStrategy),
			}).Warn("No active builder key for slot — bid skipped")
		}

		return nil
	}

	key := selected[0]

	s.mu.Lock()
	state = s.getSlotState(slot)

	if state.PayloadKeys == nil {
		state.PayloadKeys = make(map[phase0.Hash32]uint64, 2)
	}

	state.PayloadKeys[payload.BlockHash] = key.KeyIndex()
	s.mu.Unlock()

	return key
}

// baseBidValue is the bid value before the per-re-bid increase: what a key must
// be able to cover to be worth selecting.
func baseBidValue(
	bidSettings *action_plan.ResolvedBidSettings, payload *payload_builder.Payload,
) uint64 {
	if bidSettings.ValueGwei != nil {
		return *bidSettings.ValueGwei
	}

	return max(weiToGweiClamped(payload.BlockValue), bidSettings.MinGwei) + bidSettings.SubsidyGwei
}

// preferredPayload picks the built payload matching the chain view's current
// head and its payload status, falling back to the cache's primary payload.
func (s *Scheduler) preferredPayload(slot phase0.Slot) *payload_builder.Payload {
	headTracker := s.chainSvc.GetHeadTracker()
	if headTracker != nil {
		if head := headTracker.CurrentHead(); head != nil && head.Slot < slot {
			hash := head.ExecutionBlockHash
			if headTracker.GetPayloadStatus(head.Root) == chain.PayloadStatusEmpty {
				hash = head.FinalitySafeExecutionBlockHash
			}

			key := beacon.AttrParentKey{Root: head.Root, Hash: hash}
			if payload := s.payloadCache.GetVariant(slot, key); payload != nil {
				return payload
			}
		}
	}

	return s.payloadCache.Get(slot)
}

// trySubmitBid runs the per-payload bid checks (window close, interval,
// single-bid dedup), computes the bid value and submits.
func (s *Scheduler) trySubmitBid(
	ctx context.Context,
	slot phase0.Slot,
	now time.Time,
	msRelativeToSlot int64,
	bidSettings *action_plan.ResolvedBidSettings,
	key *builder_keys.Key,
	payload *payload_builder.Payload,
	prefsBypassed bool,
) {
	builderIndex, _ := key.BuilderIndex()

	s.mu.Lock()
	state := s.getSlotState(slot)

	// Check if we should bid
	// - Not if bidding is closed (block already received)
	// - Not if we bid too recently (respect interval)
	// - Not if we already bid this payload (single bid mode)
	if state.BidsClosed {
		s.mu.Unlock()
		return
	}

	// Check bid interval (per payload, so "all" mode candidates do not
	// throttle each other)
	lastBid, alreadyBid := state.BidPayloads[payload.BlockHash]

	if bidSettings.IntervalMs > 0 {
		if alreadyBid && time.Since(lastBid) < time.Duration(bidSettings.IntervalMs)*time.Millisecond {
			s.mu.Unlock()
			return
		}
	} else {
		// Single bid mode - only bid payloads we have not bid yet.
		if alreadyBid {
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

	// Claim the payload's bid slot BEFORE releasing the lock. The submission
	// below is a network call taking tens of milliseconds while the scheduler
	// ticks every 10ms: marking the payload only afterwards lets the next ticks
	// pass this very check and gossip the same bid again, which the beacon node
	// rejects as already known and which burns the key's one bid for the slot.
	state.LastBidTime = now
	state.LastBidHash = payload.BlockHash

	if state.BidPayloads == nil {
		state.BidPayloads = make(map[phase0.Hash32]time.Time, 2)
	}

	state.BidPayloads[payload.BlockHash] = now
	state.BidCount++
	bidCount := state.BidCount

	s.mu.Unlock()

	s.log.WithFields(logrus.Fields{
		"slot":         slot,
		"key":          key.String(),
		"bid_value":    bidValue,
		"bid_count":    bidCount,
		"block_hash":   fmt.Sprintf("%x", payload.BlockHash[:8]),
		"ms_into_slot": msRelativeToSlot,
	}).Info("Creating and submitting bid")

	// Submit bid, applying the slot's frozen bid transform if any.
	var bidTransform string
	if state.Frozen != nil && state.Frozen.Transforms != nil {
		bidTransform = state.Frozen.Transforms.Bid
	}

	signedBid, err := s.bidCreator.CreateAndSubmitBid(ctx, key, payload, bidValue, bidTransform)

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

	if high, ok := s.bidTracker.GetHighestCompetitorBid(slot, payload.Attributes.ParentBlockHash); ok {
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

	s.registry.RecordBid(key.KeyIndex())

	// Track the bid
	s.bidTracker.TrackBid(&ExecutionPayloadBid{
		Slot:            slot,
		BuilderIndex:    builderIndex,
		Value:           bidValue,
		BlockHash:       payload.BlockHash,
		ParentBlockHash: payload.Attributes.ParentBlockHash,
		ParentBlockRoot: payload.Attributes.ParentBlockRoot,
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
