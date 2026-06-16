package builder

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
)

// BuildSource indicates how a payload was built.
type BuildSource int

const (
	// BuildSourceBlock indicates the payload was built on a parent block.
	BuildSourceBlock BuildSource = iota
	// BuildSourcePayload indicates the payload was built on a parent payload (slot-2 fallback).
	BuildSourcePayload
)

// String returns a string representation of the build source.
func (s BuildSource) String() string {
	switch s {
	case BuildSourceBlock:
		return "block"
	case BuildSourcePayload:
		return "payload"
	default:
		return "unknown"
	}
}

// PayloadReadyEvent is emitted when a new payload is built.
// Payload, BlobsBundle, and ExecutionRequests are stored typed; marshal to JSON only when sending API responses.
type PayloadReadyEvent struct {
	Slot              phase0.Slot
	ParentBlockRoot   phase0.Root
	ParentBlockHash   phase0.Hash32
	BlockHash         phase0.Hash32
	Payload           *engine.ExecutionPayload // Typed execution payload
	BlobsBundle       *engine.BlobsBundle      // Deneb+ blobs bundle (typed)
	ExecutionRequests engine.ExecutionRequests // Electra+ execution requests (typed)
	Timestamp         uint64
	GasLimit          uint64
	PrevRandao        phase0.Root
	FeeRecipient      common.Address
	BlockValue        *big.Int    // MEV value from EL in wei
	BuildSource       BuildSource // How the payload was built
	ReadyAt           time.Time   // When the payload became ready
}

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
