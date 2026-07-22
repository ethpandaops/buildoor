package chain

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

const (
	// voteFlushInterval is how often dirty vote states are checked for a
	// throttled update. Together with voteFirePctStep it bounds the update
	// rate to at most one event per percent-step / interval per slot.
	voteFlushInterval = 100 * time.Millisecond
	// voteFirePctStep is the minimum participation advance (in percentage
	// points) between throttled updates.
	voteFirePctStep = 1.0
	// maxTrackedRootsPerSlot caps how many distinct beacon block roots are
	// tracked per slot (head + competing forks); votes for further roots are
	// dropped.
	maxTrackedRootsPerSlot = 4
	// voteSlotRetention is how many slots of vote state are kept before
	// pruning (relative to the latest head slot).
	voteSlotRetention = 4
)

// HeadVoteUpdate is dispatched when head vote participation changes.
type HeadVoteUpdate struct {
	Slot             phase0.Slot
	BlockRoot        phase0.Root
	ParticipationPct float64
	ParticipationETH uint64
	TotalSlotETH     uint64
	VoteCount        int
	TotalMembers     int
	ThresholdPct     float64 // effective threshold at fire time (0 = disabled)
	ThresholdMet     bool
	Timestamp        int64
}

// slotCommitteeLayout is the root-independent committee geometry of one slot,
// built once from the epoch's locally computed attester duties. The hot vote
// path only touches positions/weights — never the epoch stats.
type slotCommitteeLayout struct {
	committeeOffsets []int // per-committee global bit offset
	committeeSizes   []int
	totalMembers     int
	positions        map[phase0.ValidatorIndex]int32 // validator index -> global bit position
	weights          []uint32                        // effective balance in full ETH per bit position
	totalSlotETH     uint64
}

// rootVoteState is the vote bitmap for one (slot, beacon block root) pair.
// Bits map 1:1 to the slot layout's member positions; participation is
// accumulated incrementally as bits are set.
type rootVoteState struct {
	voteBits         []byte
	participatingETH uint64
	voteCount        int
	lastVoteAt       time.Time // receive time of the last merged vote; stamps updates
	lastFiredPct     float64
	thresholdFired   bool
	dirty            bool
}

// slotVoteState tracks all vote bitmaps of one slot. States are created lazily
// on the first vote seen for the slot — no dependency on the head event — so
// early raw attestations racing our node's block import are never dropped, and
// a head root change (reorg) keeps the accumulated bitmaps of both roots.
type slotVoteState struct {
	layout   *slotCommitteeLayout
	roots    map[phase0.Root]*rootVoteState
	headRoot phase0.Root // zero until a head event names the slot's head
}

// primary returns the root whose participation is streamed to subscribers:
// the head root once known, otherwise the root with the most votes.
func (s *slotVoteState) primary() (phase0.Root, *rootVoteState) {
	if rs, ok := s.roots[s.headRoot]; ok {
		return s.headRoot, rs
	}

	var (
		bestRoot phase0.Root
		best     *rootVoteState
	)

	for root, rs := range s.roots {
		if best == nil || rs.voteCount > best.voteCount {
			bestRoot, best = root, rs
		}
	}

	return bestRoot, best
}

// HeadVoteTracker tracks per-slot attestation participation by aggregating raw
// single_attestation events (streaming in from the Gloas attester deadline at
// 25% of the slot) into per-root bitmaps against locally computed attester
// duties. Aggregated attestation events are merged into the same bitmaps as a
// late-slot complement: the node only sees raw votes on subnets it subscribes
// to, while aggregates cover all committees by the aggregate deadline (50%).
// Updates are throttled to percent-steps on a flush interval; crossing the
// configured participation threshold fires immediately.
type HeadVoteTracker struct {
	cfg      *config.Config // shared live config; threshold read per check, never cached
	chainSvc Service
	clClient *beacon.Client
	log      logrus.FieldLogger

	mu         sync.Mutex
	slotStates map[phase0.Slot]*slotVoteState

	updateDispatcher *utils.Dispatcher[*HeadVoteUpdate]

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewHeadVoteTracker creates a new head vote tracker.
func NewHeadVoteTracker(
	cfg *config.Config,
	chainSvc Service,
	clClient *beacon.Client,
	log logrus.FieldLogger,
) *HeadVoteTracker {
	return &HeadVoteTracker{
		cfg:              cfg,
		chainSvc:         chainSvc,
		clClient:         clClient,
		log:              log.WithField("component", "head-vote-tracker"),
		slotStates:       make(map[phase0.Slot]*slotVoteState, voteSlotRetention),
		updateDispatcher: &utils.Dispatcher[*HeadVoteUpdate]{},
	}
}

// Start starts the head vote tracker.
func (t *HeadVoteTracker) Start(ctx context.Context) {
	t.ctx, t.cancel = context.WithCancel(ctx)

	t.wg.Add(1)
	go t.run()

	t.log.Info("Head vote tracker started")
}

// Stop stops the head vote tracker.
func (t *HeadVoteTracker) Stop() {
	if t.cancel != nil {
		t.cancel()
	}

	t.wg.Wait()
	t.log.Info("Head vote tracker stopped")
}

// SubscribeUpdates returns a subscription for head vote updates.
func (t *HeadVoteTracker) SubscribeUpdates() *utils.Subscription[*HeadVoteUpdate] {
	return t.updateDispatcher.Subscribe(64, false)
}

// GetParticipation returns the current participation snapshot for a tracked
// (slot, beacon block root) pair; ok is false when the pair is not tracked.
func (t *HeadVoteTracker) GetParticipation(slot phase0.Slot, root phase0.Root) (HeadVoteUpdate, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state, ok := t.slotStates[slot]
	if !ok {
		return HeadVoteUpdate{}, false
	}

	rs, ok := state.roots[root]
	if !ok {
		return HeadVoteUpdate{}, false
	}

	return *t.buildUpdate(slot, root, state.layout, rs), true
}

func (t *HeadVoteTracker) run() {
	defer t.wg.Done()

	headSub := t.clClient.Events().SubscribeHead()
	defer headSub.Unsubscribe()

	attSub := t.clClient.Events().SubscribeAttestations()
	defer attSub.Unsubscribe()

	singleSub := t.clClient.Events().SubscribeSingleAttestations()
	defer singleSub.Unsubscribe()

	ticker := time.NewTicker(voteFlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-t.ctx.Done():
			return
		case event := <-headSub.Channel():
			t.handleHeadEvent(event)
		case event := <-attSub.Channel():
			t.handleAttestationEvent(event)
		case event := <-singleSub.Channel():
			t.handleSingleAttestation(event)
		case <-ticker.C:
			t.flushDirty()
		}
	}
}

// thresholdPct returns the live-configured participation threshold in percent
// (0 = disabled). Read on every check so UI overrides apply immediately.
func (t *HeadVoteTracker) thresholdPct() float64 {
	if t.cfg == nil {
		return 0
	}

	return float64(t.cfg.EPBS.HeadVoteThresholdPct)
}

// handleHeadEvent marks the slot's head root as the primary tracked root and
// prunes stale slot states. Vote state creation does not depend on head
// events; this only names the primary root (and fires a deferred threshold
// update if the newly primary root already crossed it).
func (t *HeadVoteTracker) handleHeadEvent(event *beacon.HeadEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.getOrCreateSlotState(event.Slot)
	if state != nil && state.headRoot != event.Block {
		state.headRoot = event.Block

		// The head root must always be tracked: when the per-slot root cap is
		// already filled by other forks, evict the least-voted one.
		rs := t.getOrCreateRootState(state, event.Block)
		if rs == nil {
			evictLeastVotedRoot(state)
			rs = t.getOrCreateRootState(state, event.Block)
		}

		if rs != nil {
			t.maybeFireThreshold(event.Slot, state, event.Block, rs)

			if rs.voteCount > 0 {
				rs.dirty = true
			}
		}
	}

	// Cleanup states older than the retention window.
	for slot := range t.slotStates {
		if event.Slot > voteSlotRetention && slot < event.Slot-voteSlotRetention {
			delete(t.slotStates, slot)
		}
	}
}

// handleSingleAttestation merges one raw vote into its (slot, root) bitmap in
// O(1): position lookup, bit set, incremental balance accumulation.
func (t *HeadVoteTracker) handleSingleAttestation(event *beacon.SingleAttestationEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.getOrCreateSlotState(event.Slot)
	if state == nil {
		return
	}

	rs := t.getOrCreateRootState(state, event.BeaconBlockRoot)
	if rs == nil {
		return
	}

	layout := state.layout

	pos, ok := layout.positions[event.AttesterIndex]
	if !ok {
		return
	}

	// Sanity-check the claimed committee against the duty-derived position.
	ci := int(event.CommitteeIndex)
	if ci >= len(layout.committeeOffsets) ||
		int(pos) < layout.committeeOffsets[ci] ||
		int(pos) >= layout.committeeOffsets[ci]+layout.committeeSizes[ci] {
		return
	}

	if isBitSet(rs.voteBits, int(pos)) {
		return
	}

	setBit(rs.voteBits, int(pos))
	rs.participatingETH += uint64(layout.weights[pos])
	rs.voteCount++
	rs.lastVoteAt = event.ReceivedAt
	rs.dirty = true

	t.maybeFireThreshold(event.Slot, state, event.BeaconBlockRoot, rs)
}

// handleAttestationEvent merges an aggregated attestation's bits into its
// (slot, root) bitmap. Aggregates arrive by the aggregate deadline (50% of the
// slot) and backfill votes from subnets the node does not subscribe to.
func (t *HeadVoteTracker) handleAttestationEvent(event *beacon.AttestationEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.getOrCreateSlotState(event.Slot)
	if state == nil {
		return
	}

	rs := t.getOrCreateRootState(state, event.BeaconBlockRoot)
	if rs == nil {
		return
	}

	if !t.mergeAggregateBits(state.layout, rs, event) {
		return
	}

	rs.lastVoteAt = event.ReceivedAt
	rs.dirty = true

	t.maybeFireThreshold(event.Slot, state, event.BeaconBlockRoot, rs)
}

// getOrCreateSlotState returns the slot's vote state, lazily building the
// committee layout from the epoch's attester duties. Returns nil when the slot
// is outside the tracking window or its epoch stats are unavailable. Caller
// must hold t.mu.
func (t *HeadVoteTracker) getOrCreateSlotState(slot phase0.Slot) *slotVoteState {
	if state, ok := t.slotStates[slot]; ok {
		return state
	}

	// Only track slots around the wall-clock head; attestations for anything
	// older are noise and must not allocate state.
	current := t.chainSvc.GetCurrentSlot()
	if slot+voteSlotRetention < current || slot > current+1 {
		return nil
	}

	layout := t.buildCommitteeLayout(slot)
	if layout == nil {
		return nil
	}

	state := &slotVoteState{
		layout: layout,
		roots:  make(map[phase0.Root]*rootVoteState, maxTrackedRootsPerSlot),
	}
	t.slotStates[slot] = state

	t.log.WithFields(logrus.Fields{
		"slot":          slot,
		"committees":    len(layout.committeeSizes),
		"total_members": layout.totalMembers,
		"total_eth":     layout.totalSlotETH,
	}).Debug("Initialized head vote state")

	return state
}

// evictLeastVotedRoot removes the tracked root with the fewest votes from a
// slot state. Caller must hold t.mu.
func evictLeastVotedRoot(state *slotVoteState) {
	var (
		victim phase0.Root
		least  *rootVoteState
	)

	for root, rs := range state.roots {
		if least == nil || rs.voteCount < least.voteCount {
			victim, least = root, rs
		}
	}

	if least != nil {
		delete(state.roots, victim)
	}
}

// getOrCreateRootState returns the root's bitmap within a slot state, creating
// it unless the per-slot root cap is reached. Caller must hold t.mu.
func (t *HeadVoteTracker) getOrCreateRootState(state *slotVoteState, root phase0.Root) *rootVoteState {
	if rs, ok := state.roots[root]; ok {
		return rs
	}

	if len(state.roots) >= maxTrackedRootsPerSlot {
		return nil
	}

	rs := &rootVoteState{
		voteBits: make([]byte, (state.layout.totalMembers+7)/8),
	}
	state.roots[root] = rs

	return rs
}

// buildCommitteeLayout computes the slot's committee geometry from the cached
// epoch stats: bit offsets per committee, the validator-index -> bit-position
// map, and per-bit effective balances. Returns nil when stats are unavailable.
func (t *HeadVoteTracker) buildCommitteeLayout(slot phase0.Slot) *slotCommitteeLayout {
	spec := t.chainSvc.GetChainSpec()
	epoch := phase0.Epoch(uint64(slot) / spec.SlotsPerEpoch)

	stats := t.chainSvc.GetEpochStats(epoch)
	if stats == nil {
		t.log.WithField("epoch", epoch).Debug("No epoch stats available for head vote tracking")
		return nil
	}

	slotIndex := uint64(slot) % spec.SlotsPerEpoch
	if stats.AttesterDuties == nil || slotIndex >= uint64(len(stats.AttesterDuties)) {
		return nil
	}

	committees := stats.AttesterDuties[slotIndex]

	totalMembers := 0
	committeeSizes := make([]int, len(committees))
	committeeOffsets := make([]int, len(committees))

	for i, committee := range committees {
		committeeOffsets[i] = totalMembers
		committeeSizes[i] = len(committee)
		totalMembers += len(committee)
	}

	layout := &slotCommitteeLayout{
		committeeOffsets: committeeOffsets,
		committeeSizes:   committeeSizes,
		totalMembers:     totalMembers,
		positions:        make(map[phase0.ValidatorIndex]int32, totalMembers),
		weights:          make([]uint32, totalMembers),
	}

	pos := 0

	for _, committee := range committees {
		for _, aidx := range committee {
			layout.positions[stats.ActiveIndices[aidx]] = int32(pos)
			layout.weights[pos] = stats.EffectiveBalances[aidx]
			layout.totalSlotETH += uint64(stats.EffectiveBalances[aidx])
			pos++
		}
	}

	return layout
}

// mergeAggregateBits ORs an aggregate's bits into the root's bitmap, updating
// participation incrementally per newly set bit. Handles the Electra+ format
// (committee_bits selects committees, aggregation_bits is concatenated) and
// the pre-Electra format (Index identifies a single committee). Returns true
// if any new bit was set.
func (t *HeadVoteTracker) mergeAggregateBits(
	layout *slotCommitteeLayout,
	rs *rootVoteState,
	event *beacon.AttestationEvent,
) bool {
	changed := false

	mergeCommittee := func(offset, size, bitBase int) {
		for j := range size {
			if isBitSet(event.AggregationBits, bitBase+j) && !isBitSet(rs.voteBits, offset+j) {
				setBit(rs.voteBits, offset+j)

				rs.participatingETH += uint64(layout.weights[offset+j])
				rs.voteCount++
				changed = true
			}
		}
	}

	if event.CommitteeBits != nil {
		// Electra+: aggregation_bits concatenates the selected committees'
		// members (with a trailing sentinel bit beyond the walked range).
		aggBitPos := 0

		for ci := range layout.committeeSizes {
			if !isBitSet(event.CommitteeBits, ci) {
				continue
			}

			mergeCommittee(layout.committeeOffsets[ci], layout.committeeSizes[ci], aggBitPos)
			aggBitPos += layout.committeeSizes[ci]
		}
	} else {
		committeeIdx := int(event.Index)
		if committeeIdx >= len(layout.committeeSizes) {
			return false
		}

		mergeCommittee(layout.committeeOffsets[committeeIdx], layout.committeeSizes[committeeIdx], 0)
	}

	return changed
}

// maybeFireThreshold fires an immediate update when the primary root newly
// crosses the configured participation threshold. Caller must hold t.mu.
func (t *HeadVoteTracker) maybeFireThreshold(
	slot phase0.Slot,
	state *slotVoteState,
	root phase0.Root,
	rs *rootVoteState,
) {
	threshold := t.thresholdPct()
	if threshold <= 0 || rs.thresholdFired {
		return
	}

	primaryRoot, _ := state.primary()
	if root != primaryRoot {
		return
	}

	if t.participationPct(state.layout, rs) < threshold {
		return
	}

	rs.thresholdFired = true
	t.fire(slot, root, state.layout, rs)
}

// flushDirty fires throttled updates for every slot's primary root whose
// participation advanced at least voteFirePctStep since the last update.
func (t *HeadVoteTracker) flushDirty() {
	t.mu.Lock()
	defer t.mu.Unlock()

	for slot, state := range t.slotStates {
		root, rs := state.primary()
		if rs == nil || !rs.dirty {
			continue
		}

		if t.participationPct(state.layout, rs)-rs.lastFiredPct < voteFirePctStep {
			continue
		}

		t.fire(slot, root, state.layout, rs)
	}
}

// participationPct computes a root's participation in percent of the slot's
// total attesting balance.
func (t *HeadVoteTracker) participationPct(layout *slotCommitteeLayout, rs *rootVoteState) float64 {
	if layout.totalSlotETH == 0 {
		return 0
	}

	return float64(rs.participatingETH) / float64(layout.totalSlotETH) * 100.0
}

// buildUpdate assembles the update snapshot for a root's current state. The
// timestamp is the receive time of the last merged vote — the participation
// level was reached when that vote arrived, not when we got around to
// processing or flushing it.
func (t *HeadVoteTracker) buildUpdate(
	slot phase0.Slot,
	root phase0.Root,
	layout *slotCommitteeLayout,
	rs *rootVoteState,
) *HeadVoteUpdate {
	pct := t.participationPct(layout, rs)
	threshold := t.thresholdPct()

	at := rs.lastVoteAt
	if at.IsZero() {
		at = time.Now()
	}

	return &HeadVoteUpdate{
		Slot:             slot,
		BlockRoot:        root,
		ParticipationPct: pct,
		ParticipationETH: rs.participatingETH,
		TotalSlotETH:     layout.totalSlotETH,
		VoteCount:        rs.voteCount,
		TotalMembers:     layout.totalMembers,
		ThresholdPct:     threshold,
		ThresholdMet:     threshold > 0 && pct >= threshold,
		Timestamp:        at.UnixMilli(),
	}
}

// fire dispatches an update for the root and resets its throttle state.
// Caller must hold t.mu.
func (t *HeadVoteTracker) fire(
	slot phase0.Slot,
	root phase0.Root,
	layout *slotCommitteeLayout,
	rs *rootVoteState,
) {
	update := t.buildUpdate(slot, root, layout, rs)

	rs.lastFiredPct = update.ParticipationPct
	rs.dirty = false

	t.log.WithFields(logrus.Fields{
		"slot":          slot,
		"pct":           fmt.Sprintf("%.1f%%", update.ParticipationPct),
		"votes":         update.VoteCount,
		"eth":           update.ParticipationETH,
		"total_eth":     update.TotalSlotETH,
		"threshold_met": update.ThresholdMet,
	}).Debug("Firing head vote update")

	t.updateDispatcher.Fire(update)
}

// isBitSet checks if the bit at position pos is set in the byte slice.
func isBitSet(data []byte, pos int) bool {
	byteIdx := pos / 8
	if byteIdx >= len(data) {
		return false
	}

	return data[byteIdx]&(1<<uint(pos%8)) != 0
}

// setBit sets the bit at position pos in the byte slice.
func setBit(data []byte, pos int) {
	byteIdx := pos / 8
	if byteIdx >= len(data) {
		return
	}

	data[byteIdx] |= 1 << uint(pos%8)
}
