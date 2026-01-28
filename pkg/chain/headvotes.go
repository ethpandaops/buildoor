package chain

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// HeadVoteUpdate is dispatched when head vote participation changes.
type HeadVoteUpdate struct {
	Slot             phase0.Slot
	ParticipationPct float64
	ParticipationETH uint64
	TotalSlotETH     uint64
	Timestamp        int64
}

// slotVoteState tracks per-slot head vote participation using a single
// global bitlist that spans all committees assigned to the slot.
type slotVoteState struct {
	headRoot         phase0.Root
	voteBitlist      []byte              // one bit per validator across all committees
	committeeSizes   []int               // size of each committee
	committeeOffsets []int               // cumulative bit offset for each committee
	totalMembers     int                 // total validators across all committees
	flatIndices      []ActiveIndiceIndex // all committee members, flattened
	participatingETH uint64
	totalSlotETH     uint64
	lastPct          float64
}

// HeadVoteTracker tracks head vote participation per slot by processing
// attestation events against known attester duties.
type HeadVoteTracker struct {
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
	chainSvc Service,
	clClient *beacon.Client,
	log logrus.FieldLogger,
) *HeadVoteTracker {
	return &HeadVoteTracker{
		chainSvc:         chainSvc,
		clClient:         clClient,
		log:              log.WithField("component", "head-vote-tracker"),
		slotStates:       make(map[phase0.Slot]*slotVoteState, 4),
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

func (t *HeadVoteTracker) run() {
	defer t.wg.Done()

	headSub := t.clClient.Events().SubscribeHead()
	defer headSub.Unsubscribe()

	attSub := t.clClient.Events().SubscribeAttestations()
	defer attSub.Unsubscribe()

	for {
		select {
		case <-t.ctx.Done():
			return
		case event := <-headSub.Channel():
			t.handleHeadEvent(event)
		case event := <-attSub.Channel():
			t.handleAttestationEvent(event)
		}
	}
}

// handleHeadEvent initializes a fresh vote state for the new head slot.
// If we already have a state for this slot with the same head root, skip
// re-initialization to avoid resetting accumulated vote bits.
func (t *HeadVoteTracker) handleHeadEvent(event *beacon.HeadEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Skip re-initialization if we already track this slot with the same root.
	if existing, ok := t.slotStates[event.Slot]; ok && existing.headRoot == event.Block {
		return
	}

	spec := t.chainSvc.GetChainSpec()
	epoch := phase0.Epoch(uint64(event.Slot) / spec.SlotsPerEpoch)

	stats := t.chainSvc.GetEpochStats(epoch)
	if stats == nil {
		t.log.WithField("epoch", epoch).Debug(
			"No epoch stats available for head vote tracking",
		)
		return
	}

	slotIndex := uint64(event.Slot) % spec.SlotsPerEpoch
	if stats.AttesterDuties == nil ||
		slotIndex >= uint64(len(stats.AttesterDuties)) {
		return
	}

	committees := stats.AttesterDuties[slotIndex]

	// Compute committee sizes, offsets, and flat indices.
	totalMembers := 0
	committeeSizes := make([]int, len(committees))
	committeeOffsets := make([]int, len(committees))

	for i, committee := range committees {
		committeeOffsets[i] = totalMembers
		committeeSizes[i] = len(committee)
		totalMembers += len(committee)
	}

	flatIndices := make([]ActiveIndiceIndex, 0, totalMembers)
	for _, committee := range committees {
		flatIndices = append(flatIndices, committee...)
	}

	// Compute total ETH assigned to this slot.
	var totalSlotETH uint64
	for _, idx := range flatIndices {
		totalSlotETH += uint64(stats.EffectiveBalances[idx])
	}

	bitlistLen := (totalMembers + 7) / 8
	t.slotStates[event.Slot] = &slotVoteState{
		headRoot:         event.Block,
		voteBitlist:      make([]byte, bitlistLen),
		committeeSizes:   committeeSizes,
		committeeOffsets: committeeOffsets,
		totalMembers:     totalMembers,
		flatIndices:      flatIndices,
		totalSlotETH:     totalSlotETH,
	}

	// Cleanup states older than 4 slots.
	for slot := range t.slotStates {
		if event.Slot > 4 && slot < event.Slot-4 {
			delete(t.slotStates, slot)
		}
	}

	t.log.WithFields(logrus.Fields{
		"slot":          event.Slot,
		"committees":    len(committees),
		"total_members": totalMembers,
		"total_eth":     totalSlotETH,
	}).Debug("Initialized head vote state")
}

// handleAttestationEvent merges attestation bits and fires updates
// when participation changes significantly.
func (t *HeadVoteTracker) handleAttestationEvent(event *beacon.AttestationEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Look up the vote state for the attestation's slot.
	voteState, ok := t.slotStates[event.Slot]
	if !ok {
		return
	}

	// Only accept attestations voting for the head root we're tracking.
	if event.BeaconBlockRoot != voteState.headRoot {
		return
	}

	if !t.mergeAttestationBits(voteState, event) {
		return
	}

	// Recalculate participating ETH.
	spec := t.chainSvc.GetChainSpec()
	epoch := phase0.Epoch(uint64(event.Slot) / spec.SlotsPerEpoch)

	stats := t.chainSvc.GetEpochStats(epoch)
	if stats == nil {
		return
	}

	var participatingETH uint64
	for i := 0; i < voteState.totalMembers; i++ {
		if isBitSet(voteState.voteBitlist, i) {
			idx := voteState.flatIndices[i]
			participatingETH += uint64(stats.EffectiveBalances[idx])
		}
	}

	voteState.participatingETH = participatingETH

	var pct float64
	if voteState.totalSlotETH > 0 {
		pct = float64(participatingETH) / float64(voteState.totalSlotETH) * 100.0
	}

	// Only fire update if participation changed by >= 0.5 percentage points.
	if pct-voteState.lastPct < 0.5 && voteState.lastPct > 0 {
		return
	}

	voteState.lastPct = pct

	t.log.WithFields(logrus.Fields{
		"slot":      event.Slot,
		"pct":       fmt.Sprintf("%.1f%%", pct),
		"eth":       participatingETH,
		"total_eth": voteState.totalSlotETH,
	}).Debug("Firing head vote update")

	t.updateDispatcher.Fire(&HeadVoteUpdate{
		Slot:             event.Slot,
		ParticipationPct: pct,
		ParticipationETH: participatingETH,
		TotalSlotETH:     voteState.totalSlotETH,
		Timestamp:        time.Now().UnixMilli(),
	})
}

// mergeAttestationBits ORs attestation bits into the slot's global vote bitlist.
// Handles both Electra+ format (committee_bits selects committees, aggregation_bits
// is concatenated) and pre-Electra format (Index identifies a single committee).
// Returns true if any new bits were set.
func (t *HeadVoteTracker) mergeAttestationBits(
	state *slotVoteState,
	event *beacon.AttestationEvent,
) bool {
	changed := false

	if event.CommitteeBits != nil {
		// Electra+ format: committee_bits is a bitvector indicating which
		// committees are included. aggregation_bits is a concatenated bitlist
		// of the selected committees' members (with a trailing sentinel bit).
		aggBitPos := 0

		for ci := 0; ci < len(state.committeeSizes); ci++ {
			if !isBitSet(event.CommitteeBits, ci) {
				continue
			}

			offset := state.committeeOffsets[ci]
			size := state.committeeSizes[ci]

			for j := 0; j < size; j++ {
				if isBitSet(event.AggregationBits, aggBitPos) &&
					!isBitSet(state.voteBitlist, offset+j) {
					setBit(state.voteBitlist, offset+j)
					changed = true
				}

				aggBitPos++
			}
		}
	} else {
		// Pre-Electra format: Index directly identifies the committee.
		committeeIdx := int(event.Index)
		if committeeIdx >= len(state.committeeSizes) {
			return false
		}

		offset := state.committeeOffsets[committeeIdx]
		size := state.committeeSizes[committeeIdx]

		for j := 0; j < size; j++ {
			if isBitSet(event.AggregationBits, j) &&
				!isBitSet(state.voteBitlist, offset+j) {
				setBit(state.voteBitlist, offset+j)
				changed = true
			}
		}
	}

	return changed
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
