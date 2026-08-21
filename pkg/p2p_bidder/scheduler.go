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

// payloadBidState is one payload's own bidding progress within a slot.
type payloadBidState struct {
	// lastBid gates the bid interval.
	lastBid time.Time
	// count is how often this payload has been bid. It also drives the value
	// escalation: every bid lands one increase above the last, since gossip only
	// forwards the highest bid seen for a (slot, parent) tuple and bids sharing
	// a value cannot all propagate. It is per payload so several candidates do
	// not inherit each other's escalation.
	count int
}

// SlotState tracks the bidding state for a single slot.
type SlotState struct {
	LastBidTime      time.Time
	LastBidHash      phase0.Hash32
	BidCount         int
	BidsClosed       bool        // Block received, no more bids possible
	ClosedByRoot     phase0.Root // block root that closed bidding (reopens if orphaned)
	NoPrefsWarnedFor bool        // Missing-preferences skip already reported for this slot
	NoKeyWarnedFor   bool        // No-ready-key skip already reported for this slot

	// PayloadBids tracks each payload's own bid progress, so multi-candidate
	// bidding ("all") does not starve the other candidates behind one payload's
	// interval gate.
	PayloadBids map[phase0.Hash32]*payloadBidState

	// BidCandidate is the candidate the auto selection committed to on the
	// first bid of the slot (sticky unless candidate switching is enabled).
	BidCandidate    chain.CandidateKey
	BidCandidateSet bool

	// UsedKeys holds the keys that already gossiped a bid for this slot. The
	// gossip rules ignore every bid a builder makes after its first for a slot,
	// so a key is SPENT once one of its bids reached the network — every later
	// bid, including an escalated re-bid of the very same payload, has to come
	// from a key that has not bid yet or it never propagates.
	UsedKeys map[uint64]struct{}

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
	cfgSvc         *config.Service          // settings source; one snapshot per selection
	log            logrus.FieldLogger

	// Simple state tracking per slot
	slotStates map[phase0.Slot]*SlotState
	mu         sync.Mutex

	// wg tracks in-flight bid submissions so shutdown can wait for them. They
	// deliberately do NOT block the tick that started them.
	wg sync.WaitGroup
}

// Wait blocks until every in-flight bid submission has finished. Called during
// shutdown, after the context is cancelled.
func (s *Scheduler) Wait() {
	s.wg.Wait()
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
	cfgSvc *config.Service,
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
		cfgSvc:         cfgSvc,
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

	// Every planned bid is an independent key on an independent payload, so the
	// submissions go out concurrently AND detached from this tick: a submission
	// takes tens of milliseconds while the scheduler ticks every 10ms, so
	// waiting on them here would stall the next step — and the next slot — for
	// the duration of the slowest beacon call. All the state a later tick reads
	// (the payload's interval claim, each key's claim) is already committed by
	// planBidStep before any of these start.
	for _, payload := range payloads {
		for _, planned := range s.planBidStep(slot, now, bidSettings, payload) {
			s.wg.Add(1)

			go func() {
				defer s.wg.Done()

				s.submitBid(ctx, slot, msRelativeToSlot, bidSettings, planned, payload, prefsBypassed)
			}()
		}
	}
}

// selectBidPayloads returns the built payload(s) the slot's bids commit to,
// per the live bid-candidate setting: a specific candidate, every built
// candidate ("all"), or the auto selection matching the chain view (sticky
// per slot unless candidate switching is enabled).
func (s *Scheduler) selectBidPayloads(
	slot phase0.Slot, bidSettings *action_plan.ResolvedBidSettings,
) []*payload_builder.Payload {
	// One config snapshot per selection. The frozen per-slot selection wins;
	// the snapshot covers plans frozen before the setting existed.
	cfg := s.cfgSvc.Current()

	mode := bidSettings.BidCandidate
	if mode == "" {
		mode = cfg.EPBS.BidCandidate
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

	if chosenSet && !cfg.EPBS.BidCandidateSwitch {
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

// claimBidKey reserves a builder key for one bid on the given payload, or
// returns nil when the slot has no key left to spend.
//
// The gossip rules ignore every bid a builder makes after its first for a slot,
// so a key is spent as soon as one of its bids reaches the network. Every later
// bid therefore needs a key that has not bid yet — including an escalated re-bid
// of the very same payload, which is exactly what makes the escalation reach the
// network instead of being dropped as already known.
//
// The key is claimed here rather than after the submission because the
// submission is a network call taking tens of milliseconds while the scheduler
// ticks every 10ms: the ticks landing mid-flight would otherwise pick the same
// key again. releaseBidKey hands it back when the submission never made it out.
func (s *Scheduler) claimBidKey(
	slot phase0.Slot, bidSettings *action_plan.ResolvedBidSettings, bidValue uint64,
) *builder_keys.Key {
	s.mu.Lock()
	state := s.getSlotState(slot)

	// Cap how many keys one slot may spend. Unset means every ready key.
	if limit := bidSettings.BidKeysPerSlot; limit > 0 && uint64(len(state.UsedKeys)) >= limit {
		s.mu.Unlock()

		return nil
	}

	spent := make(map[uint64]struct{}, len(state.UsedKeys))
	for keyIndex := range state.UsedKeys {
		spent[keyIndex] = struct{}{}
	}

	s.mu.Unlock()

	selected := s.registry.SelectForBid(slot, builder_keys.SelectRequest{
		Strategy:     bidSettings.KeyStrategy,
		RequiredGwei: bidValue,
		Count:        1,
		Exclude:      spent,
	})

	if len(selected) == 0 {
		s.mu.Lock()
		state = s.getSlotState(slot)
		alreadyWarned := state.NoKeyWarnedFor
		state.NoKeyWarnedFor = true
		s.mu.Unlock()

		if !alreadyWarned {
			s.log.WithFields(logrus.Fields{
				"slot":       slot,
				"spent_keys": len(spent),
				"strategy":   builder_keys.NormalizedStrategy(bidSettings.KeyStrategy),
			}).Info("Every builder key has bid this slot — no further bid can propagate")
		}

		return nil
	}

	key := selected[0]

	s.mu.Lock()
	state = s.getSlotState(slot)

	if state.UsedKeys == nil {
		state.UsedKeys = make(map[uint64]struct{}, 4)
	}

	// Re-check the cap: a concurrent tick may have claimed the last allowed key
	// while this one was selecting.
	if limit := bidSettings.BidKeysPerSlot; limit > 0 && uint64(len(state.UsedKeys)) >= limit {
		s.mu.Unlock()

		return nil
	}

	if _, taken := state.UsedKeys[key.KeyIndex()]; taken {
		s.mu.Unlock()

		return nil
	}

	state.UsedKeys[key.KeyIndex()] = struct{}{}
	s.mu.Unlock()

	return key
}

// releaseBidKey returns a claimed key to the slot's pool after a submission that
// never reached the network: nothing was gossiped, so the key is not spent.
func (s *Scheduler) releaseBidKey(slot phase0.Slot, key *builder_keys.Key) {
	s.mu.Lock()
	delete(s.getSlotState(slot).UsedKeys, key.KeyIndex())
	s.mu.Unlock()
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

// plannedBid is one bid decided by a step: which key signs it, at what value.
type plannedBid struct {
	key      *builder_keys.Key
	value    uint64
	bidCount int
}

// planBidStep decides the bids one evaluation of a payload should submit.
//
// A step spends up to BidKeysPerStep keys at once. That is what lets a slot be
// bid from several keys in parallel instead of one key per interval tick: the
// interval ladder is a single-key shape, and with a fleet the useful shapes run
// from "one key per step, escalating" all the way to "every key at once".
//
// All bookkeeping happens under one lock — the payload's interval claim, its
// escalation count and each key's claim — so the ticks that land while the
// submissions are in flight cannot re-decide the same bids.
func (s *Scheduler) planBidStep(
	slot phase0.Slot,
	now time.Time,
	bidSettings *action_plan.ResolvedBidSettings,
	payload *payload_builder.Payload,
) []plannedBid {
	s.mu.Lock()

	state := s.getSlotState(slot)

	// - Not if bidding is closed (block already received)
	// - Not if we bid this payload too recently (respect interval)
	// - Not if we already bid it at all (single bid mode)
	if state.BidsClosed {
		s.mu.Unlock()
		return nil
	}

	bidState, alreadyBid := state.PayloadBids[payload.BlockHash]

	if bidSettings.IntervalMs > 0 {
		if alreadyBid && time.Since(bidState.lastBid) < time.Duration(bidSettings.IntervalMs)*time.Millisecond {
			s.mu.Unlock()
			return nil
		}
	} else if alreadyBid {
		s.mu.Unlock()
		return nil
	}

	if state.PayloadBids == nil {
		state.PayloadBids = make(map[phase0.Hash32]*payloadBidState, 2)
	}

	if bidState == nil {
		bidState = &payloadBidState{}
		state.PayloadBids[payload.BlockHash] = bidState
	}

	bidState.lastBid = now
	state.LastBidTime = now
	state.LastBidHash = payload.BlockHash

	s.mu.Unlock()

	planned := make([]plannedBid, 0, 4)

	// Every bid gets its own value, one increase above the last. Gossip only
	// forwards a bid that is the highest seen for the (slot, parent) tuple, so
	// bids sharing a value cannot all propagate however many keys sign them —
	// all but the first to arrive are dropped as too low.
	for range bidSettings.EffectiveKeysPerStep() {
		s.mu.Lock()
		escalations := bidState.count
		s.mu.Unlock()

		value := s.bidValueFor(slot, bidSettings, payload, escalations)

		key := s.claimBidKey(slot, bidSettings, value)
		if key == nil {
			break
		}

		s.mu.Lock()
		state = s.getSlotState(slot)
		bidState.count++
		state.BidCount++
		bidCount := state.BidCount
		s.mu.Unlock()

		planned = append(planned, plannedBid{key: key, value: value, bidCount: bidCount})
	}

	if len(planned) == 0 {
		// Nothing was actually bid, so the payload's interval claim must not
		// stand — the next tick has to re-evaluate it.
		s.mu.Lock()
		if bidState.count == 0 {
			delete(s.getSlotState(slot).PayloadBids, payload.BlockHash)
		} else {
			bidState.lastBid = time.Time{}
		}
		s.mu.Unlock()
	}

	return planned
}

// bidValueFor computes the bid value for the payload's next bid.
//
// ValueGwei, when set, is an absolute base (per-slot custom value or the global
// bid value override, resolved at freeze time) replacing the
// max(blockValue, BidMinAmount) + BidSubsidy formula. The subsidy pads the
// formula bid so it clears the proposer BN's local-build threshold.
//
// escalations is how many bids this payload already has, so every bid lands one
// increase above the last and each one can be the new highest for the tuple.
func (s *Scheduler) bidValueFor(
	slot phase0.Slot,
	bidSettings *action_plan.ResolvedBidSettings,
	payload *payload_builder.Payload,
	escalations int,
) uint64 {
	var value uint64

	if bidSettings.ValueGwei != nil {
		value = *bidSettings.ValueGwei
	} else {
		value = max(weiToGweiClamped(payload.BlockValue), bidSettings.MinGwei)
		value = s.addGweiClamped(slot, value, bidSettings.SubsidyGwei)
	}

	if escalations > 0 {
		increase := s.mulGweiClamped(slot, uint64(escalations), bidSettings.IncreaseGwei) //nolint:gosec // escalations > 0
		value = s.addGweiClamped(slot, value, increase)
	}

	return value
}

// submitBid gossips one planned bid and reports the outcome.
func (s *Scheduler) submitBid(
	ctx context.Context,
	slot phase0.Slot,
	msRelativeToSlot int64,
	bidSettings *action_plan.ResolvedBidSettings,
	planned plannedBid,
	payload *payload_builder.Payload,
	prefsBypassed bool,
) {
	builderIndex, _ := planned.key.BuilderIndex()

	s.log.WithFields(logrus.Fields{
		"slot":         slot,
		"key":          planned.key.String(),
		"bid_value":    planned.value,
		"bid_count":    planned.bidCount,
		"block_hash":   fmt.Sprintf("%x", payload.BlockHash[:8]),
		"ms_into_slot": msRelativeToSlot,
	}).Info("Creating and submitting bid")

	// Submit bid, applying the slot's frozen bid transform if any.
	var bidTransform string

	s.mu.Lock()
	if frozen := s.getSlotState(slot).Frozen; frozen != nil && frozen.Transforms != nil {
		bidTransform = frozen.Transforms.Bid
	}
	s.mu.Unlock()

	signedBid, err := s.bidCreator.CreateAndSubmitBid(ctx, planned.key, payload, planned.value, bidTransform)

	event := &BidSubmissionEvent{
		Slot:      slot,
		BlockHash: payload.BlockHash,
		Value:     planned.value,
		BidCount:  planned.bidCount,
		SignedBid: signedBid,
	}

	if prefsBypassed {
		event.Warning = "no proposer preferences for slot — bid sent anyway (ignore_missing_prefs)"
	}

	if high, ok := s.bidTracker.GetHighestCompetitorBid(slot, payload.Attributes.ParentBlockHash); ok {
		event.CompetitorHighGwei = &high
	}

	if err != nil {
		// A rejection means the beacon node saw the bid and turned it down, so
		// the key stays spent: retrying it unchanged only repeats the rejection.
		// Only a submission that never reached the node hands its key back.
		rejected := beacon.BidRejected(err)
		if !rejected {
			s.releaseBidKey(slot, planned.key)
		}

		// Rejections are the normal outcome of bidding a whole fleet: the node
		// keeps only the best bid per parent, so every key beyond the first to
		// arrive at a given value is turned down. Logging those at error level
		// buries the transport failures that actually need attention — the
		// per-attempt record on the slot result keeps them all inspectable.
		logLevel := logrus.ErrorLevel
		if rejected {
			logLevel = logrus.DebugLevel
		}

		s.log.WithError(err).WithFields(logrus.Fields{
			"slot": slot,
			"key":  planned.key.String(),
		}).Log(logLevel, "Failed to submit bid")

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

	s.registry.RecordBid(planned.key.KeyIndex())

	// Track the bid
	s.bidTracker.TrackBid(&ExecutionPayloadBid{
		Slot:            slot,
		BuilderIndex:    builderIndex,
		Value:           planned.value,
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
		"key":        planned.key.String(),
		"bid_value":  planned.value,
		"bid_count":  planned.bidCount,
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
