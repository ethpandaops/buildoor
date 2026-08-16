package payload_bidder

// Test for NM-05: the reveal loop used to build+publish inline in its sole
// run() goroutine, so one slot's slow (or hanging) publish call blocked
// every other pending slot's attempt behind it -- a won slot's reveal could
// miss its own deadline purely because an earlier slot's publish was slow,
// with no fault of its own. attemptReveal now runs each slot's build+publish
// off-loop, so slots no longer serialize behind one another.

import (
	"context"
	"math/big"
	"sync"
	"testing"
	"time"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// delayedFirstCallPublisher blocks for `delay` on its first call (modeling a
// slow real-world publish) and returns instantly on every subsequent call.
// Records the wall-clock start time of each call.
type delayedFirstCallPublisher struct {
	mu         sync.Mutex
	delay      time.Duration
	callStarts []time.Time
}

var _ envelopePublisher = (*delayedFirstCallPublisher)(nil)

func (p *delayedFirstCallPublisher) SubmitExecutionPayloadEnvelope(
	_ context.Context, _ *eth2all.SignedExecutionPayloadEnvelope, _ [][]byte, _ [][]byte, _ string,
) error {
	p.mu.Lock()
	first := len(p.callStarts) == 0
	p.callStarts = append(p.callStarts, time.Now())
	p.mu.Unlock()

	if first {
		time.Sleep(p.delay)
	}

	return nil
}

func (p *delayedFirstCallPublisher) starts() []time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]time.Time, len(p.callStarts))
	copy(out, p.callStarts)

	return out
}

func TestRevealService_SlowPublishDoesNotHeadOfLineBlockAnotherSlot(t *testing.T) {
	env := newRevealTestEnv(t, 300*time.Millisecond, 10)

	slow := &delayedFirstCallPublisher{delay: 400 * time.Millisecond}
	env.svc.publisher = slow

	sub := env.svc.SubscribeResults(8, false)
	defer sub.Unsubscribe()

	require.NoError(t, env.svc.Start(context.Background()))
	defer env.svc.Stop()

	t0 := time.Now()

	slot1 := phase0.Slot(1) // due ~t0+10ms  (SlotToTime(1) == t0, per newRevealTestEnv)
	slot2 := phase0.Slot(2) // due ~t0+310ms (SlotToTime(2) == t0+300ms)

	env.svc.RequestReveal(revealRequest(slot1, phase0.Root{0x11}))
	env.svc.RequestReveal(&RevealRequest{
		Payload:   newTestPayload(slot2, phase0.Hash32{0xcd}, big.NewInt(1)),
		BlockInfo: &beacon.BlockInfo{Slot: slot2, Root: phase0.Root{0x33}, ParentRoot: phase0.Root{0x44}},
		Transport: payload_builder.BidTransportP2P,
	})

	require.Eventually(t, func() bool {
		return len(slow.starts()) == 2
	}, 2*time.Second, 10*time.Millisecond, "expected both slots to eventually publish")

	starts := slow.starts()
	slot1PublishOffset := starts[0].Sub(t0)
	slot2PublishOffset := starts[1].Sub(t0)
	slot2DueOffset := 310 * time.Millisecond

	t.Logf("slot1 publish started at t0+%v (due t0+10ms, blocks for 400ms)", slot1PublishOffset)
	t.Logf("slot2 publish started at t0+%v (due t0+%v)", slot2PublishOffset, slot2DueOffset)

	// The fix: slot2's publish call must start close to its own due time,
	// not be forced to wait for slot1's 400ms-long publish call to return
	// first. A generous margin (100ms) absorbs scheduling jitter while still
	// failing hard if the old inline, single-goroutine behavior regresses
	// (which would delay slot2 by roughly slot1's full 400ms delay).
	assert.Less(t, slot2PublishOffset, slot2DueOffset+100*time.Millisecond,
		"NM-05 regression: slot2's publish was delayed behind slot1's slow publish call")

	// Both reveals still complete successfully.
	results := map[phase0.Slot]*RevealResult{}

	for range 2 {
		res := waitForResult(t, sub.Channel(), 2*time.Second)
		results[res.Slot] = res
	}

	require.Contains(t, results, slot1)
	require.Contains(t, results, slot2)
	assert.True(t, results[slot1].Success)
	assert.True(t, results[slot2].Success)
}
