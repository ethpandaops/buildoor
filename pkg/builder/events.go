package builder

import (
	"encoding/json"
	"time"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/ethereum/go-ethereum/common"

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

// PayloadReadyEvent is emitted when a new payload is built.
type PayloadReadyEvent struct {
	Slot             phase0.Slot
	ParentBlockRoot  phase0.Root
	ParentBlockHash  phase0.Hash32
	BlockHash        phase0.Hash32
	Payload          json.RawMessage
	BlobsBundle      json.RawMessage
	Timestamp        uint64
	GasLimit         uint64
	PrevRandao       phase0.Root
	FeeRecipient     common.Address
	BlockValue       uint64      // MEV value from EL in wei
	BuildSource      BuildSource // How the payload was built
	BuildRequestedAt time.Time   // When the FCU was sent to request the build
	ReadyAt          time.Time   // When the payload became ready
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
