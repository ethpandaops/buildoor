package payload_builder

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ethpandaops/buildoor/pkg/config"
)

// A target's pick is the payload the cache hands out for it — to the p2p
// bidder's next tick, the next Builder API request — at any moment. It is
// re-evaluated whenever one of the target's builds finishes, by the payload
// source's rule over the payloads that exist then; there is no hand-over
// moment the other builds wait for. Which payload a consumer actually used
// is the consumer's own record (a bid commits to a block hash).

// targetPicker keeps one target's pick across its builds.
type targetPicker struct {
	mu  sync.Mutex
	req *BuildRequest
	// source is the payload source the pick follows (local when the target
	// runs no engine build).
	source string

	engineDone bool
	engine     *Payload
	engineErr  error

	testingDone bool
	best        *Payload      // highest block value of the testing runs; a tie goes to the later run
	last        *localOutcome // the latest run, for the reason when none succeeded

	current *Payload
	ended   bool
}

func newTargetPicker(req *BuildRequest) *targetPicker {
	source := config.PayloadSourceEL
	if selection := req.Local.selectionRequest(); selection != nil {
		source = config.NormalizedPayloadSource(selection.PayloadSource, config.PayloadSourceEL)
	}

	return &targetPicker{
		req:         req,
		source:      source,
		engineDone:  !req.Local.RunEL(),
		testingDone: req.Local == nil,
	}
}

// engineEnded records the engine build's outcome (after its build event).
func (p *targetPicker) engineEnded(payload *Payload, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.engineDone = true
	p.engine, p.engineErr = payload, err

	p.repickLocked()
	p.endLocked()
}

// testingRun records one testing run's outcome (after its build event).
func (p *targetPicker) testingRun(outcome localOutcome) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.last = &outcome

	if outcome.payload != nil && (p.best == nil || blockValue(outcome.payload).Cmp(blockValue(p.best)) >= 0) {
		p.best = outcome.payload
	}

	p.repickLocked()
}

// testingEnded records that the target's testing runs are over.
func (p *targetPicker) testingEnded() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.testingDone = true
	p.endLocked()
}

// state returns the builds as they stand.
func (p *targetPicker) stateLocked() *buildState {
	state := &buildState{EL: p.engine, ELErr: p.engineErr, Local: p.best}

	if p.best == nil && p.last != nil {
		state.LocalErr, state.LocalSkipReason = p.last.err, p.last.skipReason
	}

	if p.best == nil && p.last == nil && p.testingDone && p.req.Local != nil {
		state.LocalErr = errors.New("no testing build ran")
	}

	return state
}

// repickLocked applies the payload source's rule to the builds finished so
// far and reports a changed pick. A pick only changes to another block: a
// run rebuilding the picked block exactly is the same payload, and
// higher_value moves only to a strictly more valuable one.
func (p *targetPicker) repickLocked() {
	pick, err := selectPayload(p.stateLocked(), p.req.Local.selectionRequest())
	if err != nil || pick == nil {
		return
	}

	reason := SelectionFirst

	if p.current != nil {
		if pick.Payload.BlockHash == p.current.BlockHash {
			return
		}

		if p.source == config.PayloadSourceHigherValue &&
			blockValue(pick.Payload).Cmp(blockValue(p.current)) <= 0 {
			return
		}

		reason = SelectionBetter
	}

	p.current = pick.Payload

	if p.req.reporter != nil {
		p.req.reporter.reportPick(pick.Payload, pick.Fallback, reason)
	}
}

// endLocked reports a target whose builds all ended without a pick.
func (p *targetPicker) endLocked() {
	if p.ended || !p.engineDone || !p.testingDone {
		return
	}

	p.ended = true

	if p.current != nil {
		return
	}

	_, err := selectPayload(p.stateLocked(), p.req.Local.selectionRequest())
	if err == nil {
		err = fmt.Errorf("no payload to pick")
	}

	if p.req.reporter != nil {
		p.req.reporter.reportNoPayload(err)
	}
}
