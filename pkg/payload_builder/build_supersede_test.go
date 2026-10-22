package payload_builder

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/capella"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// pastChainService places every slot just in the past: its build start time
// has passed (late attributes take the late-build path) while the slot has
// not ended yet (late builds are still accepted).
type pastChainService struct {
	*stubChainService

	genesis time.Time
}

// newPastChainService anchors genesis so that testSlot started one second ago.
func newPastChainService(spec *chain.ChainSpec, testSlot phase0.Slot) *pastChainService {
	slotDuration := time.Duration(testSlot) * spec.SlotDuration

	return &pastChainService{
		stubChainService: &stubChainService{spec: spec},
		genesis:          time.Now().Add(-time.Second - slotDuration),
	}
}

func (s *pastChainService) SlotToTime(slot phase0.Slot) time.Time {
	return s.genesis.Add(time.Duration(slot) * s.spec.SlotDuration)
}

func supersedeTestService(t *testing.T, chainSvc chain.Service) *Service {
	t.Helper()

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	cfg := config.DefaultConfig()
	cfg.EPBSEnabled = true
	planSvc := action_plan.NewPlanService(cfg, chainSvc, log)

	clClient, err := beacon.NewClient(context.Background(), "http://127.0.0.1:1", log)
	require.NoError(t, err)

	svc, err := NewService(cfg, clClient, chainSvc, planSvc, nil, common.Address{}, log)
	require.NoError(t, err)

	svc.ctx = context.Background()
	svc.payloadBuilder = NewPayloadBuilder(clClient, nil, nil, nil, chainSvc, common.Address{}, cfg, log, nil)

	return svc
}

// synthesizedAttrs returns the attributes the missing-block fallback would
// synthesize for slot 133 from slot 132 (pre-epoch-transition withdrawals).
func synthesizedAttrs() *beacon.PayloadAttributesEvent {
	return &beacon.PayloadAttributesEvent{
		ProposalSlot:    133,
		ProposerIndex:   4,
		ParentBlockRoot: phase0.Root{0x01},
		ParentBlockHash: phase0.Hash32{0xaa},
		Timestamp:       1396,
		PrevRandao:      phase0.Root{0xcc},
		Withdrawals: []*capella.Withdrawal{
			{Index: 1, ValidatorIndex: 10, Amount: 84045},
		},
		Synthesized: true,
	}
}

// registerBuild registers a started target for the event's tuple the way
// executeCandidateBuild does, returning it and the context it would run the
// engine build with.
func registerBuild(
	svc *Service, attrs *beacon.PayloadAttributesEvent, synthesized bool,
) (*candidateBuild, context.Context) {
	ctx, cancel := context.WithCancel(context.Background())

	svc.scheduledBuildMu.Lock()
	defer svc.scheduledBuildMu.Unlock()

	state := svc.slotBuilds[attrs.ProposalSlot]
	if state == nil {
		state = newSlotBuildState()
		state.passScheduled = true
		svc.slotBuilds[attrs.ProposalSlot] = state
	}

	build := &candidateBuild{
		svc:         svc,
		slot:        attrs.ProposalSlot,
		candidate:   chain.CandidateParentFull,
		seq:         svc.buildSeq.Add(1),
		attrs:       attrs,
		synthesized: synthesized,
		cancel:      cancel,
	}
	state.started[beacon.AttrParentKeyOf(attrs)] = build

	return build, ctx
}

func (s *Service) startedBuild(slot phase0.Slot, key beacon.AttrParentKey) *candidateBuild {
	s.scheduledBuildMu.Lock()
	defer s.scheduledBuildMu.Unlock()

	if state := s.slotBuilds[slot]; state != nil {
		return state.started[key]
	}

	return nil
}

// nodeAttrs returns the node's attributes for the synthesized ones, with the
// post-transition withdrawal amounts.
func nodeAttrs(synthesized *beacon.PayloadAttributesEvent) *beacon.PayloadAttributesEvent {
	real := *synthesized
	real.Synthesized = false
	real.Withdrawals = []*capella.Withdrawal{{Index: 1, ValidatorIndex: 10, Amount: 104426}}

	return &real
}

func TestSupersedeSynthesizedBuild_AbortsInFlightBuild(t *testing.T) {
	spec := &chain.ChainSpec{SlotDuration: 12 * time.Second, SlotsPerEpoch: 32}
	svc := supersedeTestService(t, &stubChainService{spec: spec})

	synthesized := synthesizedAttrs()
	build, ctx := registerBuild(svc, synthesized, true)

	events := svc.SubscribeBuildEvents(4, false)
	defer events.Unsubscribe()

	real := nodeAttrs(synthesized)
	require.True(t, svc.supersedeSynthesizedBuild(real))

	assert.Error(t, ctx.Err(), "the in-flight engine build is cancelled")
	assert.True(t, build.isAborted())
	assert.Nil(t, svc.startedBuild(133, beacon.AttrParentKeyOf(real)), "tuple released for the rebuild")

	select {
	case event := <-events.Channel():
		target, ok := event.(*BuildTargetEvent)
		require.True(t, ok)
		assert.Equal(t, TargetStatusSuperseded, target.Status)
		assert.Equal(t, build.seq, target.Seq)
	default:
		t.Fatal("the superseded target must be reported")
	}

	require.False(t, svc.supersedeSynthesizedBuild(real), "nothing left to supersede")
}

func TestSupersedeSynthesizedBuild_KeepsEqualInputs(t *testing.T) {
	spec := &chain.ChainSpec{SlotDuration: 12 * time.Second, SlotsPerEpoch: 32}
	svc := supersedeTestService(t, &stubChainService{spec: spec})

	synthesized := synthesizedAttrs()
	build, ctx := registerBuild(svc, synthesized, true)

	real := *synthesized
	real.Synthesized = false
	real.ParentBlockNumber = 31 // informational, backfilled by sanitization
	real.Withdrawals = []*capella.Withdrawal{{Index: 1, ValidatorIndex: 10, Amount: 84045}}

	require.False(t, svc.supersedeSynthesizedBuild(&real), "identical inputs keep the build")
	assert.NoError(t, ctx.Err())
	assert.False(t, build.synthesized, "the build now counts as confirmed by the node")
	assert.Same(t, build, svc.startedBuild(133, beacon.AttrParentKeyOf(&real)))

	// Once confirmed, a later differing node event no longer aborts it (the
	// regular variant handling applies).
	differing := real
	differing.Timestamp++
	require.False(t, svc.supersedeSynthesizedBuild(&differing))
	assert.NoError(t, ctx.Err())
}

func TestSupersedeSynthesizedBuild_IgnoresNodeBuilds(t *testing.T) {
	spec := &chain.ChainSpec{SlotDuration: 12 * time.Second, SlotsPerEpoch: 32}
	svc := supersedeTestService(t, &stubChainService{spec: spec})

	attrs := synthesizedAttrs()
	attrs.Synthesized = false
	_, ctx := registerBuild(svc, attrs, false)

	updated := *attrs
	updated.Timestamp++

	require.False(t, svc.supersedeSynthesizedBuild(&updated))
	assert.NoError(t, ctx.Err())

	// Unknown slot / tuple: nothing to do.
	other := *attrs
	other.ProposalSlot = 99
	require.False(t, svc.supersedeSynthesizedBuild(&other))
}

// TestSupersedeSynthesizedBuild_WithdrawsEveryPayload covers a target that
// already built and selected: the engine payload and the testing run's
// upgrade both leave the cache, the superseded report is the last event and
// nothing of the target is reported or selected afterwards.
func TestSupersedeSynthesizedBuild_WithdrawsEveryPayload(t *testing.T) {
	spec := &chain.ChainSpec{SlotDuration: 12 * time.Second, SlotsPerEpoch: 32}
	svc := supersedeTestService(t, &stubChainService{spec: spec})

	synthesized := synthesizedAttrs()
	build, _ := registerBuild(svc, synthesized, true)

	engine := &Payload{Attributes: synthesized, BlockHash: phase0.Hash32{0xe0}, Build: BuildRef{Kind: BuildKindEngine}}
	upgrade := &Payload{Attributes: synthesized, BlockHash: phase0.Hash32{0x70}, Build: BuildRef{Kind: BuildKindTesting, Run: 1}}

	build.reportEngine(&EngineBuildEvent{Status: EngineStatusReady, Payload: engine})
	require.True(t, build.selectPayload(engine, SelectionFirst, false))
	build.reportTesting(&TestingBuildEvent{Run: 1, Status: LocalStatusReady, Payload: upgrade})
	require.True(t, build.selectPayload(upgrade, SelectionBetter, false))
	require.Same(t, upgrade, svc.payloadCache.Get(133))

	events := svc.SubscribeBuildEvents(4, false)
	defer events.Unsubscribe()

	require.True(t, svc.supersedeSynthesizedBuild(nodeAttrs(synthesized)))

	assert.Nil(t, svc.payloadCache.Get(133), "the stale payloads must never be bid")
	assert.Nil(t, svc.payloadCache.GetByBlockHash(engine.BlockHash))
	assert.Nil(t, svc.payloadCache.GetByBlockHash(upgrade.BlockHash))

	select {
	case event := <-events.Channel():
		target, ok := event.(*BuildTargetEvent)
		require.True(t, ok)
		assert.Equal(t, TargetStatusSuperseded, target.Status)
		assert.Equal(t, errBuildSuperseded, target.Error)
		assert.Equal(t, build.seq, target.Seq, "the report carries the superseded target's seq")
	default:
		t.Fatal("a selected target must be reported as superseded")
	}

	// The cancelled engine build's failure and a late run's pick stay silent.
	build.reportEngine(&EngineBuildEvent{Status: EngineStatusFailed, Error: "context canceled"})
	assert.False(t, build.selectPayload(upgrade, SelectionBetter, false))
	build.fireTarget(&BuildTargetEvent{Status: TargetStatusNoPayload})

	select {
	case event := <-events.Channel():
		t.Fatalf("the superseded report is the target's last event, got %T", event)
	default:
	}

	assert.Nil(t, svc.payloadCache.Get(133))
}

// TestHandlePayloadAttributes_RebuildsAfterSupersede drives the whole path
// through the attributes handler: the fallback's synthesized target is
// aborted by the node's differing event and a fresh target starts from the
// node's attributes on the same parent tuple.
func TestHandlePayloadAttributes_RebuildsAfterSupersede(t *testing.T) {
	spec := &chain.ChainSpec{SlotDuration: 12 * time.Second, SlotsPerEpoch: 32}
	chainSvc := newPastChainService(spec, 133)
	svc := supersedeTestService(t, chainSvc)

	synthesized := synthesizedAttrs()
	require.True(t, svc.clClient.Events().InjectPayloadAttributes(synthesized))

	stale, ctx := registerBuild(svc, synthesized, true)
	key := beacon.AttrParentKeyOf(synthesized)

	events := svc.SubscribeBuildEvents(8, false)
	defer events.Unsubscribe()

	svc.handlePayloadAttributesEvent(nodeAttrs(synthesized))

	assert.Error(t, ctx.Err(), "synthesized target aborted")
	assert.True(t, stale.isAborted())

	// The late-build path starts a fresh target from the node's attributes
	// (it fails against the offline clients, which is fine: it ran).
	require.Eventually(t, func() bool {
		build := svc.startedBuild(133, key)
		return build != nil && build != stale && !build.synthesized && build.seq > stale.seq
	}, 2*time.Second, 10*time.Millisecond, "a new target from the node's attributes must start")

	var statuses []string

	timeout := time.After(2 * time.Second)

	for len(statuses) < 3 {
		select {
		case event := <-events.Channel():
			if target, ok := event.(*BuildTargetEvent); ok {
				statuses = append(statuses, target.Status)
			}
		case <-timeout:
			t.Fatalf("the rebuild must report its outcome, got %v", statuses)
		}
	}

	assert.Equal(t, []string{TargetStatusSuperseded, TargetStatusStarted, TargetStatusNoPayload}, statuses)

	// A synthesized event never supersedes anything.
	again := *synthesized
	svc.handlePayloadAttributesEvent(&again)
	assert.NotNil(t, svc.startedBuild(133, key))
}
