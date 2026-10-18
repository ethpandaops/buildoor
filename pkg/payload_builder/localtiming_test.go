package payload_builder

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
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

func TestLocalResultsHandsOverTheBest(t *testing.T) {
	built := func(value int64) localOutcome {
		return localOutcome{payload: &Payload{BlockValue: big.NewInt(value)}, info: &LocalBuildInfo{}}
	}

	var results localResults

	_, ok := results.result()
	require.False(t, ok, "nothing finished yet")

	results.add(localOutcome{err: errors.New("refused"), info: &LocalBuildInfo{}}, LocalBuildRun{Run: 1})

	outcome, ok := results.result()
	require.True(t, ok)
	require.Error(t, outcome.err, "without a success the latest failure is handed over")

	low, high, tie := built(5), built(9), built(9)
	results.add(low, LocalBuildRun{Run: 2})
	results.add(high, LocalBuildRun{Run: 3})
	results.add(tie, LocalBuildRun{Run: 4})
	results.add(localOutcome{err: errors.New("late refusal"), info: &LocalBuildInfo{}}, LocalBuildRun{Run: 5})

	outcome, ok = results.result()
	require.True(t, ok)
	require.Same(t, tie.payload, outcome.payload, "the highest value wins, the later build a tie")
	require.Equal(t, 5, outcome.info.Builds)
	require.Len(t, outcome.info.Runs, 5, "every build is summarized")
}

func TestRunLocalBuildsTimesTheSingleBuild(t *testing.T) {
	client := &fakeLocalClient{}
	b, attrs, prelude := newLocalTestBuilder(client)

	// Without a rebuild interval: one build, timed by the seeded estimate
	// (370 ms) and the margin to end before the deadline.
	deadline := time.Now().Add(600 * time.Millisecond)
	req := &LocalBuildRequest{TxSource: config.TxSourceEmpty, ReadyMargin: 50 * time.Millisecond}

	var results localResults

	b.runLocalBuilds(context.Background(), attrs, prelude, req, deadline, &results)
	require.Len(t, client.calls, 1)

	outcome, ok := results.result()
	require.True(t, ok)
	require.NoError(t, outcome.err)
	require.Equal(t, 1, outcome.info.Run)
	require.WithinDuration(t, deadline.Add(-420*time.Millisecond), outcome.info.StartedAt, 60*time.Millisecond)
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

	req := &LocalBuildRequest{
		TxSource:        config.TxSourceTxPool,
		RebuildInterval: 100 * time.Millisecond,
		ReadyMargin:     50 * time.Millisecond,
		GasFillPct:      100,
	}

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

	var results localResults

	started := time.Now()
	b.runLocalBuilds(context.Background(), attrs, prelude, req, started.Add(time.Second), &results)

	require.Len(t, client.calls, 2, "the first build at once, a rebuild for the arrival, none without news")
	require.Empty(t, client.calls[0])
	require.Len(t, client.calls[1], 1)

	outcome, ok := results.result()
	require.True(t, ok)
	require.Equal(t, 2, outcome.info.Run, "the later build wins the tie")
	require.Len(t, outcome.info.Runs, 2)
	require.Less(t, outcome.info.Runs[0].StartedAt.Sub(started), 50*time.Millisecond, "the first build starts at once")
}

func TestCollectLocalStopsAtTheDeadline(t *testing.T) {
	b, _, _ := newLocalTestBuilder(&fakeLocalClient{})

	result := &BuildResult{}
	b.collectLocal(context.Background(), result, &localResults{}, make(chan struct{}), time.Now())
	require.EqualError(t, result.LocalErr, "local build not ready at the hand-over")

	// An earlier build is handed over while a later one still runs.
	results := &localResults{}
	results.add(localOutcome{payload: &Payload{BlockValue: big.NewInt(1)}, info: &LocalBuildInfo{}}, LocalBuildRun{Run: 1})

	result = &BuildResult{}
	b.collectLocal(context.Background(), result, results, make(chan struct{}), time.Now())
	require.NoError(t, result.LocalErr)
	require.NotNil(t, result.Local)
}
