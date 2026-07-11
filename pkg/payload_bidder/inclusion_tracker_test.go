package payload_bidder

import (
	"fmt"
	"io"
	"math/big"
	"strings"
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// newHookedLogger returns a logger capturing entries in a test hook while
// discarding output.
func newHookedLogger() (*logrus.Logger, *test.Hook) {
	logger, hook := test.NewNullLogger()
	logger.SetOutput(io.Discard)
	logger.SetLevel(logrus.DebugLevel)

	return logger, hook
}

// hasWarnContaining reports whether the hook captured a warning containing msg.
func hasWarnContaining(hook *test.Hook, msg string) bool {
	for _, entry := range hook.AllEntries() {
		if entry.Level == logrus.WarnLevel && strings.Contains(entry.Message, msg) {
			return true
		}
	}

	return false
}

func TestInclusionTracker_FollowUpOrphanDetection(t *testing.T) {
	tests := []struct {
		name       string
		revealed   bool
		expectWarn bool
	}{
		{name: "revealed payload is confirmed", revealed: true, expectWarn: false},
		{name: "unrevealed payload is orphaned", revealed: false, expectWarn: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, hook := newHookedLogger()
			chainSvc := &stubChainService{currentFork: version.DataVersionGloas}
			builderSvc := newTestBuilderSvc(chainSvc)
			tracker := NewInclusionTracker(nil, chainSvc, builderSvc, nil, nil, logger)

			blockHash := phase0.Hash32{0xab}
			payload := newTestPayload(5, blockHash, big.NewInt(1_000_000_000_000))
			builderSvc.GetPayloadCache().Store(payload)

			// Head block for slot 5 commits to our payload.
			tracker.processBlockInfo(&beacon.BlockInfo{Slot: 5, ExecutionBlockHash: blockHash})
			require.Equal(t, phase0.Slot(5), tracker.lastIncludedSlot)
			require.Equal(t, blockHash, tracker.lastIncludedHash)

			if tt.revealed {
				payload.MarkRevealed(payload_builder.RevealRecord{
					Transport:       payload_builder.BidTransportP2P,
					BeaconBlockRoot: phase0.Root{0x11},
				})
			}

			// Follow-up head block for slot 6 triggers the orphan check.
			tracker.processBlockInfo(&beacon.BlockInfo{Slot: 6, ExecutionBlockHash: phase0.Hash32{0xcd}})

			assert.Equal(t, phase0.Slot(0), tracker.lastIncludedSlot, "follow-up tracking must be cleared")
			assert.Equal(t, phase0.Hash32{}, tracker.lastIncludedHash)
			assert.Equal(t, tt.expectWarn, hasWarnContaining(hook, "NOT revealed"),
				"orphan warning expectation")
		})
	}
}

func TestInclusionTracker_GloasGatingAndInclusion(t *testing.T) {
	tests := []struct {
		name            string
		fork            version.DataVersion
		expectPayment   bool
		expectRevealReq bool
	}{
		{name: "pre-Gloas skips payments and reveal", fork: version.DataVersionElectra},
		{name: "Gloas records payment and requests reveal", fork: version.DataVersionGloas,
			expectPayment: true, expectRevealReq: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, _ := newHookedLogger()
			chainSvc := &stubChainService{currentFork: tt.fork}
			builderSvc := newTestBuilderSvc(chainSvc)
			payments := NewPaymentTracker(chainSvc, logger)

			blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
			require.NoError(t, err)

			// Not started: RequestReveal just queues on the buffered channel.
			cfg := &config.Config{}
			revealSvc := NewRevealService(cfg, NewSigner(blsSigner), &mockEnvelopePublisher{},
				chainSvc, builderSvc, payments, action_plan.NewPlanService(cfg, chainSvc, logger), logger)

			tracker := NewInclusionTracker(nil, chainSvc, builderSvc, revealSvc, payments, logger)
			includedSub := tracker.SubscribeIncluded(4)

			defer includedSub.Unsubscribe()

			blockHash := phase0.Hash32{0xab}
			payload := newTestPayload(7, blockHash, big.NewInt(3_000_000_000_000)) // 3000 gwei
			builderSvc.GetPayloadCache().Store(payload)

			blockInfo := &beacon.BlockInfo{Slot: 7, ExecutionBlockHash: blockHash}
			tracker.processBlockInfo(blockInfo)

			if tt.expectPayment {
				assert.Equal(t, uint64(3000), payments.GetTotalPendingPayments())
			} else {
				assert.Equal(t, uint64(0), payments.GetTotalPendingPayments())
			}

			if tt.expectRevealReq {
				require.Len(t, revealSvc.requests, 1, "reveal must be requested")
				req := <-revealSvc.requests
				assert.Same(t, payload, req.Payload)
				assert.Equal(t, payload_builder.BidTransportP2P, req.Transport)
				assert.Same(t, blockInfo, req.BlockInfo)
			} else {
				assert.Empty(t, revealSvc.requests, "no reveal request pre-Gloas")
			}

			// Inclusion stats and event fire on all forks.
			assert.Equal(t, uint64(1), builderSvc.GetStats().BlocksIncluded)

			select {
			case ev := <-includedSub.Channel():
				assert.Same(t, payload, ev.Payload)
				assert.Equal(t, uint64(3000), ev.BidValueGwei)
				require.NotNil(t, ev.WonBlock, "event must carry the won-block record")
				assert.Equal(t, uint64(7), ev.WonBlock.Slot)
				assert.Equal(t, "3000000000000", ev.WonBlock.ValueWei)
			default:
				t.Fatal("expected a PayloadIncludedEvent")
			}
		})
	}
}

func TestInclusionTracker_BuildWonBlockSource(t *testing.T) {
	tests := []struct {
		name           string
		bids           []payload_builder.BidRecord
		expectedSource string
	}{
		{
			name: "any builder api bid marks source builder_api",
			bids: []payload_builder.BidRecord{
				{Transport: payload_builder.BidTransportBuilderAPI, Value: 1000},
				{Transport: payload_builder.BidTransportP2P, Value: 1000},
			},
			expectedSource: WonBlockSourceBuilderAPI,
		},
		{
			name: "p2p-only bids mark source epbs",
			bids: []payload_builder.BidRecord{
				{Transport: payload_builder.BidTransportP2P, Value: 2000},
			},
			expectedSource: WonBlockSourceEPBS,
		},
		{
			name:           "no bid records default to epbs",
			expectedSource: WonBlockSourceEPBS,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, _ := newHookedLogger()
			chainSvc := &stubChainService{currentFork: version.DataVersionGloas}
			builderSvc := newTestBuilderSvc(chainSvc)
			tracker := NewInclusionTracker(nil, chainSvc, builderSvc, nil, nil, logger)

			blockHash := phase0.Hash32{0xaa}
			payload := newTestPayload(5, blockHash, big.NewInt(1_000_000_000_000))

			for _, bid := range tt.bids {
				payload.AddBid(bid)
			}

			wonBlock := tracker.buildWonBlock(payload, blockHash)

			require.NotNil(t, wonBlock)
			assert.Equal(t, tt.expectedSource, wonBlock.Source)
			assert.Equal(t, uint64(5), wonBlock.Slot)
			assert.Equal(t, fmt.Sprintf("%#x", blockHash), wonBlock.BlockHash)
			assert.Equal(t, "1000000000000", wonBlock.ValueWei)
			assert.Equal(t, "0.000001000000000000", wonBlock.ValueETH)
			assert.NotZero(t, wonBlock.Timestamp)
		})
	}
}

func TestWonBlockCodecRoundTrip(t *testing.T) {
	codec := WonBlockCodec{}

	assert.Equal(t, "12345", codec.EncodeKey(phase0.Slot(12345)))

	slot, err := codec.DecodeKey("12345")
	require.NoError(t, err)
	assert.Equal(t, phase0.Slot(12345), slot)

	_, err = codec.DecodeKey("not-a-slot")
	require.Error(t, err)

	wonBlock := &WonBlock{
		Source:          WonBlockSourceBuilderAPI,
		Slot:            12345,
		BlockHash:       "0xabcdef",
		NumTransactions: 7,
		NumBlobs:        2,
		ValueWei:        "1000000000000",
		ValueETH:        "0.000001000000000000",
		Timestamp:       1700000000000,
	}

	encoded, err := codec.EncodeValue(wonBlock)
	require.NoError(t, err)

	decoded, err := codec.DecodeValue(encoded)
	require.NoError(t, err)
	assert.Equal(t, wonBlock, decoded)

	_, err = codec.EncodeValue(nil)
	require.Error(t, err)

	_, err = codec.DecodeValue([]byte("not json"))
	require.Error(t, err)
}
