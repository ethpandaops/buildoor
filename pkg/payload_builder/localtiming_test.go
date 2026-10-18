package payload_builder

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/config"
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

func TestLocalResultsHandsOverTheBest(t *testing.T) {
	built := func(value int64) localOutcome {
		return localOutcome{payload: &Payload{BlockValue: big.NewInt(value)}, info: &LocalBuildInfo{}}
	}

	var results localResults

	_, ok := results.result()
	require.False(t, ok, "nothing finished yet")

	results.add(localOutcome{err: errors.New("refused"), info: &LocalBuildInfo{}})

	outcome, ok := results.result()
	require.True(t, ok)
	require.Error(t, outcome.err, "without a success the latest failure is handed over")

	low, high, tie := built(5), built(9), built(9)
	results.add(low)
	results.add(high)
	results.add(tie)
	results.add(localOutcome{err: errors.New("late refusal"), info: &LocalBuildInfo{}})

	outcome, ok = results.result()
	require.True(t, ok)
	require.Same(t, tie.payload, outcome.payload, "the highest value wins, the later build a tie")
	require.Equal(t, 5, outcome.info.Builds)
}

func TestRunLocalBuildsTimesTheFinalBuild(t *testing.T) {
	client := &fakeLocalClient{}
	b, attrs, prelude := newLocalTestBuilder(client)

	// The seeded estimate (370 ms) and margin put the final build 580 ms out; a 300 ms
	// interval adds builds before it. The fake builds take no time, so the
	// model learns that and moves the final build closer to the deadline.
	deadline := time.Now().Add(time.Second)
	req := &LocalBuildRequest{
		TxSource:        config.TxSourceEmpty,
		RebuildInterval: 300 * time.Millisecond,
		ReadyMargin:     50 * time.Millisecond,
	}

	var results localResults

	b.runLocalBuilds(context.Background(), attrs, prelude, req, deadline, &results)

	require.GreaterOrEqual(t, len(client.calls), 2, "rebuilds every interval")
	require.True(t, time.Now().Before(deadline), "every build ends before the deadline")

	outcome, ok := results.result()
	require.True(t, ok)
	require.NoError(t, outcome.err)
	require.Equal(t, len(client.calls), outcome.info.Builds)
	require.True(t, outcome.info.StartedAt.After(deadline.Add(-500*time.Millisecond)),
		"the handed-over (latest, equal-valued) build is the final one, timed late")

	// Without an interval only the final build runs.
	client.calls = nil

	b.runLocalBuilds(context.Background(), attrs, prelude,
		&LocalBuildRequest{TxSource: config.TxSourceEmpty}, time.Now().Add(500*time.Millisecond), &localResults{})
	require.Len(t, client.calls, 1)
}

func TestCollectLocalStopsAtTheDeadline(t *testing.T) {
	b, _, _ := newLocalTestBuilder(&fakeLocalClient{})

	result := &BuildResult{}
	b.collectLocal(context.Background(), result, &localResults{}, make(chan struct{}), time.Now())
	require.EqualError(t, result.LocalErr, "local build not ready at the hand-over")

	// An earlier build is handed over while a later one still runs.
	results := &localResults{}
	results.add(localOutcome{payload: &Payload{BlockValue: big.NewInt(1)}, info: &LocalBuildInfo{}})

	result = &BuildResult{}
	b.collectLocal(context.Background(), result, results, make(chan struct{}), time.Now())
	require.NoError(t, result.LocalErr)
	require.NotNil(t, result.Local)
}
