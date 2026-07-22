package p2p_bidder

import (
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestBidTracker(ourBuilderIdx uint64) *BidTracker {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	return NewBidTracker(ourBuilderIdx, log)
}

func newTestBid(slot phase0.Slot, builderIndex, value uint64) *ExecutionPayloadBid {
	return &ExecutionPayloadBid{
		Slot:         slot,
		BuilderIndex: builderIndex,
		Value:        value,
	}
}

func TestBidTracker_TrackBidAndGetHighestBid(t *testing.T) {
	tests := []struct {
		name          string
		ourBuilderIdx uint64
		bids          []*ExecutionPayloadBid
		slot          phase0.Slot
		wantHighest   uint64 // builder index of expected highest bid
		wantValue     uint64
		wantOursIdx   *uint64 // builder index of expected our bid (nil = none)
	}{
		{
			name:          "single competitor bid",
			ourBuilderIdx: 1,
			bids:          []*ExecutionPayloadBid{newTestBid(100, 2, 500)},
			slot:          100,
			wantHighest:   2,
			wantValue:     500,
			wantOursIdx:   nil,
		},
		{
			name:          "our bid tracked and detected",
			ourBuilderIdx: 1,
			bids:          []*ExecutionPayloadBid{newTestBid(100, 1, 700)},
			slot:          100,
			wantHighest:   1,
			wantValue:     700,
			wantOursIdx:   uint64Ptr(1),
		},
		{
			name:          "competitor outbids us",
			ourBuilderIdx: 1,
			bids: []*ExecutionPayloadBid{
				newTestBid(100, 1, 700),
				newTestBid(100, 2, 900),
			},
			slot:        100,
			wantHighest: 2,
			wantValue:   900,
			wantOursIdx: uint64Ptr(1),
		},
		{
			name:          "we outbid competitor",
			ourBuilderIdx: 1,
			bids: []*ExecutionPayloadBid{
				newTestBid(100, 2, 900),
				newTestBid(100, 1, 1000),
			},
			slot:        100,
			wantHighest: 1,
			wantValue:   1000,
			wantOursIdx: uint64Ptr(1),
		},
		{
			name:          "rebid by same builder replaces entry but keeps highest",
			ourBuilderIdx: 1,
			bids: []*ExecutionPayloadBid{
				newTestBid(100, 2, 900),
				newTestBid(100, 2, 400), // lower rebid does not lower the highest bid
			},
			slot:        100,
			wantHighest: 2,
			wantValue:   900,
			wantOursIdx: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := newTestBidTracker(tt.ourBuilderIdx)

			for _, bid := range tt.bids {
				tracker.TrackBid(bid, bid.BuilderIndex == tt.ourBuilderIdx)
			}

			highest := tracker.GetHighestBid(tt.slot)
			require.NotNil(t, highest, "expected a highest bid")
			assert.Equal(t, tt.wantHighest, highest.BuilderIndex, "highest bid builder index")
			assert.Equal(t, tt.wantValue, highest.Bid.Value, "highest bid value")
			assert.Equal(t, tt.wantHighest == tt.ourBuilderIdx, highest.IsOurs, "highest bid is-ours flag")

			ourBid := tracker.GetOurBid(tt.slot)
			if tt.wantOursIdx == nil {
				assert.Nil(t, ourBid, "expected no bid of ours")
			} else {
				require.NotNil(t, ourBid, "expected our bid to be tracked")
				assert.Equal(t, *tt.wantOursIdx, ourBid.BuilderIndex, "our bid builder index")
				assert.True(t, ourBid.IsOurs, "our bid must be flagged as ours")
			}
		})
	}
}

func TestBidTracker_GetHighestCompetitorBid(t *testing.T) {
	tests := []struct {
		name          string
		ourBuilderIdx uint64
		bids          []*ExecutionPayloadBid
		slot          phase0.Slot
		wantValue     uint64
		wantOK        bool
	}{
		{
			name:          "unknown slot",
			ourBuilderIdx: 1,
			slot:          100,
			wantOK:        false,
		},
		{
			name:          "only our bid",
			ourBuilderIdx: 1,
			bids:          []*ExecutionPayloadBid{newTestBid(100, 1, 700)},
			slot:          100,
			wantOK:        false,
		},
		{
			name:          "our bid is highest but excluded",
			ourBuilderIdx: 1,
			bids: []*ExecutionPayloadBid{
				newTestBid(100, 1, 1000),
				newTestBid(100, 2, 500),
			},
			slot:      100,
			wantValue: 500,
			wantOK:    true,
		},
		{
			name:          "highest competitor of several",
			ourBuilderIdx: 1,
			bids: []*ExecutionPayloadBid{
				newTestBid(100, 2, 500),
				newTestBid(100, 3, 900),
				newTestBid(100, 1, 100),
			},
			slot:      100,
			wantValue: 900,
			wantOK:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := newTestBidTracker(tt.ourBuilderIdx)

			for _, bid := range tt.bids {
				tracker.TrackBid(bid, bid.BuilderIndex == tt.ourBuilderIdx)
			}

			value, ok := tracker.GetHighestCompetitorBid(tt.slot, tt.ourBuilderIdx)
			assert.Equal(t, tt.wantOK, ok, "competitor bid known")
			assert.Equal(t, tt.wantValue, value, "highest competitor value")
		})
	}
}

func TestBidTracker_GetHighestBidUnknownSlot(t *testing.T) {
	tracker := newTestBidTracker(1)

	assert.Nil(t, tracker.GetHighestBid(42))
	assert.Nil(t, tracker.GetOurBid(42))
	assert.Nil(t, tracker.GetSlotBids(42))
}

func TestBidTracker_GetSlotBids(t *testing.T) {
	tracker := newTestBidTracker(1)

	tracker.TrackBid(newTestBid(100, 1, 700), true)
	tracker.TrackBid(newTestBid(100, 2, 900), false)
	tracker.TrackBid(newTestBid(101, 3, 100), false)

	slotBids := tracker.GetSlotBids(100)
	require.NotNil(t, slotBids)
	assert.Equal(t, phase0.Slot(100), slotBids.Slot)
	assert.Len(t, slotBids.Bids, 2)
	require.NotNil(t, slotBids.OurBid)
	assert.Equal(t, uint64(1), slotBids.OurBid.BuilderIndex)
	require.NotNil(t, slotBids.HighestBid)
	assert.Equal(t, uint64(2), slotBids.HighestBid.BuilderIndex)
}

func TestBidTracker_Cleanup(t *testing.T) {
	tracker := newTestBidTracker(1)

	tracker.TrackBid(newTestBid(100, 2, 500), false)
	tracker.TrackBid(newTestBid(101, 2, 500), false)
	tracker.TrackBid(newTestBid(102, 2, 500), false)

	tracker.Cleanup(102)

	assert.Nil(t, tracker.GetSlotBids(100))
	assert.Nil(t, tracker.GetSlotBids(101))
	assert.NotNil(t, tracker.GetSlotBids(102))
}

func TestBidTracker_SetBuilderIndex(t *testing.T) {
	tracker := newTestBidTracker(0)

	// Simulate late registration: index becomes known after startup.
	tracker.SetBuilderIndex(7)

	tracker.TrackBid(newTestBid(100, 7, 800), true)

	ourBid := tracker.GetOurBid(100)
	require.NotNil(t, ourBid)
	assert.Equal(t, uint64(7), ourBid.BuilderIndex)
	assert.True(t, ourBid.IsOurs)
}

func uint64Ptr(v uint64) *uint64 {
	return &v
}
