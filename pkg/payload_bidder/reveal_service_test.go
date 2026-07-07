package payload_bidder

import (
	"context"
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

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// mockEnvelopePublisher records envelope publishes and can be set to fail.
type mockEnvelopePublisher struct {
	mu    sync.Mutex
	calls int
	fail  bool
}

var _ envelopePublisher = (*mockEnvelopePublisher)(nil)

func (p *mockEnvelopePublisher) SubmitExecutionPayloadEnvelope(
	_ context.Context, _ *eth2all.SignedExecutionPayloadEnvelope, _ [][]byte, _ [][]byte,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.calls++

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

// revealTestEnv bundles the wiring shared by the reveal service tests.
type revealTestEnv struct {
	cfg        *config.Config
	chainSvc   *stubChainService
	builderSvc *payload_builder.Service
	payments   *PaymentTracker
	publisher  *mockEnvelopePublisher
	svc        *RevealService
}

// newRevealTestEnv creates a reveal service whose slot 1 starts "now", with
// the given slot duration and reveal time (ms into the slot).
func newRevealTestEnv(t *testing.T, slotDuration time.Duration, revealTimeMs int64) *revealTestEnv {
	t.Helper()

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)

	cfg := &config.Config{}
	cfg.EPBS.RevealTime = revealTimeMs

	chainSvc := &stubChainService{
		genesisTime:  time.Now().Add(-slotDuration), // slot 1 starts now
		slotDuration: slotDuration,
		currentFork:  version.DataVersionGloas,
	}
	builderSvc := newTestBuilderSvc(chainSvc)
	payments := NewPaymentTracker(chainSvc, log)
	publisher := &mockEnvelopePublisher{}

	svc := NewRevealService(cfg, NewSigner(blsSigner), publisher, chainSvc, builderSvc, payments, log)

	return &revealTestEnv{
		cfg:        cfg,
		chainSvc:   chainSvc,
		builderSvc: builderSvc,
		payments:   payments,
		publisher:  publisher,
		svc:        svc,
	}
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
	sub := env.svc.SubscribeResults(4)

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
	assert.Equal(t, slot, res.Slot)
	assert.Equal(t, payload_builder.BidTransportBuilderAPI, res.Transport)
	assert.Equal(t, 1, res.Attempt)
	assert.Equal(t, maxRevealAttempts, res.MaxAttempts)

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
	sub := env.svc.SubscribeResults(4)

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
	sub := env.svc.SubscribeResults(8)

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
		return env.publisher.callCount() == maxRevealAttempts
	}, 5*time.Second, 10*time.Millisecond, "expected exactly maxRevealAttempts publish attempts")

	// No further attempts after the retry budget is spent.
	time.Sleep(revealRetryDelay + 200*time.Millisecond)
	assert.Equal(t, maxRevealAttempts, env.publisher.callCount())

	for attempt := 1; attempt <= maxRevealAttempts; attempt++ {
		res := waitForResult(t, sub.Channel(), 2*time.Second)
		assert.False(t, res.Success)
		assert.False(t, res.Skipped)
		assert.Equal(t, attempt, res.Attempt)
		assert.Equal(t, maxRevealAttempts, res.MaxAttempts)
		assert.NotEmpty(t, res.Error)
	}

	assert.Nil(t, payload.Reveal(), "payload must not be marked revealed")
	assert.Equal(t, uint64(42), env.payments.GetTotalPendingPayments(), "payment must stay pending")
	assert.Equal(t, uint64(1), env.builderSvc.GetStats().RevealsFailed)
}

func TestRevealService_SkipsStaleSlot(t *testing.T) {
	env := newRevealTestEnv(t, 100*time.Millisecond, 10)
	// Slot 1 ended long ago.
	env.chainSvc.genesisTime = time.Now().Add(-time.Minute)

	sub := env.svc.SubscribeResults(4)
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
	assert.Equal(t, slot, res.Slot)

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 0, env.publisher.callCount(), "stale slot must never publish")
	assert.Nil(t, payload.Reveal())
}
