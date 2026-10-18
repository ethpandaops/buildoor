package payload_builder

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// The testing build is not incremental: it builds once from the list it is
// handed. runLocalBuilds therefore times its final build to finish just before
// the hand-over, from a model of how long a build takes, and optionally runs
// earlier builds as a fallback.

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

// localResults collects a target's testing builds; the hand-over takes the
// best one at the deadline.
type localResults struct {
	mu     sync.Mutex
	best   *localOutcome // highest block value of the successful builds
	last   *localOutcome // the latest build, for the reason when none succeeded
	builds int
}

func (r *localResults) add(outcome localOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.builds++
	r.last = &outcome

	if outcome.payload == nil {
		return
	}

	// The later build wins a tie: it saw the newer transactions.
	if r.best == nil || blockValue(outcome.payload).Cmp(blockValue(r.best.payload)) >= 0 {
		r.best = &outcome
	}
}

// result returns the outcome handed over: the best build, else the latest
// failure or skip; ok is false when no build finished.
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

	if chosen.info != nil {
		chosen.info.Builds = r.builds
	}

	return *chosen, true
}

// runLocalBuilds runs the target's testing builds. The final build starts at
// deadline - ReadyMargin - the model's estimate, so it finishes right before
// the hand-over with the newest transactions; with a rebuild interval earlier
// builds run every interval before it (from now on), each a fallback should a
// later one fail or overrun. A skip ends the loop: it would skip again.
func (b *PayloadBuilder) runLocalBuilds(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
	prelude *buildPrelude,
	req *LocalBuildRequest,
	deadline time.Time,
	results *localResults,
) {
	latestStart := func() time.Time {
		return deadline.Add(-req.ReadyMargin - b.localTiming.estimate())
	}

	start := latestStart()

	if interval := req.RebuildInterval; interval > 0 {
		if ahead := time.Until(start); ahead > 0 {
			start = start.Add(-ahead.Truncate(interval))
		}
	}

	for {
		if wait := time.Until(start); wait > 0 {
			timer := time.NewTimer(wait)

			select {
			case <-ctx.Done():
				timer.Stop()

				return
			case <-timer.C:
			}
		}

		started := time.Now()
		b.fireLocalProgress(attrs, req, started, nil)

		// A build is not cut off at the hand-over: the EL finishes it
		// anyway, and its duration is what keeps the model from timing the
		// next final build too late.
		buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Until(deadline)+lateBuildTimeout)
		outcome := b.buildLocal(buildCtx, attrs, prelude, req)

		cancel()

		if outcome.info != nil {
			outcome.info.StartedAt = started
		}

		b.observeLocalTiming(outcome)
		b.fireLocalProgress(attrs, req, started, &outcome)
		results.add(outcome)

		if req.RebuildInterval == 0 || outcome.skipReason != "" || ctx.Err() != nil {
			return
		}

		// The grid stays aligned to the final build; a model that moved
		// shifts the final build, never past its latest start.
		next := start.Add(req.RebuildInterval)
		if latest := latestStart(); next.After(latest) {
			if !latest.After(start.Add(req.RebuildInterval / 2)) {
				return
			}

			next = latest
		}

		start = next
	}
}

// observeLocalTiming feeds a successful build's selection and call times to
// the timing model (per attempt: retries repeat the call).
func (b *PayloadBuilder) observeLocalTiming(outcome localOutcome) {
	info := outcome.info
	if outcome.payload == nil || info == nil || info.SelectedAt.IsZero() || info.StartedAt.IsZero() {
		return
	}

	call := info.BuiltAt.Sub(info.SelectedAt) / time.Duration(max(info.Attempts, 1))
	b.localTiming.observe(info.SelectedAt.Sub(info.StartedAt), call, outcome.payload.ExecutionPayload.GasUsed)
}
