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
	slotDuration := time.Duration(testSlot) * spec.SecondsPerSlot

	return &pastChainService{
		stubChainService: &stubChainService{spec: spec},
		genesis:          time.Now().Add(-time.Second - slotDuration),
	}
}

func (s *pastChainService) SlotToTime(slot phase0.Slot) time.Time {
	return s.genesis.Add(time.Duration(slot) * s.spec.SecondsPerSlot)
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
	svc.payloadBuilder = NewPayloadBuilder(clClient, nil, chainSvc, common.Address{}, cfg, log, nil)

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

// registerBuild registers a started build for the event's tuple the way
// executeCandidateBuild does, returning the build and the context it would
// run the engine build with.
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

	build := &candidateBuild{attrs: attrs, synthesized: synthesized, cancel: cancel}
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

func TestSupersedeSynthesizedBuild_AbortsInFlightBuild(t *testing.T) {
	spec := &chain.ChainSpec{SecondsPerSlot: 12 * time.Second, SlotsPerEpoch: 32}
	svc := supersedeTestService(t, &stubChainService{spec: spec})

	synthesized := synthesizedAttrs()
	build, ctx := registerBuild(svc, synthesized, true)

	failedSub := svc.SubscribePayloadBuildFailed(4, false)
	defer failedSub.Unsubscribe()

	// The node's attributes carry the post-transition withdrawal amounts.
	real := *synthesized
	real.Synthesized = false
	real.Withdrawals = []*capella.Withdrawal{{Index: 1, ValidatorIndex: 10, Amount: 104426}}

	require.True(t, svc.supersedeSynthesizedBuild(&real))

	assert.Error(t, ctx.Err(), "the in-flight engine build is cancelled")
	assert.True(t, svc.buildAborted(build))
	assert.Nil(t, svc.startedBuild(133, beacon.AttrParentKeyOf(&real)),
		"tuple released for the rebuild")

	select {
	case <-failedSub.Channel():
		t.Fatal("a running build reports its own abort, supersede must not report it")
	default:
	}

	// The build goroutine finishing after the abort never emits its payload.
	require.False(t, svc.supersedeSynthesizedBuild(&real), "nothing left to supersede")
}

func TestSupersedeSynthesizedBuild_KeepsEqualInputs(t *testing.T) {
	spec := &chain.ChainSpec{SecondsPerSlot: 12 * time.Second, SlotsPerEpoch: 32}
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
	spec := &chain.ChainSpec{SecondsPerSlot: 12 * time.Second, SlotsPerEpoch: 32}
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

func TestSupersedeSynthesizedBuild_WithdrawsEmittedPayload(t *testing.T) {
	spec := &chain.ChainSpec{SecondsPerSlot: 12 * time.Second, SlotsPerEpoch: 32}
	svc := supersedeTestService(t, &stubChainService{spec: spec})

	synthesized := synthesizedAttrs()
	build, _ := registerBuild(svc, synthesized, true)

	// The build already finished: its payload is cached and was dispatched.
	payload := &Payload{
		Attributes: synthesized,
		BlockHash:  phase0.Hash32{0xb0, 0xd7},
		Candidate:  chain.CandidateParentFull,
		ReadyAt:    time.Now(),
	}

	svc.scheduledBuildMu.Lock()
	build.seq = 7
	build.payload = payload
	build.readyFired = true
	svc.payloadCache.Store(payload)
	svc.scheduledBuildMu.Unlock()

	failedSub := svc.SubscribePayloadBuildFailed(4, false)
	defer failedSub.Unsubscribe()

	real := *synthesized
	real.Synthesized = false
	real.Withdrawals = []*capella.Withdrawal{{Index: 1, ValidatorIndex: 10, Amount: 104426}}

	require.True(t, svc.supersedeSynthesizedBuild(&real))

	assert.Nil(t, svc.payloadCache.Get(133), "the stale payload must never be bid")
	assert.Nil(t, svc.payloadCache.GetByBlockHash(payload.BlockHash))

	select {
	case event := <-failedSub.Channel():
		assert.Equal(t, phase0.Slot(133), event.Slot)
		assert.Equal(t, string(chain.CandidateParentFull), event.Candidate)
		assert.Equal(t, errBuildSuperseded, event.Error)
		assert.Equal(t, uint64(7), event.BuildSeq, "the failure carries the superseded build's seq")
	default:
		t.Fatal("a finished build must be reported as superseded")
	}
}

// TestSupersedeSynthesizedBuild_FailureFollowsInFlightReady covers the window
// between caching the payload and finishing its ready dispatch: the supersede
// withdraws the payload but leaves the failure report to the build goroutine,
// so consumers always see the ready event before the superseded failure.
func TestSupersedeSynthesizedBuild_FailureFollowsInFlightReady(t *testing.T) {
	spec := &chain.ChainSpec{SecondsPerSlot: 12 * time.Second, SlotsPerEpoch: 32}
	svc := supersedeTestService(t, &stubChainService{spec: spec})

	synthesized := synthesizedAttrs()
	build, _ := registerBuild(svc, synthesized, true)

	payload := &Payload{
		Attributes: synthesized,
		BlockHash:  phase0.Hash32{0xb0, 0xd7},
		Candidate:  chain.CandidateParentFull,
		ReadyAt:    time.Now(),
	}

	// Cached, ready event still being dispatched (readyFired not yet set).
	svc.scheduledBuildMu.Lock()
	build.seq = 3
	build.payload = payload
	svc.payloadCache.Store(payload)
	svc.scheduledBuildMu.Unlock()

	failedSub := svc.SubscribePayloadBuildFailed(4, false)
	defer failedSub.Unsubscribe()

	real := *synthesized
	real.Synthesized = false
	real.Withdrawals = []*capella.Withdrawal{{Index: 1, ValidatorIndex: 10, Amount: 104426}}

	require.True(t, svc.supersedeSynthesizedBuild(&real))
	assert.Nil(t, svc.payloadCache.Get(133), "withdrawn immediately")

	select {
	case <-failedSub.Channel():
		t.Fatal("the failure must wait for the build's ready dispatch to finish")
	default:
	}

	// The build goroutine finishes its ready dispatch and reports the abort.
	svc.completeBuild(build)

	select {
	case event := <-failedSub.Channel():
		assert.Equal(t, errBuildSuperseded, event.Error)
		assert.Equal(t, uint64(3), event.BuildSeq)
	default:
		t.Fatal("the finished ready dispatch must be followed by the superseded failure")
	}

	// A build that was never aborted reports nothing on completion.
	other := synthesizedAttrs()
	other.ParentBlockHash = phase0.Hash32{0xbb}
	otherBuild, _ := registerBuild(svc, other, false)
	otherBuild.payload = &Payload{Attributes: other, Candidate: chain.CandidateParentEmpty}
	svc.completeBuild(otherBuild)

	select {
	case <-failedSub.Channel():
		t.Fatal("an unaborted build must not report a failure")
	default:
	}
}

// TestHandlePayloadAttributes_RebuildsAfterSupersede drives the whole path
// through the attributes handler: the fallback's synthesized build is
// aborted by the node's differing event and a fresh build starts from the
// node's attributes on the same parent tuple.
func TestHandlePayloadAttributes_RebuildsAfterSupersede(t *testing.T) {
	spec := &chain.ChainSpec{SecondsPerSlot: 12 * time.Second, SlotsPerEpoch: 32}
	chainSvc := newPastChainService(spec, 133)
	svc := supersedeTestService(t, chainSvc)

	synthesized := synthesizedAttrs()
	require.True(t, svc.clClient.Events().InjectPayloadAttributes(synthesized))

	stale, ctx := registerBuild(svc, synthesized, true)
	key := beacon.AttrParentKeyOf(synthesized)

	failedSub := svc.SubscribePayloadBuildFailed(4, false)
	defer failedSub.Unsubscribe()

	real := *synthesized
	real.Synthesized = false
	real.Withdrawals = []*capella.Withdrawal{{Index: 1, ValidatorIndex: 10, Amount: 104426}}

	svc.handlePayloadAttributesEvent(&real)

	assert.Error(t, ctx.Err(), "synthesized build aborted")
	assert.True(t, svc.buildAborted(stale))

	// The late-build path starts a fresh build from the node's attributes
	// (it fails against the offline clients, which is fine: it ran).
	require.Eventually(t, func() bool {
		build := svc.startedBuild(133, key)
		return build != nil && build != stale && !build.synthesized && build.seq > stale.seq
	}, 2*time.Second, 10*time.Millisecond, "a new build from the node's attributes must start")

	select {
	case event := <-failedSub.Channel():
		assert.NotEqual(t, errBuildSuperseded, event.Error, "the rebuild fails for its own reason")
	case <-time.After(2 * time.Second):
		t.Fatal("the rebuild must report its outcome")
	}

	// A synthesized event never supersedes anything.
	again := *synthesized
	svc.handlePayloadAttributesEvent(&again)
	assert.NotNil(t, svc.startedBuild(133, key))
}

func TestPayloadCacheRemove(t *testing.T) {
	cache := NewPayloadCache(4)
	attrs := synthesizedAttrs()

	first := &Payload{Attributes: attrs, BlockHash: phase0.Hash32{0x01}}
	cache.Store(first)

	replacement := &Payload{Attributes: attrs, BlockHash: phase0.Hash32{0x02}}
	cache.Store(replacement)

	assert.False(t, cache.Remove(first), "a newer build on the tuple stays")
	assert.Same(t, replacement, cache.Get(133))

	assert.True(t, cache.Remove(replacement))
	assert.Nil(t, cache.Get(133))
	assert.Equal(t, 0, cache.Size())
}
