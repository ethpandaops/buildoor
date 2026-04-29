package builder

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
	"github.com/ethpandaops/buildoor/pkg/utils"
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

// PayloadVariant indicates which assumption a payload was built under.
//
// FULL is built on top of the parent slot's bid block_hash — i.e. assuming
// the prior slot's payload was published. EMPTY is built on top of the parent
// slot's bid parent_block_hash — i.e. assuming the prior slot was missed and
// we should build on the grandparent EL block.
type PayloadVariant int

const (
	// PayloadVariantFull is built on the bid's block_hash for the parent block root.
	PayloadVariantFull PayloadVariant = iota
	// PayloadVariantEmpty is built on the bid's parent_block_hash for the parent block root.
	PayloadVariantEmpty
)

// String returns a string representation of the payload variant.
func (v PayloadVariant) String() string {
	switch v {
	case PayloadVariantFull:
		return "full"
	case PayloadVariantEmpty:
		return "empty"
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
	BlockValue        uint64         // MEV value from EL in wei
	BuildSource       BuildSource    // How the payload was built
	Variant           PayloadVariant // FULL (bid.block_hash head) or EMPTY (bid.parent_block_hash head)
	ReadyAt           time.Time      // When the payload became ready
}

// PayloadReadyDispatcher dispatches payload ready events to subscribers.
type PayloadReadyDispatcher struct {
	*utils.Dispatcher[*PayloadReadyEvent]
}

// NewPayloadReadyDispatcher creates a new payload ready dispatcher.
func NewPayloadReadyDispatcher() *PayloadReadyDispatcher {
	return &PayloadReadyDispatcher{
		Dispatcher: &utils.Dispatcher[*PayloadReadyEvent]{},
	}
}

// SubscribePayloadReady subscribes to payload ready events.
func (d *PayloadReadyDispatcher) SubscribePayloadReady(
	capacity int,
) *utils.Subscription[*PayloadReadyEvent] {
	return d.Subscribe(capacity, false)
}
