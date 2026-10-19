package payload_builder

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/ethpandaops/buildoor/pkg/config"
)

// The testing build is not incremental: it builds once from the list it is
// handed. runLocalBuilds therefore either times a single build to finish just
// before its window ends, from a model of how long a build takes, or builds
// at once and rebuilds whenever new transactions arrived while a build still
// fits before the window ends.

const (
	// timingDecay weighs the previous observations against a new one: the
	// model follows the EL's load over a few dozen builds.
	timingDecay = 0.9
	// timingMinSamples is how many builds the model needs before its own fit
	// replaces the seeds.
	timingMinSamples = 3
	// Seeds until enough builds were observed: a selection served from the
	// EL mempool mirror, a call of ~100 ms plus 4 ms per Mgas, a half-full
	// 60M block.
	seedSelectionMs = 150.0
	seedCallBaseMs  = 100.0
	seedCallPerMgas = 4.0
	seedMgas        = 30.0
	// lateBuildTimeout bounds a build that outlives its window.
	lateBuildTimeout = 5 * time.Second
	// rebuildPollInterval is how often a waiting rebuild checks for new
	// transactions (the mirror itself polls the EL every 500 ms).
	rebuildPollInterval = 25 * time.Millisecond
)

// buildTimingModel estimates one testing build's duration: the selection
// (assembling the list) plus the EL call, which grows linearly with the
// block's gas. The call cost is an exponentially weighted least-squares fit
// of duration over gas, so it adapts to the EL and its load; the estimate
// adds two standard deviations of the fit's recent residuals.
type buildTimingModel struct {
	mu sync.Mutex

	samples     int
	selectionMs float64
	mgas        float64 // recent blocks' gas used, the expected size

	// Weighted sums of the call fit (x = Mgas, y = ms).
	sw, sx, sy, sxx, sxy float64
	residualVar          float64
}

// observe records one finished build.
func (m *buildTimingModel) observe(selection, call time.Duration, gasUsed uint64) {
	selectionMs := float64(selection.Milliseconds())
	callMs := float64(call.Milliseconds())
	mgas := float64(gasUsed) / 1e6

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.samples == 0 {
		m.selectionMs, m.mgas = selectionMs, mgas
	} else {
		m.selectionMs = timingDecay*m.selectionMs + (1-timingDecay)*selectionMs
		m.mgas = timingDecay*m.mgas + (1-timingDecay)*mgas

		residual := callMs - m.callMsLocked(mgas)
		m.residualVar = timingDecay*m.residualVar + (1-timingDecay)*residual*residual
	}

	m.sw = timingDecay*m.sw + 1
	m.sx = timingDecay*m.sx + mgas
	m.sy = timingDecay*m.sy + callMs
	m.sxx = timingDecay*m.sxx + mgas*mgas
	m.sxy = timingDecay*m.sxy + mgas*callMs
	m.samples++
}

// estimate returns the expected duration of the next build plus its spread.
func (m *buildTimingModel) estimate() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.samples < timingMinSamples {
		return msDuration(seedSelectionMs + seedCallBaseMs + seedCallPerMgas*seedMgas)
	}

	return msDuration(m.selectionMs + m.callMsLocked(m.mgas) + 2*math.Sqrt(m.residualVar))
}

// callMsLocked evaluates the call fit at mgas; the seeds stand in while the
// observed gas does not vary enough to fit a slope.
func (m *buildTimingModel) callMsLocked(mgas float64) float64 {
	if m.samples < timingMinSamples {
		return seedCallBaseMs + seedCallPerMgas*mgas
	}

	slope := seedCallPerMgas

	if denom := m.sw*m.sxx - m.sx*m.sx; denom > 1e-6*m.sw*m.sw {
		slope = max(0, (m.sw*m.sxy-m.sx*m.sy)/denom)
	}

	base := max(0, (m.sy-slope*m.sx)/m.sw)

	return base + slope*mgas
}

func msDuration(ms float64) time.Duration {
	return time.Duration(ms * float64(time.Millisecond))
}

// runLocalBuilds runs the target's testing builds inside their window
// [windowStart, windowEnd], independent of the engine build's.
//
// Without a rebuild interval it is one build, started at windowEnd - the
// model's estimate (not before windowStart) so it finishes at the window
// end with the newest transactions.
//
// With one (pool sources only) the first build starts at windowStart, and
// every later one once the pool or the EL mempool got new transactions, at
// least the interval after the previous build and only while a build
// started then is still estimated to finish by windowEnd. No news, no
// rebuild: the block would be the same. A skip ends the loop: it would skip
// again.
func (b *PayloadBuilder) runLocalBuilds(
	ctx context.Context,
	req *BuildRequest,
	prelude *buildPrelude,
	windowStart, windowEnd time.Time,
	picker *targetPicker,
) {
	local := req.Local

	latestStart := func() time.Time {
		return windowEnd.Add(-b.localTiming.estimate())
	}

	rebuild := local.RebuildInterval > 0 && b.txPool != nil &&
		(local.TxSource == config.TxSourceTxPool || local.TxSource == config.TxSourceTxPoolELFill)

	start := latestStart()
	if rebuild || start.Before(windowStart) {
		start = windowStart
	}

	if !sleepUntil(ctx, start) {
		return
	}

	for run := 1; ; run++ {
		var version uint64
		if rebuild {
			version = b.contentVersion()
		}

		started := time.Now()
		outcome := b.runLocalBuild(ctx, req, prelude, windowEnd, run)
		picker.testingRun(outcome)

		if !rebuild || outcome.skipReason != "" || ctx.Err() != nil {
			return
		}

		if !b.awaitRebuild(ctx, started.Add(local.RebuildInterval), latestStart, version) {
			return
		}
	}
}

// awaitRebuild waits until earliest and then for new transactions, as long as
// a build started now would still finish before the window ends.
func (b *PayloadBuilder) awaitRebuild(
	ctx context.Context, earliest time.Time, latestStart func() time.Time, version uint64,
) bool {
	if !sleepUntil(ctx, earliest) {
		return false
	}

	ticker := time.NewTicker(rebuildPollInterval)
	defer ticker.Stop()

	for {
		if time.Now().After(latestStart()) {
			return false
		}

		if b.contentVersion() != version {
			return true
		}

		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

// runLocalBuild runs one testing build and reports it: building when it
// starts, then its own outcome. It is not cut off at the window end: the EL
// finishes it anyway, and its duration is what keeps the model from timing
// the next build too late.
func (b *PayloadBuilder) runLocalBuild(
	ctx context.Context,
	req *BuildRequest,
	prelude *buildPrelude,
	deadline time.Time,
	run int,
) localOutcome {
	started := time.Now()
	b.reportTesting(req, &TestingBuildEvent{Run: run, Status: LocalStatusBuilding, StartedAt: started})

	buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Until(deadline)+lateBuildTimeout)
	defer cancel()

	outcome := b.buildLocal(buildCtx, req.Attrs, prelude, req.Local)

	if outcome.info != nil {
		outcome.info.StartedAt = started
		outcome.info.Run = run
	}

	b.observeLocalTiming(outcome)

	if outcome.payload != nil {
		if err := b.completePayload(buildCtx, req, outcome.payload, BuildRef{Kind: BuildKindTesting, Run: run}); err != nil {
			outcome.payload, outcome.err = nil, err
		}
	}

	event := &TestingBuildEvent{Run: run, StartedAt: started, Info: outcome.info}

	switch {
	case outcome.payload != nil:
		event.Status = LocalStatusReady
		event.Payload = outcome.payload
	case outcome.skipReason != "":
		event.Status = LocalStatusSkipped
		event.SkipReason = outcome.skipReason
	default:
		event.Status = LocalStatusFailed

		if outcome.err != nil {
			event.Error = outcome.err.Error()
		}
	}

	b.reportTesting(req, event)

	return outcome
}

// sleepUntil waits until t; false when ctx ended first.
func sleepUntil(ctx context.Context, t time.Time) bool {
	wait := time.Until(t)
	if wait <= 0 {
		return ctx.Err() == nil
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// observeLocalTiming feeds a successful build's selection and call times to
// the timing model (per attempt: retries repeat the call). A selection that
// started the EL mempool mirror is left out: a one-off full read.
func (b *PayloadBuilder) observeLocalTiming(outcome localOutcome) {
	info := outcome.info
	if outcome.payload == nil || info == nil || info.MirrorStarted ||
		info.SelectedAt.IsZero() || info.StartedAt.IsZero() {
		return
	}

	call := info.BuiltAt.Sub(info.SelectedAt) / time.Duration(max(info.Attempts, 1))
	b.localTiming.observe(info.SelectedAt.Sub(info.StartedAt), call, outcome.payload.ExecutionPayload.GasUsed)
}

// contentVersion changes whenever a testing rebuild could carry something
// new: the pool's content and EL mempool arrivals (Pool.ContentVersion), or a
// new inclusion list.
func (b *PayloadBuilder) contentVersion() uint64 {
	version := b.txPool.ContentVersion()
	if b.ilStore != nil {
		version += b.ilStore.Version()
	}

	return version
}
