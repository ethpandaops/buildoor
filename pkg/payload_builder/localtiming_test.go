package payload_builder

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/txpool"
)

func TestBuildTimingModel(t *testing.T) {
	var m buildTimingModel

	seeded := m.estimate()
	require.Equal(t, msDuration(seedSelectionMs+seedCallBaseMs+seedCallPerMgas*seedMgas), seeded,
		"the seeds stand in before the first builds")

	// Builds of 40 ms selection and a 50 ms + 2 ms/Mgas call, alternating
	// between 10 and 50 Mgas: the fit recovers the line, the expected size
	// follows the recent blocks.
	for i := range 40 {
		mgas := uint64(10 + 40*(i%2))
		m.observe(40*time.Millisecond, time.Duration(50+2*mgas)*time.Millisecond, mgas*1_000_000)
	}

	m.mu.Lock()
	require.InDelta(t, 50+2*40.0, m.callMsLocked(40), 1)
	require.InDelta(t, 50+2*10.0, m.callMsLocked(10), 1)
	mean := 40 + m.callMsLocked(m.mgas)
	m.mu.Unlock()

	// The estimate adds the spread of the early, still converging fit.
	estimate := float64(m.estimate().Milliseconds())
	require.GreaterOrEqual(t, estimate, mean-1)
	require.LessOrEqual(t, estimate, mean+20)
}

// recordedPick is one pick change a reporter saw.
type recordedPick struct {
	payload  *Payload
	fallback bool
	reason   string
}

// recordingReporter collects a target's build events and picks.
type recordingReporter struct {
	mu        sync.Mutex
	engine    []*EngineBuildEvent
	testing   []*TestingBuildEvent
	picks     []recordedPick
	noPayload []error
}

func (r *recordingReporter) reportPick(payload *Payload, fallback bool, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.picks = append(r.picks, recordedPick{payload: payload, fallback: fallback, reason: reason})
}

func (r *recordingReporter) reportNoPayload(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.noPayload = append(r.noPayload, err)
}

func (r *recordingReporter) reportEngine(event *EngineBuildEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.engine = append(r.engine, event)
}

func (r *recordingReporter) reportTesting(event *TestingBuildEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.testing = append(r.testing, event)
}

func (r *recordingReporter) testingEvents() []*TestingBuildEvent {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]*TestingBuildEvent(nil), r.testing...)
}

func testingRequest(attrs *beacon.PayloadAttributesEvent, local *LocalBuildRequest, reporter buildReporter) *BuildRequest {
	return &BuildRequest{Attrs: attrs, Seq: 7, Candidate: chain.CandidateParentFull, Local: local, reporter: reporter}
}

func TestRunLocalBuildsTimesTheSingleBuild(t *testing.T) {
	client := &fakeLocalClient{}
	b, attrs, prelude := newLocalTestBuilder(client)

	// Without a rebuild interval: one build, timed by the seeded estimate
	// (370 ms) to end at the window end.
	windowEnd := time.Now().Add(600 * time.Millisecond)
	reporter := &recordingReporter{}
	req := testingRequest(attrs, &LocalBuildRequest{TxSource: config.TxSourceEmpty}, reporter)

	b.runLocalBuilds(context.Background(), req, prelude, time.Now(), windowEnd, newTargetPicker(req))
	require.Len(t, client.calls, 1)

	events := reporter.testingEvents()
	require.Len(t, events, 2, "building, then the run's outcome")
	require.Equal(t, LocalStatusBuilding, events[0].Status)
	require.Equal(t, LocalStatusReady, events[1].Status)
	require.NotNil(t, events[1].Payload, "the run reports its own payload")
	require.Len(t, reporter.picks, 1, "a testing-only target picks its run at once")
	require.Same(t, events[1].Payload, reporter.picks[0].payload)
	require.Equal(t, 1, events[1].Run)
	require.Equal(t, uint64(7), events[1].Seq)
	require.WithinDuration(t, windowEnd.Add(-370*time.Millisecond), events[1].StartedAt, 60*time.Millisecond)

	payload := events[1].Payload
	require.Equal(t, BuildRef{Kind: BuildKindTesting, Run: 1}, payload.Build)
	require.Equal(t, uint64(7), payload.BuildSeq)
	require.Equal(t, chain.CandidateParentFull, payload.Candidate)
	require.Nil(t, payload.EngineCalls, "a testing payload has no engine calls")
	require.Equal(t, payload.Local.BuiltAt, payload.ReadyAt, "ready when its own build finished")

	// A window opening later than that holds the build back to its start.
	client.calls = nil
	windowStart := time.Now().Add(400 * time.Millisecond)
	reporter = &recordingReporter{}
	req = testingRequest(attrs, &LocalBuildRequest{TxSource: config.TxSourceEmpty}, reporter)

	b.runLocalBuilds(context.Background(), req, prelude, windowStart, windowStart.Add(100*time.Millisecond), newTargetPicker(req))

	events = reporter.testingEvents()
	require.NotEmpty(t, events)
	require.False(t, events[0].StartedAt.Before(windowStart))
}

func TestRunLocalBuildsRebuildsOnNewTransactions(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.TxPool.Enabled = true

	pool := txpool.NewPool(cfg, &fakePoolEL{}, &stubChain{stubChainService{spec: &chain.ChainSpec{SlotsPerEpoch: 32}}}, nil, logrus.New())
	require.NoError(t, pool.Start(context.Background()))

	t.Cleanup(func() { _ = pool.Stop() })

	client := &fakeLocalClient{}
	b, attrs, prelude := newLocalTestBuilder(client)
	b.txPool = pool

	reporter := &recordingReporter{}
	req := testingRequest(attrs, &LocalBuildRequest{
		TxSource:        config.TxSourceTxPool,
		RebuildInterval: 100 * time.Millisecond,
		GasFillPct:      100,
	}, reporter)

	// A transaction arrives after the first build: one rebuild, then quiet.
	go func() {
		time.Sleep(150 * time.Millisecond)

		key, _ := crypto.GenerateKey()
		tx, _ := types.SignNewTx(key, types.LatestSignerForChainID(big.NewInt(1)), &types.DynamicFeeTx{
			ChainID: big.NewInt(1), GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2), Gas: 21000,
		})
		raw, _ := tx.MarshalBinary()
		_, _ = pool.Add(raw)
	}()

	started := time.Now()
	b.runLocalBuilds(context.Background(), req, prelude, started, started.Add(time.Second), newTargetPicker(req))

	require.Len(t, client.calls, 2, "the first build at once, a rebuild for the arrival, none without news")
	require.Empty(t, client.calls[0])
	require.Len(t, client.calls[1], 1)

	var ready []*TestingBuildEvent

	for _, event := range reporter.testingEvents() {
		if event.Status == LocalStatusReady {
			ready = append(ready, event)
		}
	}

	require.Len(t, ready, 2, "every run reports itself")
	require.Equal(t, []int{1, 2}, []int{ready[0].Run, ready[1].Run})
	require.NotSame(t, ready[0].Payload, ready[1].Payload, "each run has its own payload")
	require.Equal(t, 2, ready[1].Payload.Build.Run)
	require.Less(t, ready[0].StartedAt.Sub(started), 50*time.Millisecond, "the first build starts at once")

	require.Len(t, reporter.picks, 2, "each run's different block is picked as it finishes")
	require.Same(t, ready[1].Payload, reporter.picks[1].payload, "the later build wins the tie")
	require.Equal(t, SelectionBetter, reporter.picks[1].reason)
}

// pickerFor returns a picker of a target with the given payload source
// (engine build on) and its reporter.
func pickerFor(source string) (*targetPicker, *recordingReporter) {
	reporter := &recordingReporter{}
	req := &BuildRequest{
		Attrs:    &beacon.PayloadAttributesEvent{ProposalSlot: 5},
		Local:    &LocalBuildRequest{PayloadSource: source, EngineEnabled: true},
		reporter: reporter,
	}

	return newTargetPicker(req), reporter
}

func built(value int64, hash byte, ref BuildRef) *Payload {
	return &Payload{BlockValue: big.NewInt(value), BlockHash: [32]byte{hash}, Build: ref}
}

var (
	engineRef = BuildRef{Kind: BuildKindEngine}
	run1      = BuildRef{Kind: BuildKindTesting, Run: 1}
	run2      = BuildRef{Kind: BuildKindTesting, Run: 2}
)

// TestPickerPicksAsBuildsFinish: the pick follows the payload source over the
// builds finished so far, the moment each one finishes — no build waits for
// another.
func TestPickerPicksAsBuildsFinish(t *testing.T) {
	t.Run("local picks a testing run before the engine build ends", func(t *testing.T) {
		picker, reporter := pickerFor(config.PayloadSourceLocal)

		testing1 := built(5, 0x01, run1)
		picker.testingRun(localOutcome{payload: testing1})
		require.Len(t, reporter.picks, 1)
		require.Same(t, testing1, reporter.picks[0].payload)
		require.Equal(t, SelectionFirst, reporter.picks[0].reason)

		picker.engineEnded(built(50, 0xe1, engineRef), nil)
		require.Len(t, reporter.picks, 1, "the engine payload is no testing payload")
	})

	t.Run("el ignores the testing runs", func(t *testing.T) {
		picker, reporter := pickerFor(config.PayloadSourceEL)

		picker.testingRun(localOutcome{payload: built(50, 0x01, run1)})
		require.Empty(t, reporter.picks)

		engine := built(5, 0xe1, engineRef)
		picker.engineEnded(engine, nil)
		require.Len(t, reporter.picks, 1)
		require.Same(t, engine, reporter.picks[0].payload)
	})

	t.Run("higher_value moves only to a more valuable block", func(t *testing.T) {
		picker, reporter := pickerFor(config.PayloadSourceHigherValue)

		cheap := built(5, 0x01, run1)
		picker.testingRun(localOutcome{payload: cheap})

		engine := built(10, 0xe1, engineRef)
		picker.engineEnded(engine, nil)

		// The same block again, and an equally valuable other block: no change.
		picker.testingRun(localOutcome{payload: built(10, 0xe1, run2)})
		picker.testingRun(localOutcome{payload: built(10, 0x03, BuildRef{Kind: BuildKindTesting, Run: 3})})

		rich := built(20, 0x04, BuildRef{Kind: BuildKindTesting, Run: 4})
		picker.testingRun(localOutcome{payload: rich})

		require.Len(t, reporter.picks, 3)
		require.Same(t, cheap, reporter.picks[0].payload)
		require.Equal(t, SelectionFirst, reporter.picks[0].reason)
		require.Same(t, engine, reporter.picks[1].payload)
		require.Equal(t, SelectionBetter, reporter.picks[1].reason)
		require.Same(t, rich, reporter.picks[2].payload)
	})

	t.Run("local_or_el takes the engine payload until a testing run exists", func(t *testing.T) {
		picker, reporter := pickerFor(config.PayloadSourceLocalOrEL)

		engine := built(10, 0xe1, engineRef)
		picker.engineEnded(engine, nil)

		testing1 := built(5, 0x01, run1)
		picker.testingRun(localOutcome{payload: testing1})

		require.Len(t, reporter.picks, 2)
		require.Same(t, engine, reporter.picks[0].payload)
		require.True(t, reporter.picks[0].fallback)
		require.Same(t, testing1, reporter.picks[1].payload)
		require.False(t, reporter.picks[1].fallback)

		picker.testingEnded()
		require.Empty(t, reporter.noPayload)
	})

	t.Run("no payload once every build ended without one", func(t *testing.T) {
		picker, reporter := pickerFor(config.PayloadSourceLocal)

		picker.engineEnded(built(10, 0xe1, engineRef), nil)
		picker.testingRun(localOutcome{err: errors.New("nonce too low")})
		require.Empty(t, reporter.noPayload, "the testing runs are not over yet")

		picker.testingEnded()
		require.Len(t, reporter.noPayload, 1)
		require.ErrorContains(t, reporter.noPayload[0], "nonce too low")

		picker.testingEnded()
		require.Len(t, reporter.noPayload, 1, "reported once")
	})
}

func TestPayloadCacheKeepsEveryBuiltPayload(t *testing.T) {
	cache := NewPayloadCache(8)
	attrs := &beacon.PayloadAttributesEvent{ProposalSlot: 5, ParentBlockHash: [32]byte{0xaa}}

	engine := &Payload{Attributes: attrs, BlockHash: [32]byte{1}, Build: BuildRef{Kind: BuildKindEngine}}
	testRun := &Payload{Attributes: attrs, BlockHash: [32]byte{2}, Build: BuildRef{Kind: BuildKindTesting, Run: 1}}

	cache.Store(engine)
	cache.Store(testRun)
	require.Nil(t, cache.Get(5), "stored is not selected")
	require.Same(t, engine, cache.GetByBlockHash([32]byte{1}))

	cache.Select(engine)
	require.Same(t, engine, cache.Get(5))

	cache.Select(testRun)
	require.Same(t, testRun, cache.Get(5), "the newer selection feeds the next bid")
	require.Len(t, cache.GetSlotPayloads(5), 1, "one selection per target")
	require.Same(t, engine, cache.GetByBlockHash([32]byte{1}), "a bid on the engine payload still finds it")

	cache.Withdraw(5, []phase0.Hash32{{1}, {2}})
	require.Nil(t, cache.Get(5))
	require.Nil(t, cache.GetByBlockHash([32]byte{1}))

	cache.Store(engine)
	cache.Cleanup(6)
	require.Nil(t, cache.GetByBlockHash([32]byte{1}))
}
