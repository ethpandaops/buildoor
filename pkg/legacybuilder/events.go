// Package legacybuilder implements the Flashbots relay builder API (MEV-Boost compatible).
package legacybuilder

import (
	"time"

	"github.com/attestantio/go-eth2-client/spec/phase0"
)

// BlockSubmissionEvent is emitted when a block is submitted to a relay.
type BlockSubmissionEvent struct {
	Slot           phase0.Slot
	BlockHash      string
	Value          string // wei
	ProposerPubkey string
	RelayURL       string
	Success        bool
	Error          string
	Timestamp      time.Time
}

// LegacyBuilderStats tracks statistics for legacy builder operations.
type LegacyBuilderStats struct {
	BlocksSubmitted    uint64 `json:"blocks_submitted"`
	BlocksAccepted     uint64 `json:"blocks_accepted"`
	SubmissionFailures uint64 `json:"submission_failures"`
	ValidatorsTracked  uint64 `json:"validators_tracked"`
}
