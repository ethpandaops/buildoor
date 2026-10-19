package payload_builder

import (
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// A build target is one (slot, parent tuple) the builder runs builds for. Its
// builds — the engine build and the testing runs — each produce their own
// payload, and the selection names which of those payloads (by block hash)
// feeds bids and reveals. The four are reported by separate events and never
// mixed: a build event describes that build and its own payload only, a
// selection event only names a payload some build event already reported.

// Build target statuses (BuildTargetEvent.Status).
const (
	// TargetStatusStarted: the target's builds start.
	TargetStatusStarted = "started"
	// TargetStatusNoPayload: the target's builds all ended without a payload
	// the payload source accepts.
	TargetStatusNoPayload = "no_payload"
	// TargetStatusSuperseded: the target ran from synthesized attributes the
	// beacon node's own contradicted; its payloads were withdrawn.
	TargetStatusSuperseded = "superseded"
)

// Engine build statuses (EngineBuildEvent.Status).
const (
	EngineStatusStarted = "started"
	EngineStatusReady   = "ready"
	EngineStatusFailed  = "failed"
)

// Pick reasons (PayloadSelectedEvent.Reason).
const (
	// SelectionFirst is the target's first pick: the first build whose
	// payload the payload source accepts.
	SelectionFirst = "first"
	// SelectionBetter is a build finishing later whose payload the payload
	// source prefers to the current pick.
	SelectionBetter = "better"
)

// errBuildSuperseded is the recorded reason of a superseded target.
const errBuildSuperseded = "superseded by beacon node attributes"

// BuildTargetEvent reports a build target's lifecycle.
type BuildTargetEvent struct {
	Slot      phase0.Slot
	Candidate chain.CandidateKey // "" = unclassified tuple
	// Seq identifies the target across all its events and orders the
	// targets of one slot: a superseded target's late events carry a lower
	// seq than its replacement's. Monotonic per process.
	Seq    uint64
	Status string
	// Attributes are the attributes the target builds from (started).
	Attributes *beacon.PayloadAttributesEvent
	// Engine / Testing tell which builds run (started).
	Engine  bool
	Testing bool
	// TestingSkipReason is why no testing build runs although the slot asked
	// for one (started).
	TestingSkipReason string
	// TxSource / PayloadSource are the testing request's (started).
	TxSource      string
	PayloadSource string
	Error         string
	At            time.Time
}

// EngineBuildEvent reports the target's engine build: started at its
// forkchoiceUpdated, ready or failed when getPayload returned.
type EngineBuildEvent struct {
	Slot      phase0.Slot
	Candidate chain.CandidateKey
	Seq       uint64
	Status    string
	StartedAt time.Time
	// Calls times the forkchoiceUpdated and getPayload calls (ready).
	Calls *EngineCallTimes
	// Payload is the engine build's own payload (ready).
	Payload *Payload
	Error   string
	At      time.Time
}

// TestingBuildEvent reports one testing build run: building when it starts,
// then ready, failed or skipped the moment it ended.
type TestingBuildEvent struct {
	Slot      phase0.Slot
	Candidate chain.CandidateKey
	Seq       uint64
	Run       int
	Status    string // LocalStatus*
	TxSource  string
	// Payload is the run's own payload (ready).
	Payload *Payload
	// Info describes the run's transaction assembly (ready, failed).
	Info       *LocalBuildInfo
	SkipReason string
	Error      string
	StartedAt  time.Time
	At         time.Time
}

// PayloadSelectedEvent reports a change of the target's pick: the payload
// the cache hands to the consumers from now on (the p2p bidder's next tick,
// the next Builder API request). Which payload a consumer used is its own
// record (a bid's block hash).
type PayloadSelectedEvent struct {
	Slot      phase0.Slot
	Candidate chain.CandidateKey
	Seq       uint64
	// Build is the build that produced the selected payload.
	Build     BuildRef
	BlockHash phase0.Hash32
	Reason    string
	// Fallback marks an engine payload selected because the testing payload
	// the payload source asked for did not exist.
	Fallback bool
	// Payload is the selected payload (in-process consumers; the same object
	// its build event carried).
	Payload *Payload
	At      time.Time
}

// BuildEvent is one of a build target's events: *BuildTargetEvent,
// *EngineBuildEvent, *TestingBuildEvent or *PayloadSelectedEvent. They travel
// on one stream (Service.SubscribeBuildEvents) so a consumer sees each
// target's events in the order they happened: a payload's build event
// before its selection, everything before the superseded report.
type BuildEvent interface {
	buildEvent()
}

func (*BuildTargetEvent) buildEvent()     {}
func (*EngineBuildEvent) buildEvent()     {}
func (*TestingBuildEvent) buildEvent()    {}
func (*PayloadSelectedEvent) buildEvent() {}

// buildReporter receives a target's build events and pick changes the
// moment they happen.
type buildReporter interface {
	reportEngine(event *EngineBuildEvent)
	reportTesting(event *TestingBuildEvent)
	reportPick(payload *Payload, fallback bool, reason string)
	reportNoPayload(err error)
}

// BuildSkippedEvent is emitted when the builder deliberately does not build
// for a slot that has a per-slot action plan or an effectively active
// consumer. Subscribers (e.g. the slot results tracker) use it to record
// explainable skips.
type BuildSkippedEvent struct {
	Slot   phase0.Slot
	Reason string // one of the BuildSkipReason* constants
}
