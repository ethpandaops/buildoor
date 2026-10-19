package slot_results

import (
	"fmt"
	"io"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	legacytypes "github.com/ethpandaops/buildoor/pkg/builderapi/legacy/types"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// stubChainService provides the minimal chain.Service surface used here.
type stubChainService struct {
	chain.Service

	spec        *chain.ChainSpec
	genesisTime time.Time
	currentSlot phase0.Slot
	fork        version.DataVersion
}

func newStubChain() *stubChainService {
	return &stubChainService{
		spec: &chain.ChainSpec{
			SecondsPerSlot: 12 * time.Second,
			SlotsPerEpoch:  32,
		},
		genesisTime: time.Now().Add(-12000 * time.Second),
		currentSlot: 1000,
		fork:        version.DataVersionGloas,
	}
}

func (s *stubChainService) GetChainSpec() *chain.ChainSpec { return s.spec }
func (s *stubChainService) GetCurrentSlot() phase0.Slot    { return s.currentSlot }
func (s *stubChainService) GetEpochOfSlot(slot phase0.Slot) phase0.Epoch {
	return phase0.Epoch(uint64(slot) / s.spec.SlotsPerEpoch)
}

func (s *stubChainService) ActiveForkAtEpoch(_ phase0.Epoch) version.DataVersion {
	return s.fork
}

func (s *stubChainService) SlotToTime(slot phase0.Slot) time.Time {
	return s.genesisTime.Add(time.Duration(slot) * s.spec.SecondsPerSlot)
}

func (s *stubChainService) SubscribeEpochStats() *utils.Subscription[*chain.EpochStats] {
	return (&utils.Dispatcher[*chain.EpochStats]{}).Subscribe(1, false)
}

type trackerTestEnv struct {
	cfg      *config.Config
	chainSvc *stubChainService
	planSvc  *action_plan.PlanService
	stateDB  *db.Database
	tracker  *Tracker
}

func newTrackerTestEnv(t *testing.T, withDB bool) *trackerTestEnv {
	t.Helper()

	log := logrus.New()
	log.SetOutput(io.Discard)

	cfg := config.DefaultConfig()
	cfg.EPBSEnabled = true
	cfg.BuilderAPIEnabled = true
	cfg.APIPort = 8080

	chainSvc := newStubChain()
	planSvc := action_plan.NewPlanService(cfg, chainSvc, log)

	dbFile := ""
	if withDB {
		dbFile = filepath.Join(t.TempDir(), "state.db")
	}

	stateDB := db.NewDatabase(&db.Config{File: dbFile}, log)
	require.NoError(t, stateDB.Init())

	t.Cleanup(func() { _ = stateDB.Close() })

	tracker := NewTracker(cfg, chainSvc, stateDB, planSvc, nil, nil, nil, nil, nil, log)

	return &trackerTestEnv{
		cfg:      cfg,
		chainSvc: chainSvc,
		planSvc:  planSvc,
		stateDB:  stateDB,
		tracker:  tracker,
	}
}

func TestUpsertCreatesRecordWithFrozenPlan(t *testing.T) {
	env := newTrackerTestEnv(t, false)

	env.tracker.RecordBlockSubmission(2000, "epbs", string(SubmissionStatusAccepted), "")

	result := env.tracker.Get(2000)
	require.NotNil(t, result)
	require.Equal(t, phase0.Slot(2000), result.Slot)
	require.Equal(t, uint64(62), result.Epoch)
	require.Equal(t, version.DataVersionGloas.String(), result.Fork)
	require.NotNil(t, result.AppliedPlan, "record creation must freeze the applied plan")
	require.Len(t, result.BlockSubmissions, 1)
	require.True(t, env.planSvc.IsFrozen(2000))

	require.Nil(t, env.tracker.Get(2001))
}

// drainUpdates reads all immediately available update events.
func drainUpdates(ch <-chan *SlotResult) []*SlotResult {
	events := make([]*SlotResult, 0, 4)

	for {
		select {
		case ev := <-ch:
			events = append(events, ev)
		default:
			return events
		}
	}
}

func TestUpdateCoalescingDeliversTrailingState(t *testing.T) {
	env := newTrackerTestEnv(t, false)

	sub := env.tracker.SubscribeUpdates(16)
	defer sub.Unsubscribe()

	// Two rapid updates inside the 1s coalescing window: the first fires
	// immediately (leading edge), the second is suppressed but must still be
	// delivered by the trailing flush — otherwise the UI keeps the stale state.
	env.tracker.RecordBlockSubmission(2000, "epbs", string(SubmissionStatusReceived), "")
	env.tracker.RecordBlockSubmission(2000, "epbs", string(SubmissionStatusAccepted), "")

	leading := drainUpdates(sub.Channel())
	require.Len(t, leading, 1, "only the leading update fires synchronously")
	require.Len(t, leading[0].BlockSubmissions, 1)

	// The trailing flush lands within ~1 coalescing interval.
	var trailing *SlotResult
	require.Eventually(t, func() bool {
		for _, ev := range drainUpdates(sub.Channel()) {
			trailing = ev
		}

		return trailing != nil
	}, 2*time.Second, 20*time.Millisecond, "trailing state must be delivered")

	require.Len(t, trailing.BlockSubmissions, 2,
		"the trailing update must carry the final (latest) state")
	require.Equal(t, SubmissionStatusAccepted, trailing.BlockSubmissions[1].Status)
}

func TestGetReturnsClones(t *testing.T) {
	env := newTrackerTestEnv(t, false)

	env.tracker.RecordBuilderAPIBid(2000, "gloas", nil, 5000, 100, string(BidStatusServed), "")

	first := env.tracker.Get(2000)
	first.Bids[0].TotalValueGwei = 1
	first.Build = &SlotBuild{Status: BuildStatusFailed}

	second := env.tracker.Get(2000)
	require.Equal(t, uint64(5000), second.Bids[0].TotalValueGwei)
	require.Nil(t, second.Build)
}

func TestRecordBuilderAPIBidCapturesArtifact(t *testing.T) {
	env := newTrackerTestEnv(t, true)

	bid := &eth2all.SignedExecutionPayloadBid{Version: version.DataVersionGloas}

	env.tracker.RecordBuilderAPIBid(2000, "gloas", bid, 7000, 500, string(BidStatusServed), "")
	env.tracker.RecordBuilderAPIBid(2000, "gloas", bid, 7500, 500, string(BidStatusServed), "")

	result := env.tracker.Get(2000)
	require.Len(t, result.Bids, 2)
	require.NotNil(t, result.Bids[0].ArtifactIndex)
	require.NotNil(t, result.Bids[1].ArtifactIndex)
	require.Equal(t, 0, *result.Bids[0].ArtifactIndex)
	require.Equal(t, 1, *result.Bids[1].ArtifactIndex, "bid artifact indices must increment")
	require.Equal(t, uint64(500), result.Bids[0].ExecutionPaymentGwei)

	artifact, err := env.tracker.Artifacts().Get(2000, ArtifactKindBid, 1)
	require.NoError(t, err)
	require.NotNil(t, artifact)
	require.Equal(t, int64(version.DataVersionGloas), artifact.Fork)
	require.NotEmpty(t, artifact.Data)

	metas, err := env.tracker.Artifacts().ListBids(2000)
	require.NoError(t, err)
	require.Len(t, metas, 2)
}

func TestRecordBidCaptureDisabled(t *testing.T) {
	env := newTrackerTestEnv(t, true)
	env.cfg.SlotArtifactCaptureEnabled = false

	bid := &eth2all.SignedExecutionPayloadBid{Version: version.DataVersionGloas}
	env.tracker.RecordBuilderAPIBid(2000, "gloas", bid, 7000, 0, string(BidStatusServed), "")

	result := env.tracker.Get(2000)
	require.Len(t, result.Bids, 1)
	require.Nil(t, result.Bids[0].ArtifactIndex, "capture disabled must not store artifacts")

	metas, err := env.tracker.Artifacts().ListBids(2000)
	require.NoError(t, err)
	require.Empty(t, metas)
}

// TestRecordLegacyHeaderBidPayload: a pre-Gloas header bid records the block
// hash it committed to, so the payload it used is known.
func TestRecordLegacyHeaderBidPayload(t *testing.T) {
	env := newTrackerTestEnv(t, false)
	env.cfg.SlotArtifactCaptureEnabled = false

	bid := &legacytypes.SignedBuilderBid{
		Version: version.DataVersionFulu,
		Message: &legacytypes.BuilderBid{
			Version: version.DataVersionFulu,
			Header: &eth2all.ExecutionPayloadHeader{
				Version:      version.DataVersionFulu,
				BlockHash:    phase0.Hash32{0xab},
				ParentHash:   phase0.Hash32{0xaa},
				FeeRecipient: [20]byte{0x01},
				GasLimit:     36_000_000,
			},
		},
	}
	env.tracker.RecordBuilderAPIBid(5, "fulu", bid, 7000, 0, string(BidStatusServed), "")

	result := env.tracker.Get(5)
	require.Len(t, result.Bids, 1)
	require.Equal(t, hashHex(0xab), result.Bids[0].BlockHash)
	require.Equal(t, hashHex(0xaa), result.Bids[0].ParentBlockHash)
	require.Equal(t, uint64(36_000_000), result.Bids[0].GasLimit)
}

func TestAttemptCapAndDroppedCounter(t *testing.T) {
	env := newTrackerTestEnv(t, false)
	env.cfg.SlotArtifactCaptureEnabled = false

	for range maxAttemptsPerKind + 10 {
		env.tracker.RecordBuilderAPIBid(2000, "gloas", nil, 1, 0, string(BidStatusServed), "")
	}

	result := env.tracker.Get(2000)
	require.Len(t, result.Bids, maxAttemptsPerKind)
	require.Equal(t, 10, result.DroppedAttempts["bids"])
}

func TestBaselineMaterialization(t *testing.T) {
	env := newTrackerTestEnv(t, false)

	// Slot with active consumers → waiting baseline, flipped to no_attributes
	// after the slot passes.
	env.tracker.materializeBaseline(1500)

	result := env.tracker.Get(1500)
	require.NotNil(t, result)
	require.Equal(t, BuildStatusWaitingAttributes, result.Build.Status)

	env.tracker.finalizeWaitingBaseline(1500)

	result = env.tracker.Get(1500)
	require.Equal(t, BuildStatusNoAttributes, result.Build.Status)

	// A ready outcome is never regressed by finalize.
	env.tracker.upsert(1501, func(r *SlotResult) {
		r.Build = &SlotBuild{Status: BuildStatusReady}
	})
	env.tracker.finalizeWaitingBaseline(1501)
	require.Equal(t, BuildStatusReady, env.tracker.Get(1501).Build.Status)
}

func TestBaselineSkippedForInactiveSlots(t *testing.T) {
	env := newTrackerTestEnv(t, false)
	env.cfg.EPBSEnabled = false
	env.cfg.BuilderAPIEnabled = false

	env.tracker.materializeBaseline(1600)
	require.Nil(t, env.tracker.Get(1600), "inactive slots get no baseline record")
}

func TestBaselineRecordsScheduleSkips(t *testing.T) {
	env := newTrackerTestEnv(t, false)
	env.cfg.Schedule.Mode = config.ScheduleModeNextN
	env.cfg.Schedule.NextN = 0

	env.tracker.materializeBaseline(1700)

	result := env.tracker.Get(1700)
	require.NotNil(t, result)
	require.Equal(t, BuildStatusSkipped, result.Build.Status)
	require.Equal(t, action_plan.BuildSkipReasonSchedule, result.Build.SkipReason)
}

func TestGetRangeSortedAndBounded(t *testing.T) {
	env := newTrackerTestEnv(t, false)

	for _, slot := range []phase0.Slot{2005, 2001, 2003} {
		env.tracker.RecordBlockSubmission(slot, "legacy", string(SubmissionStatusAccepted), "")
	}

	results := env.tracker.GetRange(2001, 2004)
	require.Len(t, results, 2)
	require.Equal(t, phase0.Slot(2001), results[0].Slot)
	require.Equal(t, phase0.Slot(2003), results[1].Slot)
}

func TestGetWonBlocksViewParity(t *testing.T) {
	env := newTrackerTestEnv(t, false)

	includedAt := time.Now()

	for _, slot := range []phase0.Slot{2001, 2003, 2005} {
		env.tracker.upsert(slot, func(r *SlotResult) {
			r.Inclusion = &InclusionResult{
				Source:          payload_bidder.WonBlockSourceEPBS,
				BlockHash:       "0xabc",
				NumTransactions: 3,
				NumBlobs:        1,
				ValueWei:        "2000000000000",
				ValueETH:        "0.000002000000000000",
				Timestamp:       includedAt,
			}
		})
	}

	// A non-included slot must not appear.
	env.tracker.RecordBlockSubmission(2002, "epbs", string(SubmissionStatusAccepted), "")

	wonBlocks, total := env.tracker.GetWonBlocks(0, 2)
	require.Equal(t, 3, total)
	require.Len(t, wonBlocks, 2)
	require.Equal(t, uint64(2005), wonBlocks[0].Slot, "slot-descending order")
	require.Equal(t, uint64(2003), wonBlocks[1].Slot)
	require.Equal(t, includedAt.UnixMilli(), wonBlocks[0].Timestamp)

	page2, _ := env.tracker.GetWonBlocks(2, 2)
	require.Len(t, page2, 1)
	require.Equal(t, uint64(2001), page2[0].Slot)

	empty, total := env.tracker.GetWonBlocks(10, 2)
	require.Equal(t, 3, total)
	require.Empty(t, empty)
}

func TestPruneForEpochSeparateWindows(t *testing.T) {
	env := newTrackerTestEnv(t, true)
	env.cfg.SlotResultRetentionEpochs = 4
	env.cfg.SlotArtifactRetentionEpochs = 2

	bid := &eth2all.SignedExecutionPayloadBid{Version: version.DataVersionGloas}

	for _, slot := range []phase0.Slot{100, 200, 300} {
		env.tracker.RecordBuilderAPIBid(slot, "gloas", bid, 1000, 0, string(BidStatusServed), "")
	}

	// Flush the artifact writer synchronously for the assertion.
	env.tracker.artifacts.Start(t.Context())
	env.tracker.artifacts.Stop()

	// Epoch 10: results cutoff (10-4)*32 = 192; artifacts cutoff (10-2)*32 = 256.
	env.tracker.pruneForEpoch(10)

	require.Nil(t, env.tracker.Get(100), "result below the result window must be pruned")
	require.NotNil(t, env.tracker.Get(200))
	require.NotNil(t, env.tracker.Get(300))

	gone, err := env.stateDB.GetSlotArtifact(200, ArtifactKindBid, 0)
	require.NoError(t, err)
	require.Nil(t, gone, "artifact below the artifact window must be pruned")

	kept, err := env.stateDB.GetSlotArtifact(300, ArtifactKindBid, 0)
	require.NoError(t, err)
	require.NotNil(t, kept)
}

func TestPersistenceRoundTrip(t *testing.T) {
	env := newTrackerTestEnv(t, true)

	env.tracker.SetPersistence(t.Context(), env.stateDB)
	env.tracker.RecordBlockSubmission(2000, "epbs", string(SubmissionStatusAccepted), "")
	env.tracker.store.Stop()

	// Rehydrate into a fresh tracker.
	log := logrus.New()
	log.SetOutput(io.Discard)

	fresh := NewTracker(env.cfg, env.chainSvc, env.stateDB, env.planSvc, nil, nil, nil, nil, nil, log)
	fresh.SetPersistence(t.Context(), env.stateDB)

	defer fresh.store.Stop()

	result := fresh.Get(2000)
	require.NotNil(t, result)
	require.Len(t, result.BlockSubmissions, 1)
}

func TestMigrateWonBlocks(t *testing.T) {
	log := logrus.New()
	log.SetOutput(io.Discard)

	env := newTrackerTestEnv(t, true)

	// Seed the legacy namespace: one standalone win, one win for a slot that
	// already has a result WITH inclusion (must not be overwritten), one for
	// a slot with a result WITHOUT inclusion (must merge).
	oldPersistence := db.NewKVPersistence(env.stateDB, payload_bidder.WonBlocksNamespace,
		payload_bidder.WonBlockCodec{})
	require.NoError(t, oldPersistence.PersistBatch(map[phase0.Slot]*payload_bidder.WonBlock{
		100: {Source: "epbs", Slot: 100, BlockHash: "0x01", ValueWei: "1", ValueETH: "0.1", Timestamp: 1000},
		200: {Source: "epbs", Slot: 200, BlockHash: "0x02", ValueWei: "2", ValueETH: "0.2", Timestamp: 2000},
		300: {Source: "builder_api", Slot: 300, BlockHash: "0x03", ValueWei: "3", ValueETH: "0.3", Timestamp: 3000},
	}, nil))

	resultsPersistence := db.NewKVPersistence(env.stateDB, Namespace, ResultCodec{})
	require.NoError(t, resultsPersistence.PersistBatch(map[phase0.Slot]*SlotResult{
		200: {Slot: 200, Epoch: 6, Inclusion: &InclusionResult{BlockHash: "0xexisting"}},
		300: {Slot: 300, Epoch: 9, Build: &SlotBuild{Status: BuildStatusReady}},
	}, nil))

	migrateWonBlocks(env.stateDB, 32, log)

	migrated, err := resultsPersistence.Load()
	require.NoError(t, err)

	require.NotNil(t, migrated[100])
	require.Equal(t, "0x01", migrated[100].Inclusion.BlockHash)
	require.Equal(t, uint64(3), migrated[100].Epoch)
	require.Equal(t, int64(1000), migrated[100].Inclusion.Timestamp.UnixMilli())

	require.Equal(t, "0xexisting", migrated[200].Inclusion.BlockHash,
		"existing inclusion must not be overwritten")

	require.Equal(t, "0x03", migrated[300].Inclusion.BlockHash, "inclusion merged into existing result")
	require.NotNil(t, migrated[300].Build, "existing result fields preserved")

	// Old namespace deleted.
	oldEntries, err := oldPersistence.Load()
	require.NoError(t, err)
	require.Empty(t, oldEntries)

	// Idempotent re-run.
	migrateWonBlocks(env.stateDB, 32, log)

	again, err := resultsPersistence.Load()
	require.NoError(t, err)
	require.Len(t, again, 3)
}

func TestArtifactStoreRestartSafeIndices(t *testing.T) {
	log := logrus.New()
	log.SetOutput(io.Discard)

	env := newTrackerTestEnv(t, true)

	store := NewArtifactStore(env.stateDB, log)
	bid := &eth2all.SignedExecutionPayloadBid{Version: version.DataVersionGloas}

	idx0, err := store.StoreBid(500, version.DataVersionGloas, bid, BidArtifactMeta{Transport: "p2p"})
	require.NoError(t, err)
	require.Equal(t, 0, idx0)

	// Flush synchronously.
	store.Start(t.Context())
	store.Stop()

	// A fresh store (restart) must continue after the persisted MAX(idx).
	fresh := NewArtifactStore(env.stateDB, log)

	idx1, err := fresh.StoreBid(500, version.DataVersionGloas, bid, BidArtifactMeta{Transport: "p2p"})
	require.NoError(t, err)
	require.Equal(t, 1, idx1, "index allocation must survive restarts")
}

func TestArtifactStoreBufferWithoutDB(t *testing.T) {
	log := logrus.New()
	log.SetOutput(io.Discard)

	env := newTrackerTestEnv(t, false)
	require.False(t, env.stateDB.Enabled())

	store := NewArtifactStore(env.stateDB, log)
	payload := &eth2all.ExecutionPayload{
		Version:  version.DataVersionFulu,
		GasLimit: 1, GasUsed: 1, Timestamp: 1, BlockNumber: 1,
	}

	err := store.StorePayload(700, version.DataVersionFulu, payload, PayloadArtifactMeta{})
	require.NoError(t, err)

	artifact, err := store.Get(700, ArtifactKindPayload, 0)
	require.NoError(t, err)
	require.NotNil(t, artifact)
	require.NotEmpty(t, artifact.Data)

	// Buffer bound: newest 64 distinct slots.
	for slot := phase0.Slot(701); slot <= 800; slot++ {
		slotErr := store.StorePayload(slot, version.DataVersionFulu, payload, PayloadArtifactMeta{})
		require.NoError(t, slotErr)
	}

	evicted, err := store.Get(700, ArtifactKindPayload, 0)
	require.NoError(t, err)
	require.Nil(t, evicted, "old slots must be evicted from the memory buffer")

	newest, err := store.Get(800, ArtifactKindPayload, 0)
	require.NoError(t, err)
	require.NotNil(t, newest)
}

func TestArtifactSSZRoundTrip(t *testing.T) {
	log := logrus.New()
	log.SetOutput(io.Discard)

	env := newTrackerTestEnv(t, true)
	store := NewArtifactStore(env.stateDB, log)

	original := &eth2all.ExecutionPayload{
		Version:     version.DataVersionFulu,
		BlockNumber: 42,
		GasLimit:    30_000_000,
		GasUsed:     21_000,
		Timestamp:   1234,
	}

	err := store.StorePayload(900, version.DataVersionFulu, original, PayloadArtifactMeta{})
	require.NoError(t, err)

	artifact, err := store.Get(900, ArtifactKindPayload, 0)
	require.NoError(t, err)
	require.NotNil(t, artifact)

	decoded := &eth2all.ExecutionPayload{Version: version.DataVersion(artifact.Fork)}
	require.NoError(t, decoded.UnmarshalSSZ(artifact.Data))
	require.Equal(t, uint64(42), decoded.BlockNumber)
	require.Equal(t, uint64(30_000_000), decoded.GasLimit)
}

func TestUpdateSubscriptionCoalescing(t *testing.T) {
	env := newTrackerTestEnv(t, false)
	env.cfg.SlotArtifactCaptureEnabled = false

	sub := env.tracker.SubscribeUpdates(64)
	defer sub.Unsubscribe()

	// First update fires immediately; a burst within the interval coalesces.
	for range 5 {
		env.tracker.RecordBuilderAPIBid(2000, "gloas", nil, 1, 0, string(BidStatusServed), "")
	}

	received := 0

	timeout := time.After(200 * time.Millisecond)
drain:
	for {
		select {
		case <-sub.Channel():
			received++
		case <-timeout:
			break drain
		}
	}

	require.Equal(t, 1, received, "burst updates must coalesce to one event")

	// The record itself kept every attempt.
	require.Len(t, env.tracker.Get(2000).Bids, 5)
}

// buildEvents builds a target's events the way the builder emits them.
type buildEvents struct {
	slot phase0.Slot
	seq  uint64
	at   time.Time
}

func (b buildEvents) payload(ref payload_builder.BuildRef, hash byte, value int64) *payload_builder.Payload {
	return &payload_builder.Payload{
		Attributes: &beacon.PayloadAttributesEvent{
			ProposalSlot:    b.slot,
			ParentBlockRoot: phase0.Root{0x01},
			ParentBlockHash: phase0.Hash32{0xaa},
		},
		ExecutionPayload: &eth2all.ExecutionPayload{
			Version: version.DataVersionFulu, BlockHash: phase0.Hash32{hash}, GasLimit: 1, Timestamp: 1,
		},
		Build:      ref,
		Candidate:  chain.CandidateParentFull,
		BlockHash:  phase0.Hash32{hash},
		BlockValue: big.NewInt(value),
		ReadyAt:    b.at,
		BuildSeq:   b.seq,
	}
}

func (b buildEvents) target(status string) *payload_builder.BuildTargetEvent {
	return &payload_builder.BuildTargetEvent{
		Slot: b.slot, Candidate: chain.CandidateParentFull, Seq: b.seq, Status: status, At: b.at,
		Attributes: &beacon.PayloadAttributesEvent{ProposalSlot: b.slot, ParentBlockHash: phase0.Hash32{0xaa}},
		Engine:     true, Testing: true,
	}
}

func (b buildEvents) selected(payload *payload_builder.Payload, reason string) *payload_builder.PayloadSelectedEvent {
	return &payload_builder.PayloadSelectedEvent{
		Slot: b.slot, Candidate: chain.CandidateParentFull, Seq: b.seq, Build: payload.Build,
		BlockHash: payload.BlockHash, Reason: reason, Payload: payload, At: b.at,
	}
}

func hashHex(hash byte) string {
	return fmt.Sprintf("%#x", phase0.Hash32{hash})
}

// TestBuildTargetKeepsEveryBuild: the engine build, every testing run and
// every selection are recorded apart; an upgrade only adds a selection and
// never touches the engine build or its payload.
func TestBuildTargetKeepsEveryBuild(t *testing.T) {
	env := newTrackerTestEnv(t, false)
	ev := buildEvents{slot: 1600, seq: 4, at: time.Now()}

	engineRef := payload_builder.BuildRef{Kind: payload_builder.BuildKindEngine}
	engine := ev.payload(engineRef, 0xe1, 10)
	run1 := ev.payload(payload_builder.BuildRef{Kind: payload_builder.BuildKindTesting, Run: 1}, 0x71, 5)
	run2 := ev.payload(payload_builder.BuildRef{Kind: payload_builder.BuildKindTesting, Run: 2}, 0x72, 20)

	for _, event := range []payload_builder.BuildEvent{
		ev.target(payload_builder.TargetStatusStarted),
		&payload_builder.EngineBuildEvent{Slot: ev.slot, Seq: ev.seq, Status: payload_builder.EngineStatusStarted, StartedAt: ev.at},
		&payload_builder.TestingBuildEvent{Slot: ev.slot, Seq: ev.seq, Run: 1, Status: payload_builder.LocalStatusBuilding},
		&payload_builder.TestingBuildEvent{Slot: ev.slot, Seq: ev.seq, Run: 1, Status: payload_builder.LocalStatusReady,
			Payload: run1, Info: &payload_builder.LocalBuildInfo{Run: 1, ExpectedHashes: []string{"0x01"}}},
		&payload_builder.EngineBuildEvent{Slot: ev.slot, Seq: ev.seq, Status: payload_builder.EngineStatusReady,
			Payload: engine, Calls: &payload_builder.EngineCallTimes{ForkchoiceStart: ev.at}},
		ev.selected(engine, payload_builder.SelectionFirst),
		&payload_builder.TestingBuildEvent{Slot: ev.slot, Seq: ev.seq, Run: 2, Status: payload_builder.LocalStatusReady, Payload: run2},
		ev.selected(run2, payload_builder.SelectionBetter),
	} {
		env.tracker.handleBuildEvent(event)
	}

	result := env.tracker.Get(ev.slot)
	require.Equal(t, BuildStatusReady, result.Build.Status)
	require.Len(t, result.Build.Targets, 1)

	target := result.Build.Targets[0]
	require.Equal(t, TargetSelected, target.Status)
	require.Equal(t, hashHex(0xe1), target.Engine.PayloadHash, "the engine build keeps its own payload")
	require.Equal(t, payload_builder.EngineStatusReady, target.Engine.Status)
	require.NotNil(t, target.Engine.Calls)
	require.Len(t, target.Testing, 2)
	require.Equal(t, hashHex(0x71), target.Testing[0].PayloadHash)
	require.Equal(t, hashHex(0x72), target.Testing[1].PayloadHash)

	require.Len(t, target.Selections, 2)
	require.Equal(t, payload_builder.SelectionFirst, target.Selections[0].Reason)
	require.Equal(t, hashHex(0xe1), target.Selections[0].PayloadHash)
	require.Equal(t, payload_builder.SelectionBetter, target.Selections[1].Reason)
	require.Equal(t, hashHex(0x72), target.Selections[1].PayloadHash)

	require.Len(t, result.Payloads, 3, "every payload once, by hash")
	require.Equal(t, engineRef, result.Payloads[hashHex(0xe1)].Build)
	require.Equal(t, "10", result.Payloads[hashHex(0xe1)].BlockValueWei)
	require.Equal(t, 2, result.Payloads[hashHex(0x72)].Build.Run)
	require.Same(t, result.Payloads[hashHex(0x72)], result.SelectedPayload())

	// Captured: the engine payload and the selected run, not the run nobody
	// selected.
	require.True(t, result.Payloads[hashHex(0xe1)].Captured)
	require.True(t, result.Payloads[hashHex(0x72)].Captured)
	require.False(t, result.Payloads[hashHex(0x71)].Captured)

	for hash, want := range map[byte]bool{0xe1: true, 0x72: true, 0x71: false} {
		artifact, err := env.tracker.Artifacts().PayloadByHash(ev.slot, hashHex(hash))
		require.NoError(t, err)
		require.Equal(t, want, artifact != nil, "artifact of %x", hash)
	}

	engineArtifact, err := env.tracker.Artifacts().PayloadByHash(ev.slot, hashHex(0xe1))
	require.NoError(t, err)
	require.False(t, IsCustomPayloadArtifact(engineArtifact))

	runArtifact, err := env.tracker.Artifacts().PayloadByHash(ev.slot, hashHex(0x72))
	require.NoError(t, err)
	require.True(t, IsCustomPayloadArtifact(runArtifact))

	// The summary keeps the targets' builds and selections, the selected
	// payloads, and drops the details.
	summary := result.Summary()
	require.Len(t, summary.Build.Targets, 1)
	require.Len(t, summary.Build.Targets[0].Selections, 2)
	require.Nil(t, summary.Build.Targets[0].Testing[0].Info)
	require.Len(t, summary.Payloads, 1)
	require.NotNil(t, summary.Payloads[hashHex(0x72)])

	// Readers without a login get no transaction hash of a testing run.
	redacted := result.Redacted()
	require.Nil(t, redacted.Build.Targets[0].Testing[0].Info.ExpectedHashes)
	require.NotNil(t, result.Build.Targets[0].Testing[0].Info.ExpectedHashes, "the record itself is untouched")
}

// TestBuildTargetSupersededAndRebuilt: a superseded target stays recorded as
// such, its replacement becomes the primary one, and late events of the
// superseded target change nothing.
func TestBuildTargetSupersededAndRebuilt(t *testing.T) {
	env := newTrackerTestEnv(t, false)
	stale := buildEvents{slot: 1601, seq: 1, at: time.Now()}
	fresh := buildEvents{slot: 1601, seq: 2, at: time.Now()}

	engineRef := payload_builder.BuildRef{Kind: payload_builder.BuildKindEngine}
	stalePayload := stale.payload(engineRef, 0x11, 1)

	env.tracker.handleBuildEvent(stale.target(payload_builder.TargetStatusStarted))
	env.tracker.handleBuildEvent(&payload_builder.EngineBuildEvent{Slot: stale.slot, Seq: stale.seq,
		Status: payload_builder.EngineStatusReady, Payload: stalePayload})
	env.tracker.handleBuildEvent(stale.selected(stalePayload, payload_builder.SelectionFirst))
	require.Equal(t, BuildStatusReady, env.tracker.Get(stale.slot).Build.Status)

	superseded := stale.target(payload_builder.TargetStatusSuperseded)
	superseded.Error = "superseded by beacon node attributes"
	env.tracker.handleBuildEvent(superseded)
	require.Equal(t, BuildStatusFailed, env.tracker.Get(stale.slot).Build.Status, "a withdrawn selection is no payload")

	env.tracker.handleBuildEvent(fresh.target(payload_builder.TargetStatusStarted))
	require.Equal(t, BuildStatusBuilding, env.tracker.Get(fresh.slot).Build.Status)

	freshPayload := fresh.payload(engineRef, 0x22, 2)
	env.tracker.handleBuildEvent(&payload_builder.EngineBuildEvent{Slot: fresh.slot, Seq: fresh.seq,
		Status: payload_builder.EngineStatusReady, Payload: freshPayload})
	env.tracker.handleBuildEvent(fresh.selected(freshPayload, payload_builder.SelectionFirst))

	// A late pick of the superseded target is dropped.
	env.tracker.handleBuildEvent(stale.selected(stalePayload, payload_builder.SelectionBetter))

	result := env.tracker.Get(fresh.slot)
	require.Len(t, result.Build.Targets, 2)
	require.Equal(t, TargetSuperseded, result.Build.Targets[0].Status)
	require.Len(t, result.Build.Targets[0].Selections, 1)
	require.Equal(t, BuildStatusReady, result.Build.Status)
	require.Equal(t, hashHex(0x22), result.SelectedPayload().BlockHash)

	// A target that ends without a selection fails the slot only when it is
	// the primary one.
	other := buildEvents{slot: 1601, seq: 3, at: time.Now()}
	started := other.target(payload_builder.TargetStatusStarted)
	started.Candidate = chain.CandidateParentEmpty
	env.tracker.handleBuildEvent(started)

	noPayload := other.target(payload_builder.TargetStatusNoPayload)
	noPayload.Candidate = chain.CandidateParentEmpty
	noPayload.Error = "boom"
	env.tracker.handleBuildEvent(noPayload)

	result = env.tracker.Get(fresh.slot)
	require.Equal(t, BuildStatusReady, result.Build.Status)
	require.Equal(t, TargetNoPayload, result.Build.Targets[2].Status)
}

// TestLegacyBuildRecordsDecode maps records of the old build model onto
// targets: the payload that fed the bids becomes the target's selection.
func TestLegacyBuildRecordsDecode(t *testing.T) {
	raw := []byte(`{"slot":"7","build":{"status":"ready","candidate":"parent_full","block_hash":"0x02",` +
		`"block_value_wei":"9","source":"local","artifact_idx":1,"at":"2026-01-01T00:00:00Z",` +
		`"local_build":{"status":"ready","selected":true,"block_hash":"0x02","tx_source":"txpool",` +
		`"info":{"tx_source":"txpool","submitted_txs":3,"run":2,"started_at":"2026-01-01T00:00:00Z","built_at":"2026-01-01T00:00:00Z"},` +
		`"at":"2026-01-01T00:00:00Z"}},` +
		`"builds":[{"status":"ready","candidate":"parent_full","block_hash":"0x02","block_value_wei":"9",` +
		`"source":"local","artifact_idx":1,"at":"2026-01-01T00:00:00Z",` +
		`"local_build":{"status":"ready","selected":true,"block_hash":"0x02","tx_source":"txpool",` +
		`"info":{"tx_source":"txpool","submitted_txs":3,"run":2,"started_at":"2026-01-01T00:00:00Z","built_at":"2026-01-01T00:00:00Z"},` +
		`"at":"2026-01-01T00:00:00Z"}},` +
		`{"status":"ready","candidate":"parent_empty","block_hash":"0x03","source":"el","at":"2026-01-01T00:00:00Z"}]}`)

	result, err := ResultCodec{}.DecodeValue(raw)
	require.NoError(t, err)
	require.Equal(t, BuildStatusReady, result.Build.Status)
	require.Len(t, result.Build.Targets, 2)

	local := result.Build.Targets[0]
	require.Equal(t, "parent_full", local.Candidate)
	require.Nil(t, local.Engine, "the old record never captured the engine payload")
	require.Len(t, local.Testing, 1)
	require.Equal(t, 2, local.Testing[0].Run)
	require.Len(t, local.Selections, 1)
	require.Equal(t, payload_builder.BuildRef{Kind: payload_builder.BuildKindTesting, Run: 2}, local.Selections[0].Build)
	require.Equal(t, "9", result.Payloads["0x02"].BlockValueWei)
	require.True(t, result.Payloads["0x02"].Captured)

	engine := result.Build.Targets[1]
	require.Equal(t, "0x03", engine.Engine.PayloadHash)
	require.Equal(t, payload_builder.BuildKindEngine, result.Payloads["0x03"].Build.Kind)

	require.Equal(t, "0x02", result.SelectedPayload().BlockHash, "parent_full stays the primary target")

	// A record of the build model round-trips unchanged.
	encoded, err := ResultCodec{}.EncodeValue(result)
	require.NoError(t, err)

	again, err := ResultCodec{}.DecodeValue(encoded)
	require.NoError(t, err)
	require.Len(t, again.Build.Targets, 2)
	require.Len(t, again.Payloads, 2)
}

// TestTxPlanVerdictBeforeSeed keeps a tx plan verdict recorded before the
// tracker processed the inclusion event that seeds the plan: the verifier
// consumes the same event independently and can finish first.
func TestTxPlanVerdictBeforeSeed(t *testing.T) {
	env := newTrackerTestEnv(t, false)

	env.tracker.RecordTxPlanCheck(2000, TxPlanMatch, 2, -1, "")

	env.tracker.handleIncluded(&payload_bidder.PayloadIncludedEvent{
		Payload: &payload_builder.Payload{
			Local: &payload_builder.LocalBuildInfo{
				TxSource:       "explicit",
				ExpectedHashes: []string{"0x01", "0x02"},
			},
		},
		WonBlock: &payload_bidder.WonBlock{Source: "epbs", Slot: 2000, BlockHash: "0xbb"},
	})

	plan := env.tracker.Get(2000).TxPlan
	require.NotNil(t, plan)
	require.Equal(t, TxPlanMatch, plan.Status, "the early verdict survives the seed")
	require.Equal(t, 2, plan.IncludedCount)
	require.Equal(t, 2, plan.ExpectedCount)
	require.Equal(t, "explicit", plan.TxSource)
	require.NotNil(t, plan.VerifiedAt)

	// A later inclusion (re-included after a reorg) seeds a fresh pending plan.
	env.tracker.handleIncluded(&payload_bidder.PayloadIncludedEvent{
		Payload: &payload_builder.Payload{
			Local: &payload_builder.LocalBuildInfo{
				TxSource:       "explicit",
				ExpectedHashes: []string{"0x01", "0x02"},
			},
		},
		WonBlock: &payload_bidder.WonBlock{Source: "epbs", Slot: 2000, BlockHash: "0xcc"},
	})

	require.Equal(t, TxPlanPending, env.tracker.Get(2000).TxPlan.Status)
}
