package builder

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/electra"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// PayloadReadyEvent is emitted when a new payload is built. It carries the
// high-level build objects plus the build metadata that isn't part of them.
// Anything derivable from the objects (slot, parent hashes, timestamp, gas
// limit, ...) is read through them rather than duplicated here.
type PayloadReadyEvent struct {
	// Attributes is the payload_attributes event this build was triggered by.
	Attributes *beacon.PayloadAttributesEvent
	// ExecutionPayload is the fork-agnostic beacon execution payload.
	ExecutionPayload *eth2all.ExecutionPayload
	// BlobsBundle is the engine API blobs bundle (Deneb+), nil if none.
	BlobsBundle *engineall.BlobsBundle
	// ExecutionRequests are the parsed execution requests (Electra+).
	ExecutionRequests *electra.ExecutionRequests

	// Metadata not carried by the objects above.
	BlockHash    phase0.Hash32  // block hash after extra-data injection
	FeeRecipient common.Address // resolved proposer fee recipient for the bid
	BlockValue   *big.Int       // EL-reported block value (wei)
	ReadyAt      time.Time      // when the payload became ready
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
