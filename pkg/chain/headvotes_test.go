package chain

import (
	"context"
	"testing"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// stubChainService implements Service with just enough state for the head
// vote tracker: chain spec, epoch stats, and the wall-clock slot.
type stubChainService struct {
	spec        *ChainSpec
	stats       map[phase0.Epoch]*EpochStats
	currentSlot phase0.Slot
}

var _ Service = (*stubChainService)(nil)

func (s *stubChainService) Start(_ context.Context) error { return nil }
func (s *stubChainService) Stop() error                   { return nil }
func (s *stubChainService) GetChainSpec() *ChainSpec      { return s.spec }
func (s *stubChainService) GetGenesis() *beacon.Genesis   { return nil }
func (s *stubChainService) SlotToTime(_ phase0.Slot) time.Time {
	return time.Time{}
}
func (s *stubChainService) TimeToSlot(_ time.Time) phase0.Slot { return 0 }
func (s *stubChainService) GetCurrentEpoch() phase0.Epoch {
	return phase0.Epoch(uint64(s.currentSlot) / s.spec.SlotsPerEpoch)
}
func (s *stubChainService) GetCurrentSlot() phase0.Slot { return s.currentSlot }
func (s *stubChainService) GetCurrentFork() version.DataVersion {
	return version.DataVersionGloas
}
func (s *stubChainService) ActiveForkAtEpoch(_ phase0.Epoch) version.DataVersion {
	return version.DataVersionGloas
}
func (s *stubChainService) GetForkVersion() (phase0.Version, error) {
	return phase0.Version{}, nil
}
func (s *stubChainService) GetEpochOfSlot(slot phase0.Slot) phase0.Epoch {
	return phase0.Epoch(uint64(slot) / s.spec.SlotsPerEpoch)
}
func (s *stubChainService) GetCurrentEpochStats() *EpochStats {
	return s.stats[s.GetCurrentEpoch()]
}
func (s *stubChainService) GetEpochStats(epoch phase0.Epoch) *EpochStats {
	return s.stats[epoch]
}
func (s *stubChainService) SubscribeEpochStats() *utils.Subscription[*EpochStats] {
	return nil
}
func (s *stubChainService) GetHeadVoteTracker() *HeadVoteTracker { return nil }
func (s *stubChainService) GetFinalizedEpoch() phase0.Epoch      { return 0 }
func (s *stubChainService) GetBuilderByIndex(_ uint64) *BuilderInfo {
	return nil
}
func (s *stubChainService) GetBuilderByPubkey(_ phase0.BLSPubKey) *BuilderInfo {
	return nil
}
func (s *stubChainService) GetBuilders() []*BuilderInfo { return nil }
func (s *stubChainService) GetValidatorPubkeyByIndex(_ phase0.ValidatorIndex) *phase0.BLSPubKey {
	return nil
}
func (s *stubChainService) RefreshBuilders(_ context.Context) error { return nil }

// newVoteTestTracker builds a tracker over a synthetic epoch: slotsPerEpoch=4,
// the given per-slot committees (validator index == active indice index), and
// 32 ETH effective balance per validator.
func newVoteTestTracker(
	t *testing.T,
	thresholdPct uint64,
	currentSlot phase0.Slot,
	committees map[phase0.Slot][][]ActiveIndiceIndex,
	validatorCount int,
) (*HeadVoteTracker, *stubChainService) {
	t.Helper()

	spec := &ChainSpec{SlotsPerEpoch: 4, SecondsPerSlot: 12 * time.Second}

	stats := make(map[phase0.Epoch]*EpochStats, 2)

	for slot, slotCommittees := range committees {
		epoch := phase0.Epoch(uint64(slot) / spec.SlotsPerEpoch)

		es, ok := stats[epoch]
		if !ok {
			es = &EpochStats{
				Epoch:             epoch,
				ActiveIndices:     make([]phase0.ValidatorIndex, validatorCount),
				EffectiveBalances: make([]uint32, validatorCount),
				AttesterDuties:    make([][][]ActiveIndiceIndex, spec.SlotsPerEpoch),
			}
			for i := range validatorCount {
				es.ActiveIndices[i] = phase0.ValidatorIndex(i)
				es.EffectiveBalances[i] = 32
			}

			stats[epoch] = es
		}

		es.AttesterDuties[uint64(slot)%spec.SlotsPerEpoch] = slotCommittees
	}

	chainSvc := &stubChainService{
		spec:        spec,
		stats:       stats,
		currentSlot: currentSlot,
	}

	cfg := config.DefaultConfig()
	cfg.EPBS.HeadVoteThresholdPct = thresholdPct

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	return NewHeadVoteTracker(cfg, chainSvc, nil, log), chainSvc
}

// drainUpdates reads all buffered updates from the subscription.
func drainUpdates(sub *utils.Subscription[*HeadVoteUpdate]) []*HeadVoteUpdate {
	updates := make([]*HeadVoteUpdate, 0, 8)

	for {
		select {
		case u := <-sub.Channel():
			updates = append(updates, u)
		default:
			return updates
		}
	}
}

func singleVote(
	slot phase0.Slot,
	committee phase0.CommitteeIndex,
	attester phase0.ValidatorIndex,
	root phase0.Root,
) *beacon.SingleAttestationEvent {
	return &beacon.SingleAttestationEvent{
		Slot:            slot,
		CommitteeIndex:  committee,
		AttesterIndex:   attester,
		BeaconBlockRoot: root,
	}
}

func TestHeadVoteTrackerSingleAttestationMerge(t *testing.T) {
	rootA := phase0.Root{0xaa}
	tracker, _ := newVoteTestTracker(t, 0, 5, map[phase0.Slot][][]ActiveIndiceIndex{
		5: {{0, 1, 2}, {3, 4}},
	}, 8)

	// Votes are tracked without any head event (lazy state creation).
	tracker.handleSingleAttestation(singleVote(5, 0, 1, rootA))
	tracker.handleSingleAttestation(singleVote(5, 1, 3, rootA))
	// Duplicate vote must not double-count.
	tracker.handleSingleAttestation(singleVote(5, 0, 1, rootA))

	update, ok := tracker.GetParticipation(5, rootA)
	require.True(t, ok)
	assert.Equal(t, 2, update.VoteCount)
	assert.Equal(t, uint64(64), update.ParticipationETH)
	assert.Equal(t, uint64(160), update.TotalSlotETH)
	assert.InDelta(t, 40.0, update.ParticipationPct, 0.001)
	assert.Equal(t, 5, update.TotalMembers)
}

func TestHeadVoteTrackerRejectsInconsistentVotes(t *testing.T) {
	rootA := phase0.Root{0xaa}
	tracker, _ := newVoteTestTracker(t, 0, 5, map[phase0.Slot][][]ActiveIndiceIndex{
		5: {{0, 1, 2}, {3, 4}},
	}, 8)

	// Attester 3 sits in committee 1; a vote claiming committee 0 is rejected.
	tracker.handleSingleAttestation(singleVote(5, 0, 3, rootA))
	// Validator 7 has no duty in this slot.
	tracker.handleSingleAttestation(singleVote(5, 0, 7, rootA))
	// Committee index out of range.
	tracker.handleSingleAttestation(singleVote(5, 9, 0, rootA))
	// Slot outside the tracking window.
	tracker.handleSingleAttestation(singleVote(0, 0, 0, rootA))

	update, ok := tracker.GetParticipation(5, rootA)
	require.True(t, ok)
	assert.Zero(t, update.VoteCount, "no invalid vote may be counted")
	assert.Zero(t, update.ParticipationETH)

	_, ok = tracker.GetParticipation(0, rootA)
	require.False(t, ok, "out-of-window slots must not allocate state")
}

func TestHeadVoteTrackerBlockCoverageElectraFormat(t *testing.T) {
	rootA := phase0.Root{0xaa}
	tracker, _ := newVoteTestTracker(t, 0, 5, map[phase0.Slot][][]ActiveIndiceIndex{
		5: {{0, 1, 2}, {3, 4}},
	}, 8)

	// Raw votes for members 1 (committee 0) and 3 (committee 1) were seen.
	tracker.handleSingleAttestation(singleVote(5, 0, 1, rootA))
	tracker.handleSingleAttestation(singleVote(5, 1, 3, rootA))

	// Block attestation, Electra format: two overlapping attestations
	// covering members 0,1 of committee 0 and member 3 of committee 1
	// (concatenated bit positions 0,1 and 3) — overlaps count once.
	tracker.recordBlockAttestations([]*beacon.AttestationEvent{
		{
			Slot:            5,
			BeaconBlockRoot: rootA,
			CommitteeBits:   []byte{0b11},
			AggregationBits: []byte{0b01011},
		},
		{
			Slot:            5,
			BeaconBlockRoot: rootA,
			CommitteeBits:   []byte{0b01},
			AggregationBits: []byte{0b011},
		},
	})

	cov := tracker.GetSubnetCoverage()
	assert.Equal(t, 1, cov.Slots)
	assert.Equal(t, 3, cov.Attesters, "block attesters must dedupe across attestations")
	assert.InDelta(t, 2.0/3.0*100.0, cov.SeenPct, 0.001)
}

func TestHeadVoteTrackerBlockCoveragePreElectraFormat(t *testing.T) {
	rootA := phase0.Root{0xaa}
	tracker, _ := newVoteTestTracker(t, 0, 5, map[phase0.Slot][][]ActiveIndiceIndex{
		5: {{0, 1, 2}, {3, 4}},
	}, 8)

	tracker.handleSingleAttestation(singleVote(5, 1, 4, rootA))

	// Pre-Electra format: Index selects committee 1, bits cover both members.
	tracker.recordBlockAttestations([]*beacon.AttestationEvent{{
		Slot:            5,
		BeaconBlockRoot: rootA,
		Index:           1,
		AggregationBits: []byte{0b11},
	}})

	cov := tracker.GetSubnetCoverage()
	assert.Equal(t, 2, cov.Attesters)
	assert.InDelta(t, 50.0, cov.SeenPct, 0.001)
}

func TestHeadVoteTrackerRootSwitchPreservesBitmaps(t *testing.T) {
	rootA := phase0.Root{0xaa}
	rootB := phase0.Root{0xbb}
	tracker, _ := newVoteTestTracker(t, 0, 5, map[phase0.Slot][][]ActiveIndiceIndex{
		5: {{0, 1, 2}, {3, 4}},
	}, 8)

	tracker.handleSingleAttestation(singleVote(5, 0, 0, rootA))
	tracker.handleSingleAttestation(singleVote(5, 0, 1, rootA))
	tracker.handleSingleAttestation(singleVote(5, 1, 3, rootB))

	// Head names rootB; rootA's accumulated bitmap must survive.
	tracker.handleHeadEvent(&beacon.HeadEvent{Slot: 5, Block: rootB})

	updateA, ok := tracker.GetParticipation(5, rootA)
	require.True(t, ok)
	assert.Equal(t, 2, updateA.VoteCount)

	updateB, ok := tracker.GetParticipation(5, rootB)
	require.True(t, ok)
	assert.Equal(t, 1, updateB.VoteCount)
}

func TestHeadVoteTrackerThrottledFlush(t *testing.T) {
	rootA := phase0.Root{0xaa}

	// 300 members in one committee: each vote is 1/3 percentage point.
	committee := make([]ActiveIndiceIndex, 300)
	for i := range committee {
		committee[i] = ActiveIndiceIndex(i)
	}

	tracker, _ := newVoteTestTracker(t, 0, 5, map[phase0.Slot][][]ActiveIndiceIndex{
		5: {committee},
	}, 300)

	sub := tracker.SubscribeUpdates()
	defer sub.Unsubscribe()

	tracker.handleHeadEvent(&beacon.HeadEvent{Slot: 5, Block: rootA})

	// Two votes (0.67%) stay below the 1pp step: no update on flush.
	tracker.handleSingleAttestation(singleVote(5, 0, 0, rootA))
	tracker.handleSingleAttestation(singleVote(5, 0, 1, rootA))
	tracker.flushDirty()
	assert.Empty(t, drainUpdates(sub))

	// Two more votes (1.33% total) cross the step: exactly one update.
	tracker.handleSingleAttestation(singleVote(5, 0, 2, rootA))
	tracker.handleSingleAttestation(singleVote(5, 0, 3, rootA))
	tracker.flushDirty()

	updates := drainUpdates(sub)
	require.Len(t, updates, 1)
	assert.InDelta(t, 4.0/3.0, updates[0].ParticipationPct, 0.001)
	assert.Equal(t, rootA, updates[0].BlockRoot)
	assert.False(t, updates[0].ThresholdMet)

	// No new votes: repeated flushes stay silent.
	tracker.flushDirty()
	assert.Empty(t, drainUpdates(sub))
}

func TestHeadVoteTrackerThresholdFiresImmediately(t *testing.T) {
	rootA := phase0.Root{0xaa}
	tracker, _ := newVoteTestTracker(t, 60, 5, map[phase0.Slot][][]ActiveIndiceIndex{
		5: {{0, 1, 2, 3, 4}},
	}, 8)

	sub := tracker.SubscribeUpdates()
	defer sub.Unsubscribe()

	tracker.handleHeadEvent(&beacon.HeadEvent{Slot: 5, Block: rootA})

	// 40% < 60%: votes only mark dirty, nothing fires immediately.
	tracker.handleSingleAttestation(singleVote(5, 0, 0, rootA))
	tracker.handleSingleAttestation(singleVote(5, 0, 1, rootA))
	assert.Empty(t, drainUpdates(sub))

	// The third vote (60%) crosses the threshold and fires without a flush.
	tracker.handleSingleAttestation(singleVote(5, 0, 2, rootA))

	updates := drainUpdates(sub)
	require.Len(t, updates, 1)
	assert.True(t, updates[0].ThresholdMet)
	assert.InDelta(t, 60.0, updates[0].ParticipationPct, 0.001)
	assert.InDelta(t, 60.0, updates[0].ThresholdPct, 0.001)

	// Further votes do not re-fire the threshold event immediately.
	tracker.handleSingleAttestation(singleVote(5, 0, 3, rootA))
	assert.Empty(t, drainUpdates(sub))

	// The next throttled update still reports the threshold as met.
	tracker.flushDirty()

	updates = drainUpdates(sub)
	require.Len(t, updates, 1)
	assert.True(t, updates[0].ThresholdMet)
}

func TestHeadVoteTrackerThresholdDisabled(t *testing.T) {
	rootA := phase0.Root{0xaa}
	tracker, _ := newVoteTestTracker(t, 0, 5, map[phase0.Slot][][]ActiveIndiceIndex{
		5: {{0, 1}},
	}, 8)

	sub := tracker.SubscribeUpdates()
	defer sub.Unsubscribe()

	tracker.handleHeadEvent(&beacon.HeadEvent{Slot: 5, Block: rootA})
	tracker.handleSingleAttestation(singleVote(5, 0, 0, rootA))
	tracker.handleSingleAttestation(singleVote(5, 0, 1, rootA))

	// 100% participation with threshold 0: no immediate event, and the
	// flushed update reports the threshold as not met.
	updates := drainUpdates(sub)
	assert.Empty(t, updates)

	tracker.flushDirty()

	updates = drainUpdates(sub)
	require.Len(t, updates, 1)
	assert.False(t, updates[0].ThresholdMet)
	assert.Zero(t, updates[0].ThresholdPct)
}

func TestHeadVoteTrackerDeferredThresholdOnPrimarySwitch(t *testing.T) {
	rootA := phase0.Root{0xaa}
	rootB := phase0.Root{0xbb}
	tracker, _ := newVoteTestTracker(t, 60, 5, map[phase0.Slot][][]ActiveIndiceIndex{
		5: {{0, 1, 2, 3, 4}},
	}, 8)

	sub := tracker.SubscribeUpdates()
	defer sub.Unsubscribe()

	// rootA stays primary (most votes) and crosses the threshold itself;
	// rootB also crosses (60%) but is non-primary, so its crossing is
	// deferred.
	tracker.handleSingleAttestation(singleVote(5, 0, 0, rootA))
	tracker.handleSingleAttestation(singleVote(5, 0, 1, rootA))
	tracker.handleSingleAttestation(singleVote(5, 0, 2, rootA))
	tracker.handleSingleAttestation(singleVote(5, 0, 3, rootA))
	tracker.handleSingleAttestation(singleVote(5, 0, 0, rootB))
	tracker.handleSingleAttestation(singleVote(5, 0, 1, rootB))
	tracker.handleSingleAttestation(singleVote(5, 0, 2, rootB))

	// Exactly one immediate threshold event so far: rootA's own crossing.
	updates := drainUpdates(sub)
	require.Len(t, updates, 1)
	assert.Equal(t, rootA, updates[0].BlockRoot)
	assert.True(t, updates[0].ThresholdMet)

	// The head event makes rootB primary: its deferred crossing fires now.
	tracker.handleHeadEvent(&beacon.HeadEvent{Slot: 5, Block: rootB})

	updates = drainUpdates(sub)
	require.NotEmpty(t, updates)
	assert.Equal(t, rootB, updates[0].BlockRoot)
	assert.True(t, updates[0].ThresholdMet)
}

func TestHeadVoteTrackerRootCap(t *testing.T) {
	tracker, _ := newVoteTestTracker(t, 0, 5, map[phase0.Slot][][]ActiveIndiceIndex{
		5: {{0, 1}},
	}, 8)

	for i := range maxTrackedRootsPerSlot + 2 {
		root := phase0.Root{byte(i + 1)}
		tracker.handleSingleAttestation(singleVote(5, 0, 0, root))
	}

	tracker.mu.Lock()
	assert.Len(t, tracker.slotStates[5].roots, maxTrackedRootsPerSlot)
	tracker.mu.Unlock()

	// The head root must always be trackable: naming a fifth root as head
	// evicts a least-voted root instead of dropping the head root.
	headRoot := phase0.Root{0xff}
	tracker.handleHeadEvent(&beacon.HeadEvent{Slot: 5, Block: headRoot})
	tracker.handleSingleAttestation(singleVote(5, 0, 1, headRoot))

	update, ok := tracker.GetParticipation(5, headRoot)
	require.True(t, ok)
	assert.Equal(t, 1, update.VoteCount)

	tracker.mu.Lock()
	assert.Len(t, tracker.slotStates[5].roots, maxTrackedRootsPerSlot)
	tracker.mu.Unlock()
}

func TestHeadVoteTrackerPrunesOldSlots(t *testing.T) {
	rootA := phase0.Root{0xaa}
	tracker, chainSvc := newVoteTestTracker(t, 0, 5, map[phase0.Slot][][]ActiveIndiceIndex{
		5:  {{0, 1}},
		13: {{0, 1}},
	}, 8)

	tracker.handleSingleAttestation(singleVote(5, 0, 0, rootA))

	chainSvc.currentSlot = 13
	tracker.handleHeadEvent(&beacon.HeadEvent{Slot: 13, Block: rootA})

	_, ok := tracker.GetParticipation(5, rootA)
	assert.False(t, ok, "slots beyond the retention window must be pruned")
}

// TestHeadVoteTrackerTimestampsFromReceiveTime asserts that updates are
// stamped with the receive time of the vote that produced the state — not the
// processing/flush time — for both the immediate threshold event and the
// throttled flush.
func TestHeadVoteTrackerTimestampsFromReceiveTime(t *testing.T) {
	rootA := phase0.Root{0xaa}
	tracker, _ := newVoteTestTracker(t, 60, 5, map[phase0.Slot][][]ActiveIndiceIndex{
		5: {{0, 1, 2, 3, 4}},
	}, 8)

	sub := tracker.SubscribeUpdates()
	defer sub.Unsubscribe()

	tracker.handleHeadEvent(&beacon.HeadEvent{Slot: 5, Block: rootA})

	receivedAt := time.Now().Add(-3 * time.Second)

	vote := func(attester phase0.ValidatorIndex, at time.Time) *beacon.SingleAttestationEvent {
		v := singleVote(5, 0, attester, rootA)
		v.ReceivedAt = at

		return v
	}

	// 40%: below threshold, nothing fires yet.
	tracker.handleSingleAttestation(vote(0, receivedAt))
	tracker.handleSingleAttestation(vote(1, receivedAt.Add(100*time.Millisecond)))

	// The crossing vote's receive time stamps the immediate threshold event.
	crossedAt := receivedAt.Add(200 * time.Millisecond)
	tracker.handleSingleAttestation(vote(2, crossedAt))

	updates := drainUpdates(sub)
	require.Len(t, updates, 1)
	assert.True(t, updates[0].ThresholdMet)
	assert.Equal(t, crossedAt.UnixMilli(), updates[0].Timestamp)

	// A later throttled flush is stamped with the last vote's receive time,
	// not the flush time.
	lastVoteAt := receivedAt.Add(300 * time.Millisecond)
	tracker.handleSingleAttestation(vote(3, lastVoteAt))
	tracker.flushDirty()

	updates = drainUpdates(sub)
	require.Len(t, updates, 1)
	assert.Equal(t, lastVoteAt.UnixMilli(), updates[0].Timestamp)
}

// TestHeadVoteTrackerSubnetCoverageDetection flags sustained low singles
// coverage against block attestations and recovers once singles reappear.
func TestHeadVoteTrackerSubnetCoverageDetection(t *testing.T) {
	rootA := phase0.Root{0xaa}
	tracker, _ := newVoteTestTracker(t, 0, 5, map[phase0.Slot][][]ActiveIndiceIndex{
		5: {{0, 1, 2, 3}},
	}, 8)

	covSub := tracker.SubscribeCoverage()
	defer covSub.Unsubscribe()

	drainCoverage := func() []*SubnetCoverage {
		out := make([]*SubnetCoverage, 0, 8)
		for {
			select {
			case c := <-covSub.Channel():
				out = append(out, c)
			default:
				return out
			}
		}
	}

	blockAtt := &beacon.AttestationEvent{
		Slot:            5,
		BeaconBlockRoot: rootA,
		CommitteeBits:   []byte{0b1},
		AggregationBits: []byte{0b1111},
	}

	// Track the slot via its head event only (no singles seen at all) — the
	// coverage measurement must work for slots without any raw votes.
	tracker.handleHeadEvent(&beacon.HeadEvent{Slot: 5, Block: rootA})

	// 7 measured blocks: below the minimum sample count, never low.
	for range 7 {
		tracker.recordBlockAttestations([]*beacon.AttestationEvent{blockAtt})
	}

	for _, c := range drainCoverage() {
		assert.False(t, c.Low)
	}
	assert.False(t, tracker.GetSubnetCoverage().Low)

	// The 8th block (32 attesters total, 0% seen) trips the flag.
	tracker.recordBlockAttestations([]*beacon.AttestationEvent{blockAtt})

	updates := drainCoverage()
	require.NotEmpty(t, updates)
	assert.True(t, updates[len(updates)-1].Low)
	assert.Zero(t, updates[len(updates)-1].SeenPct)

	cov := tracker.GetSubnetCoverage()
	assert.True(t, cov.Low)
	assert.Equal(t, 32, cov.Attesters)

	// Singles reappear: the sliding window recovers and clears the flag.
	for i := phase0.ValidatorIndex(0); i < 4; i++ {
		tracker.handleSingleAttestation(singleVote(5, 0, i, rootA))
	}

	for range coverageWindowSlots {
		tracker.recordBlockAttestations([]*beacon.AttestationEvent{blockAtt})
	}

	cov = tracker.GetSubnetCoverage()
	assert.False(t, cov.Low)
	assert.InDelta(t, 100.0, cov.SeenPct, 0.001)
}
