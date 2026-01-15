package epbs

import (
	"time"

	"github.com/attestantio/go-eth2-client/spec/phase0"
)

// ExecutionPayloadBid represents a bid for an execution payload.
type ExecutionPayloadBid struct {
	ParentBlockHash        phase0.Hash32
	ParentBlockRoot        phase0.Root
	BlockHash              phase0.Hash32
	FeeRecipient           [20]byte
	GasLimit               uint64
	BuilderIndex           uint64
	Slot                   phase0.Slot
	Value                  uint64 // Gwei
	ExecutionPayment       uint64 // Gwei
	BlobKZGCommitmentsRoot phase0.Root
}

// TrackedBid represents a bid being tracked for competition analysis.
type TrackedBid struct {
	Bid          *ExecutionPayloadBid
	BuilderIndex uint64
	ReceivedAt   time.Time
	IsOurs       bool
}

// SlotBids holds all bids for a specific slot.
type SlotBids struct {
	Slot       phase0.Slot
	Bids       map[uint64]*TrackedBid // BuilderIndex -> Bid
	HighestBid *TrackedBid
	OurBid     *TrackedBid
	WinningBid *TrackedBid // Set after block inclusion
}

// NewSlotBids creates a new SlotBids instance for the given slot.
func NewSlotBids(slot phase0.Slot) *SlotBids {
	return &SlotBids{
		Slot: slot,
		Bids: make(map[uint64]*TrackedBid, 16),
	}
}
