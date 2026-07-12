package action_plan

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// stubChain provides the minimal chain.Service surface the plan service uses.
type stubChain struct {
	chain.Service

	spec        *chain.ChainSpec
	currentSlot phase0.Slot
	fork        version.DataVersion
	epochDisp   utils.Dispatcher[*chain.EpochStats]
}

func newStubChain() *stubChain {
	return &stubChain{
		spec: &chain.ChainSpec{
			SecondsPerSlot: 12 * time.Second,
			SlotsPerEpoch:  32,
		},
		currentSlot: 1000,
		fork:        version.DataVersionGloas,
	}
}

func (s *stubChain) GetChainSpec() *chain.ChainSpec { return s.spec }
func (s *stubChain) GetCurrentSlot() phase0.Slot    { return s.currentSlot }
func (s *stubChain) GetEpochOfSlot(slot phase0.Slot) phase0.Epoch {
	return phase0.Epoch(uint64(slot) / s.spec.SlotsPerEpoch)
}
func (s *stubChain) ActiveForkAtEpoch(_ phase0.Epoch) version.DataVersion { return s.fork }
func (s *stubChain) SubscribeEpochStats() *utils.Subscription[*chain.EpochStats] {
	return s.epochDisp.Subscribe(4, false)
}

func newTestService(chainSvc *stubChain, cfg *config.Config) *PlanService {
	if cfg == nil {
		cfg = config.DefaultConfig()
		cfg.EPBSEnabled = true
		cfg.BuilderAPIEnabled = true
		cfg.APIPort = 8080
	}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	return NewPlanService(cfg, chainSvc, log)
}

func TestApplyUpdatesAndGet(t *testing.T) {
	chainSvc := newStubChain()
	svc := newTestService(chainSvc, nil)

	event, err := svc.ApplyUpdates([]*PlanUpdate{{
		Slots: []uint64{2000, 2001},
		Bid:   json.RawMessage(`{"mode":"custom","bid_min_amount":777}`),
	}}, "tester")
	require.NoError(t, err)
	require.Equal(t, []uint64{2000, 2001}, event.Slots)
	require.NotNil(t, event.Plans[0])
	require.Equal(t, "tester", event.Plans[0].UpdatedBy)

	plan := svc.Get(2000)
	require.NotNil(t, plan)
	require.Equal(t, uint64(777), *plan.Bid.BidMinAmount)

	// Mutating the returned clone must not affect the store.
	*plan.Bid.BidMinAmount = 1

	stored := svc.Get(2000)
	require.Equal(t, uint64(777), *stored.Bid.BidMinAmount)

	require.Nil(t, svc.Get(1999))
}

func TestApplyUpdatesOverlappingInOrder(t *testing.T) {
	chainSvc := newStubChain()
	svc := newTestService(chainSvc, nil)

	_, err := svc.ApplyUpdates([]*PlanUpdate{
		{
			Slots: []uint64{3000},
			Bid:   json.RawMessage(`{"mode":"custom","bid_min_amount":1}`),
		},
		{
			Slots:  []uint64{3000},
			Reveal: json.RawMessage(`{"mode":"disabled"}`),
		},
	}, "tester")
	require.NoError(t, err)

	plan := svc.Get(3000)
	require.NotNil(t, plan.Bid, "first update must survive the second (merge, not replace)")
	require.NotNil(t, plan.Reveal)
}

func TestApplyUpdatesAllOrNothing(t *testing.T) {
	chainSvc := newStubChain()
	svc := newTestService(chainSvc, nil)

	_, err := svc.ApplyUpdates([]*PlanUpdate{
		{
			Slots: []uint64{4000},
			Bid:   json.RawMessage(`{"mode":"custom"}`),
		},
		{
			Slots: []uint64{4001},
			Bid:   json.RawMessage(`{"mode":"custom","bid_interval":-5}`), // invalid
		},
	}, "tester")
	require.Error(t, err)

	require.Nil(t, svc.Get(4000), "valid update must be rolled back when a later one fails")
	require.Nil(t, svc.Get(4001))
}

func TestApplyUpdatesRejectsPastAndFrozenSlots(t *testing.T) {
	chainSvc := newStubChain()
	svc := newTestService(chainSvc, nil)

	_, err := svc.ApplyUpdates([]*PlanUpdate{{
		Slots: []uint64{uint64(chainSvc.currentSlot)},
		Bid:   json.RawMessage(`{"mode":"disabled"}`),
	}}, "tester")
	require.ErrorIs(t, err, ErrSlotLocked)

	// Freeze a future slot, then try to edit it.
	frozen := svc.Freeze(1500)
	require.NotNil(t, frozen)

	_, err = svc.ApplyUpdates([]*PlanUpdate{{
		Slots: []uint64{1500},
		Bid:   json.RawMessage(`{"mode":"disabled"}`),
	}}, "tester")
	require.ErrorIs(t, err, ErrSlotLocked)
}

func TestApplyUpdatesDelete(t *testing.T) {
	chainSvc := newStubChain()
	svc := newTestService(chainSvc, nil)

	_, err := svc.ApplyUpdates([]*PlanUpdate{{
		Slots: []uint64{5000},
		Bid:   json.RawMessage(`{"mode":"disabled"}`),
	}}, "tester")
	require.NoError(t, err)
	require.NotNil(t, svc.Get(5000))

	event, err := svc.ApplyUpdates([]*PlanUpdate{{
		Slots:  []uint64{5000},
		Delete: true,
	}}, "tester")
	require.NoError(t, err)
	require.Nil(t, event.Plans[0], "deleted slots report a nil plan")
	require.Nil(t, svc.Get(5000))
}

func TestFreezeIsIdempotentAndConcurrencySafe(t *testing.T) {
	chainSvc := newStubChain()
	svc := newTestService(chainSvc, nil)

	_, err := svc.ApplyUpdates([]*PlanUpdate{{
		Slots: []uint64{6000},
		Bid:   json.RawMessage(`{"mode":"custom","bid_value_gwei":123}`),
	}}, "tester")
	require.NoError(t, err)

	results := make([]*FrozenPlan, 16)

	var wg sync.WaitGroup

	for i := range results {
		wg.Go(func() {
			results[i] = svc.Freeze(6000)
		})
	}

	wg.Wait()

	for _, frozen := range results {
		require.Same(t, results[0], frozen, "every caller must observe the identical snapshot")
	}

	require.True(t, svc.IsFrozen(6000))
	require.False(t, svc.IsFrozen(6001))
	require.NotNil(t, results[0].Bid)
	require.Equal(t, uint64(123), *results[0].Bid.ValueGwei)
}

func TestFreezeResolutionTruthTable(t *testing.T) {
	customBid := json.RawMessage(`{"mode":"custom"}`)
	disabledBid := json.RawMessage(`{"mode":"disabled"}`)

	tests := []struct {
		name        string
		epbsEnabled bool
		apiEnabled  bool
		apiPort     int
		fork        version.DataVersion
		bidPatch    json.RawMessage
		apiPatch    json.RawMessage
		wantBid     bool
		wantForced  bool
		wantAPI     bool
	}{
		{
			name:        "baseline enabled, no plan",
			epbsEnabled: true, apiEnabled: true, apiPort: 1,
			fork:    version.DataVersionGloas,
			wantBid: true, wantAPI: true,
		},
		{
			name:        "baseline disabled, no plan",
			epbsEnabled: false, apiEnabled: false, apiPort: 1,
			fork:    version.DataVersionGloas,
			wantBid: false, wantAPI: false,
		},
		{
			name:        "plan activates globally disabled modules",
			epbsEnabled: false, apiEnabled: false, apiPort: 1,
			fork:     version.DataVersionGloas,
			bidPatch: customBid, apiPatch: json.RawMessage(`{"mode":"custom"}`),
			wantBid: true, wantForced: true, wantAPI: true,
		},
		{
			name:        "plan suppresses globally enabled modules",
			epbsEnabled: true, apiEnabled: true, apiPort: 1,
			fork:     version.DataVersionGloas,
			bidPatch: disabledBid, apiPatch: json.RawMessage(`{"mode":"disabled"}`),
			wantBid: false, wantAPI: false,
		},
		{
			name:        "no api server: plan cannot activate builder api",
			epbsEnabled: true, apiEnabled: true, apiPort: 0,
			fork:     version.DataVersionGloas,
			apiPatch: json.RawMessage(`{"mode":"custom"}`),
			wantBid:  true, wantAPI: false,
		},
		{
			name:        "pre-gloas: plan cannot activate p2p bidding",
			epbsEnabled: true, apiEnabled: true, apiPort: 1,
			fork:     version.DataVersionElectra,
			bidPatch: customBid,
			wantBid:  false, wantAPI: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chainSvc := newStubChain()
			chainSvc.fork = tt.fork

			cfg := config.DefaultConfig()
			cfg.EPBSEnabled = tt.epbsEnabled
			cfg.BuilderAPIEnabled = tt.apiEnabled
			cfg.APIPort = tt.apiPort

			svc := newTestService(chainSvc, cfg)

			if tt.bidPatch != nil || tt.apiPatch != nil {
				_, err := svc.ApplyUpdates([]*PlanUpdate{{
					Slots:      []uint64{7000},
					Bid:        tt.bidPatch,
					BuilderAPI: tt.apiPatch,
				}}, "tester")
				require.NoError(t, err)
			}

			frozen := svc.Freeze(7000)

			require.Equal(t, tt.wantBid, frozen.Bid != nil, "bid active")
			if frozen.Bid != nil {
				require.Equal(t, tt.wantForced, frozen.Bid.Forced, "bid forced")
			}

			require.Equal(t, tt.wantAPI, frozen.BuilderAPI != nil, "builder api active")
			require.NotNil(t, frozen.Reveal, "reveal settings always resolve")
		})
	}
}

func TestFreezeSnapshotsGlobalsAtFreezeTime(t *testing.T) {
	chainSvc := newStubChain()

	cfg := config.DefaultConfig()
	cfg.EPBSEnabled = true
	cfg.APIPort = 1
	cfg.EPBS.BidSubsidy = 111

	svc := newTestService(chainSvc, cfg)

	frozen := svc.Freeze(8000)
	require.Equal(t, uint64(111), frozen.Bid.SubsidyGwei)

	// A later global change must not rewrite the frozen snapshot.
	cfg.EPBS.BidSubsidy = 222
	require.Equal(t, uint64(111), svc.Freeze(8000).Bid.SubsidyGwei)
}

func TestFreezeRevealSuppressionAndCustomTiming(t *testing.T) {
	chainSvc := newStubChain()
	svc := newTestService(chainSvc, nil)

	_, err := svc.ApplyUpdates([]*PlanUpdate{
		{Slots: []uint64{9000}, Reveal: json.RawMessage(`{"mode":"disabled"}`)},
		{Slots: []uint64{9001}, Reveal: json.RawMessage(`{"mode":"custom","reveal_time_ms":15000}`)},
	}, "tester")
	require.NoError(t, err)

	suppressed := svc.Freeze(9000)
	require.True(t, suppressed.Reveal.Suppressed)

	late := svc.Freeze(9001)
	require.False(t, late.Reveal.Suppressed)
	require.True(t, late.Reveal.BypassDeadline)
	require.Equal(t, int64(15000), late.Reveal.RevealTimeMs)
}

func TestFreezeReorgParentPayload(t *testing.T) {
	chainSvc := newStubChain()
	svc := newTestService(chainSvc, nil)

	_, err := svc.ApplyUpdates([]*PlanUpdate{
		{Slots: []uint64{9100}, Build: json.RawMessage(`{"reorg_parent_payload":true}`)},
		{Slots: []uint64{9101}, Bid: json.RawMessage(`{"mode":"custom"}`)},
	}, "tester")
	require.NoError(t, err)

	// The build category alone modifies HOW a build happens; it never forces a
	// build (no active consumer for slot 9100), but the flag is still resolved.
	reorg := svc.Freeze(9100)
	require.True(t, reorg.Build.ReorgParentPayload)

	// A slot without a build plan resolves the flag to false.
	plain := svc.Freeze(9101)
	require.False(t, plain.Build.ReorgParentPayload)
}

func TestPruneForEpochKeepsFuturePlans(t *testing.T) {
	chainSvc := newStubChain()

	cfg := config.DefaultConfig()
	cfg.EPBSEnabled = true
	cfg.SlotResultRetentionEpochs = 2

	svc := newTestService(chainSvc, cfg)

	// Store a past plan directly (past slots cannot go through ApplyUpdates).
	svc.store.Put(10, &SlotPlan{Slot: 10, Bid: &BidPlan{Mode: ModeDisabled}})

	_, err := svc.ApplyUpdates([]*PlanUpdate{{
		Slots: []uint64{20000},
		Bid:   json.RawMessage(`{"mode":"disabled"}`),
	}}, "tester")
	require.NoError(t, err)

	// Current epoch 10, retention 2 → cutoff slot (10-2)*32 = 256.
	svc.pruneForEpoch(10)

	require.Nil(t, svc.Get(10), "past plan outside the window must be pruned")
	require.NotNil(t, svc.Get(20000), "future plans are never pruned")
}

func TestGetRangeIsSortedAndBounded(t *testing.T) {
	chainSvc := newStubChain()
	svc := newTestService(chainSvc, nil)

	_, err := svc.ApplyUpdates([]*PlanUpdate{{
		Slots: []uint64{2005, 2001, 2003},
		Bid:   json.RawMessage(`{"mode":"disabled"}`),
	}}, "tester")
	require.NoError(t, err)

	plans := svc.GetRange(2001, 2003)
	require.Len(t, plans, 2)
	require.Equal(t, phase0.Slot(2001), plans[0].Slot)
	require.Equal(t, phase0.Slot(2003), plans[1].Slot)
}

func TestFreezeBuildDecision(t *testing.T) {
	customBid := json.RawMessage(`{"mode":"custom"}`)
	disabledBoth := map[string]json.RawMessage{
		"bid":         json.RawMessage(`{"mode":"disabled"}`),
		"builder_api": json.RawMessage(`{"mode":"disabled"}`),
	}

	tests := []struct {
		name       string
		mode       config.ScheduleMode
		everyNth   uint64
		nextN      uint64
		startSlot  uint64
		slotsBuilt uint64
		epbsOn     bool
		apiOn      bool
		fork       version.DataVersion
		slot       uint64
		bidPatch   json.RawMessage
		apiPatch   json.RawMessage
		wantBuild  bool
		wantForced bool
		wantReason string
	}{
		{
			name: "mode all builds", mode: config.ScheduleModeAll,
			epbsOn: true, fork: version.DataVersionGloas, slot: 7001,
			wantBuild: true,
		},
		{
			name: "every_nth skips off-cadence slots", mode: config.ScheduleModeEveryN,
			everyNth: 4, epbsOn: true, fork: version.DataVersionGloas, slot: 7001,
			wantBuild: false, wantReason: BuildSkipReasonSchedule,
		},
		{
			name: "every_nth builds on-cadence slots", mode: config.ScheduleModeEveryN,
			everyNth: 4, epbsOn: true, fork: version.DataVersionGloas, slot: 7004,
			wantBuild: true,
		},
		{
			name: "every_nth honors start slot offset", mode: config.ScheduleModeEveryN,
			everyNth: 4, startSlot: 7001, epbsOn: true, fork: version.DataVersionGloas, slot: 7005,
			wantBuild: true,
		},
		{
			name: "start slot gates building", mode: config.ScheduleModeAll,
			startSlot: 8000, epbsOn: true, fork: version.DataVersionGloas, slot: 7001,
			wantBuild: false, wantReason: BuildSkipReasonSchedule,
		},
		{
			name: "next_n with remaining budget builds", mode: config.ScheduleModeNextN,
			nextN: 3, slotsBuilt: 2, epbsOn: true, fork: version.DataVersionGloas, slot: 7001,
			wantBuild: true,
		},
		{
			name: "next_n with exhausted budget skips", mode: config.ScheduleModeNextN,
			nextN: 3, slotsBuilt: 3, epbsOn: true, fork: version.DataVersionGloas, slot: 7001,
			wantBuild: false, wantReason: BuildSkipReasonSchedule,
		},
		{
			name: "custom plan forces past exhausted next_n", mode: config.ScheduleModeNextN,
			nextN: 3, slotsBuilt: 3, epbsOn: true, fork: version.DataVersionGloas, slot: 7001,
			bidPatch:  customBid,
			wantBuild: true, wantForced: true,
		},
		{
			name: "custom plan forces past every_nth", mode: config.ScheduleModeEveryN,
			everyNth: 4, epbsOn: true, fork: version.DataVersionGloas, slot: 7001,
			bidPatch:  customBid,
			wantBuild: true, wantForced: true,
		},
		{
			name: "custom on enabled module still forces", mode: config.ScheduleModeEveryN,
			everyNth: 4, epbsOn: true, apiOn: true, fork: version.DataVersionGloas, slot: 7001,
			apiPatch:  json.RawMessage(`{"mode":"custom"}`),
			wantBuild: true, wantForced: true,
		},
		{
			name: "custom on unavailable consumer cannot force", mode: config.ScheduleModeAll,
			epbsOn: false, apiOn: false, fork: version.DataVersionElectra, slot: 7001,
			bidPatch:  customBid,
			wantBuild: false, wantReason: BuildSkipReasonNoConsumer,
		},
		{
			name: "all consumers suppressed by plan", mode: config.ScheduleModeAll,
			epbsOn: true, apiOn: true, fork: version.DataVersionGloas, slot: 7001,
			bidPatch: disabledBoth["bid"], apiPatch: disabledBoth["builder_api"],
			wantBuild: false, wantReason: BuildSkipReasonPlanDisabled,
		},
		{
			name: "globals off without plan", mode: config.ScheduleModeAll,
			epbsOn: false, apiOn: false, fork: version.DataVersionGloas, slot: 7001,
			wantBuild: false, wantReason: BuildSkipReasonNoConsumer,
		},
		{
			name: "reveal-only plan falls through to schedule", mode: config.ScheduleModeEveryN,
			everyNth: 4, epbsOn: true, fork: version.DataVersionGloas, slot: 7001,
			wantBuild: false, wantReason: BuildSkipReasonSchedule,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chainSvc := newStubChain()
			chainSvc.fork = tt.fork

			cfg := config.DefaultConfig()
			cfg.EPBSEnabled = tt.epbsOn
			cfg.BuilderAPIEnabled = tt.apiOn
			cfg.APIPort = 1
			cfg.Schedule.Mode = tt.mode
			cfg.Schedule.EveryNth = tt.everyNth
			cfg.Schedule.NextN = tt.nextN
			cfg.Schedule.StartSlot = tt.startSlot
			cfg.EPBS.BuildStartTime = -1234

			svc := newTestService(chainSvc, cfg)
			svc.slotsBuilt = tt.slotsBuilt

			if tt.bidPatch != nil || tt.apiPatch != nil {
				_, err := svc.ApplyUpdates([]*PlanUpdate{{
					Slots:      []uint64{tt.slot},
					Bid:        tt.bidPatch,
					BuilderAPI: tt.apiPatch,
				}}, "tester")
				require.NoError(t, err)
			}

			frozen := svc.Freeze(phase0.Slot(tt.slot))
			require.NotNil(t, frozen.Build)
			require.Equal(t, tt.wantBuild, frozen.Build.Build, "build decision")
			require.Equal(t, tt.wantForced, frozen.Build.Forced, "forced")
			require.Equal(t, tt.wantReason, frozen.Build.SkipReason, "skip reason")
			require.Equal(t, int64(-1234), frozen.Build.BuildStartTimeMs)
		})
	}
}

func TestOnSlotBuiltNextNAccounting(t *testing.T) {
	chainSvc := newStubChain()

	cfg := config.DefaultConfig()
	cfg.EPBSEnabled = true
	cfg.Schedule.Mode = config.ScheduleModeNextN
	cfg.Schedule.NextN = 2

	svc := newTestService(chainSvc, cfg)

	// First two scheduled builds consume the budget.
	require.True(t, svc.Freeze(2000).Build.Build)
	svc.OnSlotBuilt(2000)
	require.Equal(t, uint64(1), svc.GetSlotsBuilt())
	require.Equal(t, 1, svc.GetSlotsRemaining())

	require.True(t, svc.Freeze(2001).Build.Build)
	svc.OnSlotBuilt(2001)
	require.Equal(t, 0, svc.GetSlotsRemaining())

	// Budget exhausted.
	require.False(t, svc.Freeze(2002).Build.Build)

	// A forced build past the budget does not consume it.
	_, err := svc.ApplyUpdates([]*PlanUpdate{{
		Slots: []uint64{2003},
		Bid:   json.RawMessage(`{"mode":"custom"}`),
	}}, "tester")
	require.NoError(t, err)

	frozen := svc.Freeze(2003)
	require.True(t, frozen.Build.Build)
	require.True(t, frozen.Build.Forced)

	svc.OnSlotBuilt(2003)
	require.Equal(t, uint64(2), svc.GetSlotsBuilt(), "forced build must not consume the budget")

	// Switching (back) to next_n via UpdateConfig resets the counter.
	svc.UpdateConfig()
	require.Equal(t, uint64(0), svc.GetSlotsBuilt())
	require.Equal(t, 2, svc.GetSlotsRemaining())
}

func TestSubscribeChangesDeliversCommittedEvents(t *testing.T) {
	chainSvc := newStubChain()
	svc := newTestService(chainSvc, nil)

	sub := svc.SubscribeChanges(4)
	defer sub.Unsubscribe()

	_, err := svc.ApplyUpdates([]*PlanUpdate{{
		Slots: []uint64{30000},
		Bid:   json.RawMessage(`{"mode":"disabled"}`),
	}}, "tester")
	require.NoError(t, err)

	select {
	case event := <-sub.Channel():
		require.Equal(t, []uint64{30000}, event.Slots)
	case <-time.After(time.Second):
		t.Fatal("expected a plan change event")
	}
}
