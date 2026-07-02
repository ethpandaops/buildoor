package payload_bidder

import (
	"io"
	"math/big"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/db"
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
			revealSvc := NewRevealService(&config.Config{}, NewSigner(blsSigner), &mockEnvelopePublisher{},
				chainSvc, builderSvc, payments, logger)

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
			default:
				t.Fatal("expected a PayloadIncludedEvent")
			}
		})
	}
}

func TestInclusionTracker_PersistsWonBlockWithSource(t *testing.T) {
	logger, _ := newHookedLogger()
	chainSvc := &stubChainService{currentFork: version.DataVersionGloas}
	builderSvc := newTestBuilderSvc(chainSvc)
	tracker := NewInclusionTracker(nil, chainSvc, builderSvc, nil, nil, logger)

	stateDB := db.NewDatabase(&db.Config{File: filepath.Join(t.TempDir(), "state.db")}, logger)
	require.NoError(t, stateDB.Init())

	defer func() {
		require.NoError(t, stateDB.Close())
	}()

	tracker.SetStateDB(stateDB)

	// A payload with a Builder API bid record → source builder_api.
	apiHash := phase0.Hash32{0xaa}
	apiPayload := newTestPayload(5, apiHash, big.NewInt(1_000_000_000_000))
	apiPayload.AddBid(payload_builder.BidRecord{Transport: payload_builder.BidTransportBuilderAPI, Value: 1000})
	apiPayload.AddBid(payload_builder.BidRecord{Transport: payload_builder.BidTransportP2P, Value: 1000})
	builderSvc.GetPayloadCache().Store(apiPayload)
	tracker.processBlockInfo(&beacon.BlockInfo{Slot: 5, ExecutionBlockHash: apiHash})

	// A payload with only p2p bid records → source epbs.
	p2pHash := phase0.Hash32{0xbb}
	p2pPayload := newTestPayload(6, p2pHash, big.NewInt(2_000_000_000_000))
	p2pPayload.AddBid(payload_builder.BidRecord{Transport: payload_builder.BidTransportP2P, Value: 2000})
	builderSvc.GetPayloadCache().Store(p2pPayload)
	tracker.processBlockInfo(&beacon.BlockInfo{Slot: 6, ExecutionBlockHash: p2pHash})

	blocks, total, err := stateDB.GetWonBlocks(0, 10)
	require.NoError(t, err)
	require.Equal(t, 2, total, "the inclusion tracker persists every match exactly once")

	// Newest first.
	assert.Equal(t, uint64(6), blocks[0].Slot)
	assert.Equal(t, db.WonBlockSourceEPBS, blocks[0].Source)
	assert.Equal(t, "2000000000000", blocks[0].ValueWei)

	assert.Equal(t, uint64(5), blocks[1].Slot)
	assert.Equal(t, db.WonBlockSourceBuilderAPI, blocks[1].Source)
	assert.Equal(t, "0.000001000000000000", blocks[1].ValueETH)
}

func TestInclusionTracker_NoStateDBIsSafe(t *testing.T) {
	logger, _ := newHookedLogger()
	chainSvc := &stubChainService{currentFork: version.DataVersionElectra}
	builderSvc := newTestBuilderSvc(chainSvc)
	tracker := NewInclusionTracker(nil, chainSvc, builderSvc, nil, nil, logger)

	blockHash := phase0.Hash32{0xab}
	builderSvc.GetPayloadCache().Store(newTestPayload(3, blockHash, big.NewInt(1)))

	// SetStateDB never called — persistWonBlock must be a no-op, not a panic.
	tracker.processBlockInfo(&beacon.BlockInfo{Slot: 3, ExecutionBlockHash: blockHash})

	assert.Equal(t, uint64(1), builderSvc.GetStats().BlocksIncluded)
}
