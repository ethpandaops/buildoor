package payload_builder

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// The testing build is not incremental: it builds once from the list it is
// handed. runLocalBuilds therefore either times a single build to finish just
// before the hand-over, from a model of how long a build takes, or builds at
// once and rebuilds whenever new transactions arrived while a build still
// fits before the hand-over.

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
	// handoverGrace is how long past the deadline the hand-over waits for a
	// final build still in flight (the engine's getPayload takes a moment
	// too).
	handoverGrace = 50 * time.Millisecond
	// lateBuildTimeout bounds a build that outlives the hand-over.
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

// localResults collects a target's testing builds: the hand-over takes the
// best one, and every new best one after it can upgrade the published
// payload (see followLocal).
type localResults struct {
	mu   sync.Mutex
	best *localOutcome // highest block value of the successful builds
	last *localOutcome // the latest build, for the reason when none succeeded
	runs []LocalBuildRun

	// improved signals a new best build (buffered, coalescing).
	improved chan struct{}
}

func newLocalResults() *localResults {
	return &localResults{improved: make(chan struct{}, 1)}
}

func (r *localResults) add(outcome localOutcome, run LocalBuildRun) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.runs = append(r.runs, run)
	r.last = &outcome

	if outcome.payload == nil {
		return
	}

	// The later build wins a tie: it saw the newer transactions.
	if r.best == nil || blockValue(outcome.payload).Cmp(blockValue(r.best.payload)) >= 0 {
		r.best = &outcome

		select {
		case r.improved <- struct{}{}:
		default:
		}
	}
}

// result returns the outcome to hand over: the best build, else the latest
// failure or skip; ok is false when no build finished. Its info is a copy
// carrying the summary of every build so far (the original stays with the
// payload, read concurrently).
func (r *localResults) result() (localOutcome, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	chosen := r.best
	if chosen == nil {
		chosen = r.last
	}

	if chosen == nil {
		return localOutcome{}, false
	}

	outcome := *chosen

	if chosen.info != nil {
		info := *chosen.info
		info.Builds = len(r.runs)
		info.Runs = append([]LocalBuildRun(nil), r.runs...)
		outcome.info = &info
	}

	return outcome, true
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
	attrs *beacon.PayloadAttributesEvent,
	prelude *buildPrelude,
	req *LocalBuildRequest,
	windowStart, windowEnd time.Time,
	results *localResults,
) {
	latestStart := func() time.Time {
		return windowEnd.Add(-b.localTiming.estimate())
	}

	rebuild := req.RebuildInterval > 0 && b.txPool != nil &&
		(req.TxSource == config.TxSourceTxPool || req.TxSource == config.TxSourceTxPoolELFill)

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

		outcome, summary := b.runLocalBuild(ctx, attrs, prelude, req, windowEnd, run)
		results.add(outcome, summary)

		if !rebuild || outcome.skipReason != "" || ctx.Err() != nil {
			return
		}

		if !b.awaitRebuild(ctx, summary.StartedAt.Add(req.RebuildInterval), latestStart, version) {
			return
		}
	}
}

// awaitRebuild waits until earliest and then for new transactions, as long as
// a build started now would still finish before the hand-over.
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

// runLocalBuild runs one testing build. It is not cut off at the hand-over:
// the EL finishes it anyway, and its duration is what keeps the model from
// timing the next build too late.
func (b *PayloadBuilder) runLocalBuild(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
	prelude *buildPrelude,
	req *LocalBuildRequest,
	deadline time.Time,
	run int,
) (localOutcome, LocalBuildRun) {
	started := time.Now()
	b.fireLocalProgress(attrs, req, run, started, nil)

	buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Until(deadline)+lateBuildTimeout)
	outcome := b.buildLocal(buildCtx, attrs, prelude, req)

	cancel()

	if outcome.info != nil {
		outcome.info.StartedAt = started
		outcome.info.Run = run
	}

	b.observeLocalTiming(outcome)
	b.fireLocalProgress(attrs, req, run, started, &outcome)

	return outcome, summarizeRun(run, started, outcome)
}

// summarizeRun condenses one build for the target's run list.
func summarizeRun(run int, started time.Time, outcome localOutcome) LocalBuildRun {
	summary := LocalBuildRun{Run: run, StartedAt: started, At: time.Now()}

	if outcome.info != nil {
		summary.SelectedAt = outcome.info.SelectedAt
	}

	switch {
	case outcome.payload != nil:
		summary.Status = LocalStatusReady
		summary.At = outcome.payload.ReadyAt
		summary.BlockValueWei = blockValue(outcome.payload).String()

		if ep := outcome.payload.ExecutionPayload; ep != nil {
			summary.NumTransactions = len(ep.Transactions)
			summary.GasUsed = ep.GasUsed
		}
	case outcome.skipReason != "":
		summary.Status = LocalStatusSkipped
		summary.Error = outcome.skipReason
	default:
		summary.Status = LocalStatusFailed

		if outcome.err != nil {
			summary.Error = outcome.err.Error()
		}
	}

	return summary
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

// followLocal publishes the testing builds that finish after the hand-over:
// whenever a new best build makes the payload source's pick (selectPayload)
// a different payload than the one published last, the new pick goes out on
// upgrades, so the next bid or getHeader uses it. The picks only improve: the
// best testing build is the highest-valued one so far. When the builds are
// done a final report follows (Payload = the pick already published, Local
// and LocalInfo = the outcome with every run), then upgrades closes.
func (b *PayloadBuilder) followLocal(
	ctx context.Context,
	handedOver BuildResult,
	req *LocalBuildRequest,
	results *localResults,
	done <-chan struct{},
	upgrades chan<- *BuildResult,
) {
	defer close(upgrades)

	current := handedOver.Payload

	publish := func() {
		outcome, ok := results.result()
		if !ok || outcome.payload == nil {
			return
		}

		next := handedOver
		next.Local, next.LocalInfo, next.LocalErr, next.LocalSkipReason = outcome.payload, outcome.info, nil, ""
		next.Payload, next.Source, next.Fallback = nil, "", false

		picked, err := b.selectPayload(&next, req)
		if err != nil || picked.Payload == nil || (current != nil && picked.Payload.BlockHash == current.BlockHash) {
			return
		}

		if picked.Source == SourceLocal {
			picked.Payload.ReadyAt = time.Now()

			if handedOver.EL != nil {
				picked.Payload.EngineCalls = handedOver.EL.EngineCalls
			}
		}

		// Not bound to ctx: it ends with the last build, whose pick must still
		// go out. The consumer drops picks of a superseded build itself.
		timer := time.NewTimer(lateBuildTimeout)
		defer timer.Stop()

		select {
		case upgrades <- picked:
			current = picked.Payload
		case <-timer.C:
		}
	}

	report := func() {
		final := handedOver
		final.Payload = current

		if outcome, ok := results.result(); ok {
			final.Local, final.LocalInfo, final.LocalErr, final.LocalSkipReason =
				outcome.payload, outcome.info, outcome.err, outcome.skipReason
		}

		timer := time.NewTimer(lateBuildTimeout)
		defer timer.Stop()

		select {
		case upgrades <- &final:
		case <-timer.C:
		}
	}

	for {
		select {
		case <-results.improved:
			publish()
		case <-done:
			// The last build may have finished right before the loop did.
			select {
			case <-results.improved:
				publish()
			default:
			}

			report()

			return
		case <-ctx.Done():
			report()

			return
		}
	}
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
