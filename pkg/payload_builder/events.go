package payload_builder

import (
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

// PayloadBuildStartedEvent is emitted when payload building begins for a slot,
// before the build has completed. Subscribers (e.g. the WebUI) use it to render
// the build as in-progress rather than waiting for the payload to be ready.
type PayloadBuildStartedEvent struct {
	Slot      phase0.Slot
	StartedAt time.Time // When the build started
}

// PayloadBuildFailedEvent is emitted when a payload build fails. Subscribers
// (e.g. the WebUI) use it to mark the in-progress build as failed instead of
// leaving it rendered as perpetually building.
type PayloadBuildFailedEvent struct {
	Slot     phase0.Slot
	Error    string    // Failure reason
	FailedAt time.Time // When the build failed
}

// BuildSkippedEvent is emitted when the builder deliberately does not build
// for a slot that has a per-slot action plan or an effectively active
// consumer. Subscribers (e.g. the slot results tracker) use it to record
// explainable skips.
type BuildSkippedEvent struct {
	Slot   phase0.Slot
	Reason string // one of the BuildSkipReason* constants
}
