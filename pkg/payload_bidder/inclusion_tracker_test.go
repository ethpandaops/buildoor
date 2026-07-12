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

// drainVerdicts reads all immediately available payload status events.
func drainVerdicts(ch <-chan *PayloadStatusEvent) []*PayloadStatusEvent {
	events := make([]*PayloadStatusEvent, 0, 4)

	for {
		select {
		case ev := <-ch:
			events = append(events, ev)
		default:
			return events
		}
	}
}

func TestInclusionTracker_PayloadVerdicts(t *testing.T) {
	ourHash := phase0.Hash32{0xab}
	ourRoot := phase0.Root{0x05}

	// Head blocks: our win at slot 5, then a follow-up at slot 6 built either
	// on our payload, on an older execution block, or on a competing slot-5
	// block (orphaning ours).
	winBlock := &beacon.BlockInfo{Slot: 5, Root: ourRoot, ExecutionBlockHash: ourHash}
	competing5 := &beacon.BlockInfo{
		Slot: 5, Root: phase0.Root{0x55}, ExecutionBlockHash: phase0.Hash32{0x55},
	}

	tests := []struct {
		name        string
		followUp    *beacon.BlockInfo
		wantVerdict PayloadVerdict
	}{
		{
			name: "next block builds on our payload",
			followUp: &beacon.BlockInfo{
				Slot: 6, Root: phase0.Root{0x06}, ParentRoot: ourRoot,
				FinalitySafeExecutionBlockHash: ourHash,
			},
			wantVerdict: PayloadVerdictCanonical,
		},
		{
			name: "next block builds on an older execution block",
			followUp: &beacon.BlockInfo{
				Slot: 6, Root: phase0.Root{0x06}, ParentRoot: ourRoot,
				FinalitySafeExecutionBlockHash: phase0.Hash32{0x99},
			},
			wantVerdict: PayloadVerdictMissed,
		},
		{
			name: "won block reorged out",
			followUp: &beacon.BlockInfo{
				Slot: 6, Root: phase0.Root{0x06}, ParentRoot: competing5.Root,
				FinalitySafeExecutionBlockHash: competing5.ExecutionBlockHash,
			},
			wantVerdict: PayloadVerdictOrphaned,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, _ := newHookedLogger()
			chainSvc := &stubChainService{currentFork: version.DataVersionGloas}
			builderSvc := newTestBuilderSvc(chainSvc)
			tracker := NewInclusionTracker(nil, chainSvc, builderSvc, nil, nil, logger)

			statusSub := tracker.SubscribePayloadStatus(4, false)
			defer statusSub.Unsubscribe()

			payload := newTestPayload(5, ourHash, big.NewInt(1_000_000_000_000))
			builderSvc.GetPayloadCache().Store(payload)

			tracker.processBlockInfo(winBlock)
			require.Contains(t, tracker.trackedWins, phase0.Slot(5), "win must be tracked")
			// Seed the competing branch into the ancestry cache (as a head
			// event would).
			tracker.blockCache[competing5.Root] = competing5

			tracker.processBlockInfo(tt.followUp)

			events := drainVerdicts(statusSub.Channel())
			require.Len(t, events, 1, "first resolution must fire exactly one verdict")
			assert.Equal(t, phase0.Slot(5), events[0].Slot)
			assert.Equal(t, tt.wantVerdict, events[0].Verdict)
			assert.Equal(t, phase0.Slot(6), events[0].NextBlockSlot)

			// The same head again must not re-fire an unchanged verdict.
			tracker.processBlockInfo(tt.followUp)
			assert.Empty(t, drainVerdicts(statusSub.Channel()), "unchanged verdict must not re-fire")
		})
	}
}

func TestInclusionTracker_ReorgRevisesVerdict(t *testing.T) {
	logger, hook := newHookedLogger()
	chainSvc := &stubChainService{currentFork: version.DataVersionGloas}
	builderSvc := newTestBuilderSvc(chainSvc)
	tracker := NewInclusionTracker(nil, chainSvc, builderSvc, nil, nil, logger)

	statusSub := tracker.SubscribePayloadStatus(8, false)
	defer statusSub.Unsubscribe()

	ourHash := phase0.Hash32{0xab}
	ourRoot := phase0.Root{0x05}
	payload := newTestPayload(5, ourHash, big.NewInt(1_000_000_000_000))
	builderSvc.GetPayloadCache().Store(payload)

	// Win at slot 5, canonical follow-up at slot 6.
	tracker.processBlockInfo(&beacon.BlockInfo{Slot: 5, Root: ourRoot, ExecutionBlockHash: ourHash})

	onChain6 := &beacon.BlockInfo{
		Slot: 6, Root: phase0.Root{0x06}, ParentRoot: ourRoot,
		FinalitySafeExecutionBlockHash: ourHash,
	}
	tracker.processBlockInfo(onChain6)

	// Reorg: a competing slot-5 block and a slot-6 head on top of it.
	competing5 := &beacon.BlockInfo{
		Slot: 5, Root: phase0.Root{0x55}, ExecutionBlockHash: phase0.Hash32{0x55},
	}
	tracker.processBlockInfo(competing5)
	tracker.processBlockInfo(&beacon.BlockInfo{
		Slot: 6, Root: phase0.Root{0x66}, ParentRoot: competing5.Root,
		FinalitySafeExecutionBlockHash: competing5.ExecutionBlockHash,
	})

	// Reorg back: slot 7 head descending from the original slot-6 block.
	tracker.processBlockInfo(&beacon.BlockInfo{
		Slot: 7, Root: phase0.Root{0x07}, ParentRoot: onChain6.Root,
		FinalitySafeExecutionBlockHash: phase0.Hash32{0x06},
	})

	events := drainVerdicts(statusSub.Channel())
	require.Len(t, events, 3, "each verdict change must fire once")
	assert.Equal(t, PayloadVerdictCanonical, events[0].Verdict)
	assert.Equal(t, PayloadVerdictOrphaned, events[1].Verdict)
	assert.Equal(t, PayloadVerdictCanonical, events[2].Verdict)

	// The unrevealed payment warning fires with the first verdict only.
	assert.True(t, hasWarnContaining(hook, "NOT revealed"))
}

func TestInclusionTracker_PaymentStateLogging(t *testing.T) {
	tests := []struct {
		name       string
		revealed   bool
		expectWarn bool
	}{
		{name: "revealed payload is confirmed", revealed: true, expectWarn: false},
		{name: "unrevealed payload warns", revealed: false, expectWarn: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, hook := newHookedLogger()
			chainSvc := &stubChainService{currentFork: version.DataVersionGloas}
			builderSvc := newTestBuilderSvc(chainSvc)
			tracker := NewInclusionTracker(nil, chainSvc, builderSvc, nil, nil, logger)

			ourHash := phase0.Hash32{0xab}
			ourRoot := phase0.Root{0x05}
			payload := newTestPayload(5, ourHash, big.NewInt(1_000_000_000_000))
			builderSvc.GetPayloadCache().Store(payload)

			tracker.processBlockInfo(&beacon.BlockInfo{Slot: 5, Root: ourRoot, ExecutionBlockHash: ourHash})

			if tt.revealed {
				payload.MarkRevealed(payload_builder.RevealRecord{
					Transport:       payload_builder.BidTransportP2P,
					BeaconBlockRoot: phase0.Root{0x11},
				})
			}

			tracker.processBlockInfo(&beacon.BlockInfo{
				Slot: 6, Root: phase0.Root{0x06}, ParentRoot: ourRoot,
				FinalitySafeExecutionBlockHash: ourHash,
			})

			assert.Equal(t, tt.expectWarn, hasWarnContaining(hook, "NOT revealed"),
				"payment warning expectation")
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
			includedSub := tracker.SubscribeIncluded(4, false)

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
