package slot_results

import (
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
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
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

	tracker := NewTracker(cfg, chainSvc, stateDB, planSvc, nil, nil, nil, nil, log)

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

func TestGetReturnsClones(t *testing.T) {
	env := newTrackerTestEnv(t, false)

	env.tracker.RecordBuilderAPIBid(2000, "gloas", nil, 5000, 100, string(BidStatusServed), "")

	first := env.tracker.Get(2000)
	first.Bids[0].TotalValueGwei = 1
	first.Build = &BuildOutcome{Status: BuildStatusFailed}

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
		r.Build = &BuildOutcome{Status: BuildStatusReady}
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

	fresh := NewTracker(env.cfg, env.chainSvc, env.stateDB, env.planSvc, nil, nil, nil, nil, log)
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
		300: {Slot: 300, Epoch: 9, Build: &BuildOutcome{Status: BuildStatusReady}},
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

	require.NoError(t, store.StorePayload(700, version.DataVersionFulu, payload))

	artifact, err := store.Get(700, ArtifactKindPayload, 0)
	require.NoError(t, err)
	require.NotNil(t, artifact)
	require.NotEmpty(t, artifact.Data)

	// Buffer bound: newest 64 distinct slots.
	for slot := phase0.Slot(701); slot <= 800; slot++ {
		require.NoError(t, store.StorePayload(slot, version.DataVersionFulu, payload))
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

	require.NoError(t, store.StorePayload(900, version.DataVersionFulu, original))

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

func TestBuildValueFromBigInt(t *testing.T) {
	// Guard the wei string formatting used by handlePayloadReady.
	value := big.NewInt(1_500_000_000)
	require.Equal(t, "1500000000", value.String())
}
