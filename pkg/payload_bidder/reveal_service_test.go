package payload_bidder

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// mockEnvelopePublisher records envelope publishes and can be set to fail.
type mockEnvelopePublisher struct {
	mu          sync.Mutex
	calls       int
	fail        bool
	validations []string // broadcast_validation level per call
}

var _ envelopePublisher = (*mockEnvelopePublisher)(nil)

func (p *mockEnvelopePublisher) SubmitExecutionPayloadEnvelope(
	_ context.Context, _ *eth2all.SignedExecutionPayloadEnvelope, _ [][]byte, _ [][]byte,
	broadcastValidation string,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.calls++
	p.validations = append(p.validations, broadcastValidation)

	if p.fail {
		return errors.New("publish failed")
	}

	return nil
}

func (p *mockEnvelopePublisher) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.calls
}

func (p *mockEnvelopePublisher) lastValidation() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.validations) == 0 {
		return ""
	}

	return p.validations[len(p.validations)-1]
}

// Defaults mirrored from config.DefaultConfig().Reveal for assertions.
const (
	defaultMaxRevealAttempts = 3
	defaultRevealRetryDelay  = 500 * time.Millisecond
)

// stubVoteSource is a controllable headVoteSource: tests preset or fire
// participation updates per slot.
type stubVoteSource struct {
	mu            sync.Mutex
	dispatcher    utils.Dispatcher[*chain.HeadVoteUpdate]
	participation map[phase0.Slot]*chain.HeadVoteUpdate
}

var _ headVoteSource = (*stubVoteSource)(nil)

func newStubVoteSource() *stubVoteSource {
	return &stubVoteSource{participation: make(map[phase0.Slot]*chain.HeadVoteUpdate, 4)}
}

func (s *stubVoteSource) SubscribeUpdates() *utils.Subscription[*chain.HeadVoteUpdate] {
	return s.dispatcher.Subscribe(16, false)
}

func (s *stubVoteSource) GetParticipation(slot phase0.Slot, root phase0.Root) (chain.HeadVoteUpdate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	u, ok := s.participation[slot]
	if !ok || u.BlockRoot != root {
		return chain.HeadVoteUpdate{}, false
	}

	return *u, true
}

// fire stores and dispatches a participation update.
func (s *stubVoteSource) fire(slot phase0.Slot, root phase0.Root, pct float64) {
	u := &chain.HeadVoteUpdate{Slot: slot, BlockRoot: root, ParticipationPct: pct}

	s.mu.Lock()
	s.participation[slot] = u
	s.mu.Unlock()

	s.dispatcher.Fire(u)
}

// revealTestEnv bundles the wiring shared by the reveal service tests.
type revealTestEnv struct {
	cfg        *config.Config
	chainSvc   *stubChainService
	builderSvc *payload_builder.Service
	payments   *PaymentTracker
	publisher  *mockEnvelopePublisher
	planSvc    *action_plan.PlanService
	votes      *stubVoteSource
	svc        *RevealService
}

// newRevealTestEnv creates a reveal service whose slot 1 starts "now", with
// the given slot duration and reveal time (ms into the slot). A real plan
// service is always wired in (it is a mandatory dependency); slots without a
// stored plan resolve to the global reveal time without a deadline bypass.
func newRevealTestEnv(t *testing.T, slotDuration time.Duration, revealTimeMs int64) *revealTestEnv {
	t.Helper()

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)

	cfg := &config.Config{}
	cfg.Reveal = config.DefaultConfig().Reveal
	cfg.Reveal.TimeMs = revealTimeMs

	chainSvc := &stubChainService{
		genesisTime:  time.Now().Add(-slotDuration), // slot 1 starts now
		slotDuration: slotDuration,
		currentFork:  version.DataVersionGloas,
	}
	builderSvc := newTestBuilderSvc(chainSvc)
	payments := NewPaymentTracker(chainSvc, log)
	publisher := &mockEnvelopePublisher{}
	planSvc := action_plan.NewPlanService(cfg, chainSvc, log)
	votes := newStubVoteSource()

	svc := NewRevealService(cfg, NewSigner(blsSigner), publisher, chainSvc, builderSvc,
		payments, planSvc, votes, log)

	return &revealTestEnv{
		cfg:        cfg,
		chainSvc:   chainSvc,
		builderSvc: builderSvc,
		payments:   payments,
		publisher:  publisher,
		planSvc:    planSvc,
		votes:      votes,
		svc:        svc,
	}
}

// applyRevealPlan stores a reveal category plan for the slot.
func applyRevealPlan(t *testing.T, planSvc *action_plan.PlanService, slot phase0.Slot, revealJSON string) {
	t.Helper()

	_, err := planSvc.ApplyUpdates([]*action_plan.PlanUpdate{{
		Slots:  []uint64{uint64(slot)},
		Reveal: json.RawMessage(revealJSON),
	}}, "test")
	require.NoError(t, err)
}

// waitForResult receives one reveal result or fails the test after timeout.
func waitForResult(t *testing.T, ch <-chan *RevealResult, timeout time.Duration) *RevealResult {
	t.Helper()

	select {
	case res := <-ch:
		return res
	case <-time.After(timeout):
		t.Fatal("timed out waiting for reveal result")
		return nil
	}
}

func TestRevealService_RevealsAtDueTime(t *testing.T) {
	env := newRevealTestEnv(t, 4*time.Second, 500)
	sub := env.svc.SubscribeResults(4, false)

	defer sub.Unsubscribe()

	require.NoError(t, env.svc.Start(context.Background()))
	defer env.svc.Stop()

	slot := phase0.Slot(1)
	blockRoot := phase0.Root{0x11}
	payload := newTestPayload(slot, phase0.Hash32{0xab}, big.NewInt(2_000_000_000_000)) // 2000 gwei

	env.payments.RecordWonBid(slot, 2000)

	env.svc.RequestReveal(&RevealRequest{
		Payload:   payload,
		BlockInfo: &beacon.BlockInfo{Slot: slot, Root: blockRoot, ParentRoot: phase0.Root{0x22}},
		Transport: payload_builder.BidTransportBuilderAPI,
	})

	// The reveal is due 500ms into the slot — nothing may publish early.
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 0, env.publisher.callCount(), "must not publish before the reveal time")

	require.Eventually(t, func() bool {
		return env.publisher.callCount() == 1
	}, 3*time.Second, 10*time.Millisecond, "expected exactly one publish after the reveal time")

	res := waitForResult(t, sub.Channel(), 2*time.Second)
	assert.True(t, res.Success)
	assert.False(t, res.Skipped)
	assert.Empty(t, res.SkipReason)
	assert.Equal(t, slot, res.Slot)
	assert.Equal(t, payload_builder.BidTransportBuilderAPI, res.Transport)
	assert.Equal(t, 1, res.Attempt)
	assert.Equal(t, defaultMaxRevealAttempts, res.MaxAttempts)
	assert.NotNil(t, res.Envelope, "successful result must carry the published envelope")

	reveal := payload.Reveal()
	require.NotNil(t, reveal, "payload must be marked revealed")
	assert.Equal(t, payload_builder.BidTransportBuilderAPI, reveal.Transport)
	assert.Equal(t, blockRoot, reveal.BeaconBlockRoot)

	// The pending payment moved to a balance deduction.
	assert.Equal(t, uint64(0), env.payments.GetTotalPendingPayments())
	assert.Equal(t, int64(-2000), env.payments.GetBalanceAdjustment())

	assert.Equal(t, uint64(1), env.builderSvc.GetStats().RevealsSuccess)
}

func TestRevealService_DedupsBySlot(t *testing.T) {
	env := newRevealTestEnv(t, 4*time.Second, 10)
	sub := env.svc.SubscribeResults(4, false)

	defer sub.Unsubscribe()

	require.NoError(t, env.svc.Start(context.Background()))
	defer env.svc.Stop()

	slot := phase0.Slot(1)
	payload := newTestPayload(slot, phase0.Hash32{0xab}, big.NewInt(1))
	blockInfo := &beacon.BlockInfo{Slot: slot, Root: phase0.Root{0x11}, ParentRoot: phase0.Root{0x22}}

	// Two requests for the same slot from different transports.
	env.svc.RequestReveal(&RevealRequest{
		Payload: payload, BlockInfo: blockInfo,
		Transport: payload_builder.BidTransportBuilderAPI,
	})
	env.svc.RequestReveal(&RevealRequest{
		Payload: payload, BlockInfo: blockInfo,
		Transport: payload_builder.BidTransportP2P,
	})

	require.Eventually(t, func() bool {
		return env.publisher.callCount() == 1
	}, 3*time.Second, 10*time.Millisecond)

	// Give the duplicate a chance to (wrongly) publish.
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 1, env.publisher.callCount(), "duplicate request for the slot must not publish again")

	// The first transport wins.
	res := waitForResult(t, sub.Channel(), 2*time.Second)
	assert.True(t, res.Success)
	assert.Equal(t, payload_builder.BidTransportBuilderAPI, res.Transport)

	reveal := payload.Reveal()
	require.NotNil(t, reveal)
	assert.Equal(t, payload_builder.BidTransportBuilderAPI, reveal.Transport)

	select {
	case extra := <-sub.Channel():
		t.Fatalf("unexpected second reveal result: %+v", extra)
	default:
	}
}

func TestRevealService_RetriesThenGivesUp(t *testing.T) {
	env := newRevealTestEnv(t, 10*time.Second, 10)
	env.publisher.fail = true
	sub := env.svc.SubscribeResults(8, false)

	defer sub.Unsubscribe()

	require.NoError(t, env.svc.Start(context.Background()))
	defer env.svc.Stop()

	slot := phase0.Slot(1)
	payload := newTestPayload(slot, phase0.Hash32{0xab}, big.NewInt(1))

	env.payments.RecordWonBid(slot, 42)

	env.svc.RequestReveal(&RevealRequest{
		Payload:   payload,
		BlockInfo: &beacon.BlockInfo{Slot: slot, Root: phase0.Root{0x11}, ParentRoot: phase0.Root{0x22}},
		Transport: payload_builder.BidTransportP2P,
	})

	require.Eventually(t, func() bool {
		return env.publisher.callCount() == defaultMaxRevealAttempts
	}, 5*time.Second, 10*time.Millisecond, "expected exactly defaultMaxRevealAttempts publish attempts")

	// No further attempts after the retry budget is spent.
	time.Sleep(defaultRevealRetryDelay + 200*time.Millisecond)
	assert.Equal(t, defaultMaxRevealAttempts, env.publisher.callCount())

	for attempt := 1; attempt <= defaultMaxRevealAttempts; attempt++ {
		res := waitForResult(t, sub.Channel(), 2*time.Second)
		assert.False(t, res.Success)
		assert.False(t, res.Skipped)
		assert.Empty(t, res.SkipReason)
		assert.Equal(t, attempt, res.Attempt)
		assert.Equal(t, defaultMaxRevealAttempts, res.MaxAttempts)
		assert.NotEmpty(t, res.Error)
		assert.NotNil(t, res.Envelope,
			"failed publish attempt %d must still carry the built envelope", attempt)
	}

	assert.Nil(t, payload.Reveal(), "payload must not be marked revealed")
	assert.Equal(t, uint64(42), env.payments.GetTotalPendingPayments(), "payment must stay pending")
	assert.Equal(t, uint64(1), env.builderSvc.GetStats().RevealsFailed)
}

func TestRevealService_SkipsStaleSlot(t *testing.T) {
	// No plan stored for the slot → the frozen plan resolves to the global
	// reveal time without a deadline bypass, so a stale request is skipped.
	env := newRevealTestEnv(t, 100*time.Millisecond, 10)
	// Slot 1 ended long ago.
	env.chainSvc.genesisTime = time.Now().Add(-time.Minute)

	sub := env.svc.SubscribeResults(4, false)
	defer sub.Unsubscribe()

	require.NoError(t, env.svc.Start(context.Background()))
	defer env.svc.Stop()

	slot := phase0.Slot(1)
	payload := newTestPayload(slot, phase0.Hash32{0xab}, big.NewInt(1))

	env.svc.RequestReveal(&RevealRequest{
		Payload:   payload,
		BlockInfo: &beacon.BlockInfo{Slot: slot, Root: phase0.Root{0x11}, ParentRoot: phase0.Root{0x22}},
		Transport: payload_builder.BidTransportP2P,
	})

	res := waitForResult(t, sub.Channel(), 2*time.Second)
	assert.True(t, res.Skipped)
	assert.False(t, res.Success)
	assert.Equal(t, RevealSkipReasonLate, res.SkipReason)
	assert.Equal(t, slot, res.Slot)
	assert.Nil(t, res.Envelope, "skipped reveals must not carry an envelope")

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 0, env.publisher.callCount(), "stale slot must never publish")
	assert.Nil(t, payload.Reveal())
}

func TestRevealService_PlanSuppressedReveal(t *testing.T) {
	env := newRevealTestEnv(t, 4*time.Second, 10)

	slot := phase0.Slot(1)
	applyRevealPlan(t, env.planSvc, slot, `{"mode":"disabled"}`)

	sub := env.svc.SubscribeResults(4, false)
	defer sub.Unsubscribe()

	require.NoError(t, env.svc.Start(context.Background()))
	defer env.svc.Stop()

	payload := newTestPayload(slot, phase0.Hash32{0xab}, big.NewInt(1))
	blockInfo := &beacon.BlockInfo{Slot: slot, Root: phase0.Root{0x11}, ParentRoot: phase0.Root{0x22}}

	// Two requests for the suppressed slot — the second must be a no-op too.
	env.svc.RequestReveal(&RevealRequest{
		Payload: payload, BlockInfo: blockInfo,
		Transport: payload_builder.BidTransportBuilderAPI,
	})
	env.svc.RequestReveal(&RevealRequest{
		Payload: payload, BlockInfo: blockInfo,
		Transport: payload_builder.BidTransportP2P,
	})

	res := waitForResult(t, sub.Channel(), 2*time.Second)
	assert.True(t, res.Skipped)
	assert.False(t, res.Success)
	assert.Equal(t, RevealSkipReasonPlanDisabled, res.SkipReason)
	assert.Equal(t, slot, res.Slot)
	assert.Equal(t, payload_builder.BidTransportBuilderAPI, res.Transport)
	assert.Nil(t, res.Envelope, "suppressed reveals must not carry an envelope")

	// Neither request may publish, and only one terminal result may fire.
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 0, env.publisher.callCount(), "suppressed slot must never publish")
	assert.Nil(t, payload.Reveal())

	select {
	case extra := <-sub.Channel():
		t.Fatalf("unexpected second reveal result: %+v", extra)
	default:
	}
}

func TestRevealService_BypassDeadlinePublishesLate(t *testing.T) {
	slotDuration := 300 * time.Millisecond
	env := newRevealTestEnv(t, slotDuration, 10)
	// Slot 1 ended 50ms ago: without a deadline bypass this request would be
	// skipped as late.
	env.chainSvc.genesisTime = time.Now().Add(-2*slotDuration - 50*time.Millisecond)

	slot := phase0.Slot(1)
	// Custom reveal time 500ms into the slot — past the slot end (300ms) but
	// within the validation bound of one extra slot.
	applyRevealPlan(t, env.planSvc, slot, `{"mode":"custom","reveal_time_ms":500}`)

	sub := env.svc.SubscribeResults(4, false)
	defer sub.Unsubscribe()

	require.NoError(t, env.svc.Start(context.Background()))
	defer env.svc.Stop()

	payload := newTestPayload(slot, phase0.Hash32{0xab}, big.NewInt(1))

	env.svc.RequestReveal(&RevealRequest{
		Payload:   payload,
		BlockInfo: &beacon.BlockInfo{Slot: slot, Root: phase0.Root{0x11}, ParentRoot: phase0.Root{0x22}},
		Transport: payload_builder.BidTransportP2P,
	})

	require.Eventually(t, func() bool {
		return env.publisher.callCount() == 1
	}, 3*time.Second, 10*time.Millisecond, "bypass-deadline reveal must publish despite the passed slot end")

	res := waitForResult(t, sub.Channel(), 2*time.Second)
	assert.True(t, res.Success)
	assert.False(t, res.Skipped)
	assert.Empty(t, res.SkipReason)
	assert.NotNil(t, res.Envelope)
	require.NotNil(t, payload.Reveal(), "payload must be marked revealed")
}

// revealRequest builds a standard request for slot 1.
func revealRequest(slot phase0.Slot, root phase0.Root) *RevealRequest {
	return &RevealRequest{
		Payload:   newTestPayload(slot, phase0.Hash32{0xab}, big.NewInt(1)),
		BlockInfo: &beacon.BlockInfo{Slot: slot, Root: root, ParentRoot: phase0.Root{0x22}},
		Transport: payload_builder.BidTransportP2P,
	}
}

func TestRevealService_VoteGateRevealsOnThreshold(t *testing.T) {
	env := newRevealTestEnv(t, 4*time.Second, 3500)
	env.cfg.Reveal.GateMode = config.RevealGateVote
	env.cfg.Reveal.VoteThresholdPct = 60

	sub := env.svc.SubscribeResults(4, false)
	defer sub.Unsubscribe()

	require.NoError(t, env.svc.Start(context.Background()))
	defer env.svc.Stop()

	slot := phase0.Slot(1)
	root := phase0.Root{0x11}
	env.svc.RequestReveal(revealRequest(slot, root))

	// Below the threshold: nothing may publish.
	time.Sleep(150 * time.Millisecond)
	env.votes.fire(slot, root, 40)
	time.Sleep(150 * time.Millisecond)
	require.Equal(t, 0, env.publisher.callCount(), "must not publish below the vote threshold")

	// Crossing the threshold publishes immediately — far before the 3500ms
	// time gate (which this mode ignores).
	require.Eventually(t, func() bool {
		env.votes.fire(slot, root, 65)
		return env.publisher.callCount() == 1
	}, 2*time.Second, 25*time.Millisecond)

	res := waitForResult(t, sub.Channel(), 2*time.Second)
	assert.True(t, res.Success)
}

func TestRevealService_VoteGateAlreadyMetAtSchedule(t *testing.T) {
	env := newRevealTestEnv(t, 4*time.Second, 3500)
	env.cfg.Reveal.GateMode = config.RevealGateVoteOrTime
	env.cfg.Reveal.VoteThresholdPct = 60

	require.NoError(t, env.svc.Start(context.Background()))
	defer env.svc.Stop()

	slot := phase0.Slot(1)
	root := phase0.Root{0x11}

	// Participation is already above the threshold when the request arrives.
	env.votes.fire(slot, root, 80)
	env.svc.RequestReveal(revealRequest(slot, root))

	require.Eventually(t, func() bool {
		return env.publisher.callCount() == 1
	}, 2*time.Second, 10*time.Millisecond, "must publish immediately, not wait for the time gate")
}

func TestRevealService_VoteOrTimeFallsBackToTimeGate(t *testing.T) {
	env := newRevealTestEnv(t, 4*time.Second, 400)
	env.cfg.Reveal.GateMode = config.RevealGateVoteOrTime
	env.cfg.Reveal.VoteThresholdPct = 60

	require.NoError(t, env.svc.Start(context.Background()))
	defer env.svc.Stop()

	slot := phase0.Slot(1)
	env.svc.RequestReveal(revealRequest(slot, phase0.Root{0x11}))

	// The threshold is never reached — the time gate publishes at 400ms.
	time.Sleep(150 * time.Millisecond)
	require.Equal(t, 0, env.publisher.callCount())

	require.Eventually(t, func() bool {
		return env.publisher.callCount() == 1
	}, 2*time.Second, 10*time.Millisecond)
}

func TestRevealService_VoteAndTimeWaitsForBoth(t *testing.T) {
	env := newRevealTestEnv(t, 4*time.Second, 600)
	env.cfg.Reveal.GateMode = config.RevealGateVoteAndTime
	env.cfg.Reveal.VoteThresholdPct = 60

	require.NoError(t, env.svc.Start(context.Background()))
	defer env.svc.Stop()

	slot := phase0.Slot(1)
	root := phase0.Root{0x11}

	// Vote gate opens right away; the time gate must still hold the reveal.
	env.votes.fire(slot, root, 90)
	env.svc.RequestReveal(revealRequest(slot, root))

	time.Sleep(250 * time.Millisecond)
	require.Equal(t, 0, env.publisher.callCount(), "and-mode must wait for the time gate")

	require.Eventually(t, func() bool {
		return env.publisher.callCount() == 1
	}, 2*time.Second, 10*time.Millisecond)
}

func TestRevealService_VoteGateTimeoutWithholds(t *testing.T) {
	env := newRevealTestEnv(t, 700*time.Millisecond, 100)
	env.cfg.Reveal.GateMode = config.RevealGateVote
	env.cfg.Reveal.VoteThresholdPct = 60

	sub := env.svc.SubscribeResults(4, false)
	defer sub.Unsubscribe()

	require.NoError(t, env.svc.Start(context.Background()))
	defer env.svc.Stop()

	env.svc.RequestReveal(revealRequest(1, phase0.Root{0x11}))

	res := waitForResult(t, sub.Channel(), 3*time.Second)
	assert.True(t, res.Skipped)
	assert.Equal(t, RevealSkipReasonVoteGateTimeout, res.SkipReason)
	assert.Equal(t, 0, env.publisher.callCount())
}

func TestRevealService_GloballyDisabled(t *testing.T) {
	env := newRevealTestEnv(t, 4*time.Second, 10)
	env.cfg.Reveal.Enabled = false

	sub := env.svc.SubscribeResults(4, false)
	defer sub.Unsubscribe()

	require.NoError(t, env.svc.Start(context.Background()))
	defer env.svc.Stop()

	env.svc.RequestReveal(revealRequest(1, phase0.Root{0x11}))

	res := waitForResult(t, sub.Channel(), 2*time.Second)
	assert.True(t, res.Skipped)
	assert.Equal(t, RevealSkipReasonDisabled, res.SkipReason)
	assert.Equal(t, 0, env.publisher.callCount())
}

func TestRevealService_PlanCustomForcesDespiteGlobalDisable(t *testing.T) {
	env := newRevealTestEnv(t, 4*time.Second, 50)
	env.cfg.Reveal.Enabled = false

	applyRevealPlan(t, env.planSvc, 1, `{"mode":"custom"}`)

	require.NoError(t, env.svc.Start(context.Background()))
	defer env.svc.Stop()

	env.svc.RequestReveal(revealRequest(1, phase0.Root{0x11}))

	require.Eventually(t, func() bool {
		return env.publisher.callCount() == 1
	}, 2*time.Second, 10*time.Millisecond, "plan custom mode must force the reveal")
}

func TestRevealService_BroadcastValidationPassthrough(t *testing.T) {
	env := newRevealTestEnv(t, 4*time.Second, 10)
	env.cfg.Reveal.BroadcastValidation = config.BroadcastValidationConsensusAndEquivocation

	require.NoError(t, env.svc.Start(context.Background()))
	defer env.svc.Stop()

	env.svc.RequestReveal(revealRequest(1, phase0.Root{0x11}))

	require.Eventually(t, func() bool {
		return env.publisher.callCount() == 1
	}, 2*time.Second, 10*time.Millisecond)

	assert.Equal(t, config.BroadcastValidationConsensusAndEquivocation, env.publisher.lastValidation())
}

func TestRevealService_RetryPolicyFromConfig(t *testing.T) {
	env := newRevealTestEnv(t, 4*time.Second, 10)
	env.cfg.Reveal.MaxAttempts = 2
	env.cfg.Reveal.RetryIntervalMs = 50
	env.publisher.fail = true

	sub := env.svc.SubscribeResults(8, false)
	defer sub.Unsubscribe()

	require.NoError(t, env.svc.Start(context.Background()))
	defer env.svc.Stop()

	env.svc.RequestReveal(revealRequest(1, phase0.Root{0x11}))

	require.Eventually(t, func() bool {
		return env.publisher.callCount() == 2
	}, 2*time.Second, 10*time.Millisecond)

	// No further attempts past the configured budget.
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 2, env.publisher.callCount())

	for attempt := 1; attempt <= 2; attempt++ {
		res := waitForResult(t, sub.Channel(), 2*time.Second)
		assert.False(t, res.Success)
		assert.Equal(t, attempt, res.Attempt)
		assert.Equal(t, 2, res.MaxAttempts)
	}
}
