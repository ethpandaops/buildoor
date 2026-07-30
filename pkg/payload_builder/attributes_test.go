package payload_builder

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// newPrimedHeadTracker creates an offline head tracker (no beacon client)
// with the given blocks in its ancestry cache, on a Gloas-from-genesis spec
// whose genesis lies an hour in the past.
func newPrimedHeadTracker(spec *chain.ChainSpec, blocks ...*beacon.BlockInfo) *chain.HeadTracker {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	gloasSpec := *spec
	gloasSpec.ForkSchedule = []chain.ForkSchedule{
		{Fork: version.DataVersionGloas, Version: phase0.Version{0x01}, Epoch: 0},
	}

	tracker := chain.NewHeadTracker(nil, &gloasSpec,
		&beacon.Genesis{GenesisTime: time.Now().Add(-time.Hour)}, log)
	for _, block := range blocks {
		tracker.PrimeBlock(block)
	}

	return tracker
}

// sanitizeTestSetup builds a service whose chain stub carries an offline head
// tracker primed with the given blocks (genesis an hour in the past, so every
// Gloas payload without reveal evidence resolves to empty).
func sanitizeTestSetup(t *testing.T, blocks ...*beacon.BlockInfo) *Service {
	t.Helper()

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	spec := &chain.ChainSpec{
		SecondsPerSlot: 12 * time.Second,
		SlotsPerEpoch:  32,
		PayloadDueBps:  5000,
	}
	chainSvc := &stubChainService{spec: spec}
	chainSvc.headTracker = newPrimedHeadTracker(spec, blocks...)

	cfg := config.DefaultConfig()
	planSvc := action_plan.NewPlanService(cfg, chainSvc, log)

	svc, err := NewService(cfg, nil, chainSvc, planSvc, nil, common.Address{}, log)
	require.NoError(t, err)

	svc.ctx = context.Background()

	return svc
}

func TestSanitizeAttributes_UnrevealedParentPayload(t *testing.T) {
	parentBlock := &beacon.BlockInfo{
		Slot:                           4,
		Root:                           phase0.Root{0x04},
		ExecutionBlockHash:             phase0.Hash32{0xe4},
		FinalitySafeExecutionBlockHash: phase0.Hash32{0xe3},
	}
	svc := sanitizeTestSetup(t, parentBlock)

	// The event claims the parent's committed payload as execution parent,
	// but that payload was never revealed (past the deadline, no evidence).
	event := &beacon.PayloadAttributesEvent{
		ProposalSlot:      5,
		ParentBlockRoot:   parentBlock.Root,
		ParentBlockHash:   parentBlock.ExecutionBlockHash,
		ParentBlockNumber: 44,
	}

	sanitized := svc.sanitizeAttributes(event)
	require.NotSame(t, event, sanitized, "correction must copy, never mutate the cached event")
	assert.Equal(t, parentBlock.FinalitySafeExecutionBlockHash, sanitized.ParentBlockHash,
		"execution parent must be redirected to the last built block")
	assert.Equal(t, parentBlock.Root, sanitized.ParentBlockRoot, "beacon parent stays")
	assert.Equal(t, event.ParentBlockHash, phase0.Hash32{0xe4}, "original event untouched")
}

func TestSanitizeAttributes_ConsistentEventUnchanged(t *testing.T) {
	parentBlock := &beacon.BlockInfo{
		Slot:                           4,
		Root:                           phase0.Root{0x04},
		ExecutionBlockHash:             phase0.Hash32{0xe4},
		FinalitySafeExecutionBlockHash: phase0.Hash32{0xe3},
	}
	svc := sanitizeTestSetup(t, parentBlock)

	// The event already references the empty-parent execution block.
	event := &beacon.PayloadAttributesEvent{
		ProposalSlot:      5,
		ParentBlockRoot:   parentBlock.Root,
		ParentBlockHash:   parentBlock.FinalitySafeExecutionBlockHash,
		ParentBlockNumber: 43,
	}

	assert.Same(t, event, svc.sanitizeAttributes(event))
}

func TestSanitizeAttributes_BackfillsParentBlockNumber(t *testing.T) {
	// A parent whose committed and finality-safe hashes agree (no empty
	// variant) and whose execution block number is known.
	parentBlock := &beacon.BlockInfo{
		Slot:                           4,
		Root:                           phase0.Root{0x04},
		ExecutionBlockHash:             phase0.Hash32{0xe4},
		FinalitySafeExecutionBlockHash: phase0.Hash32{0xe4},
		ExecutionBlockNumber:           44,
	}
	svc := sanitizeTestSetup(t, parentBlock)

	event := &beacon.PayloadAttributesEvent{
		ProposalSlot:    5,
		ParentBlockRoot: parentBlock.Root,
		ParentBlockHash: parentBlock.ExecutionBlockHash,
	}

	sanitized := svc.sanitizeAttributes(event)
	require.NotSame(t, event, sanitized)
	assert.Equal(t, uint64(44), sanitized.ParentBlockNumber)
	assert.Equal(t, uint64(0), event.ParentBlockNumber, "original event untouched")
}

func TestSanitizeAttributes_UnknownParentUnchanged(t *testing.T) {
	svc := sanitizeTestSetup(t)

	event := &beacon.PayloadAttributesEvent{
		ProposalSlot:    5,
		ParentBlockRoot: phase0.Root{0xff},
		ParentBlockHash: phase0.Hash32{0xff},
	}

	assert.Same(t, event, svc.sanitizeAttributes(event))
}

func TestHandlePayloadAttributes_RescheduleOnParentChange(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	spec := &chain.ChainSpec{
		SecondsPerSlot: 12 * time.Second,
		SlotsPerEpoch:  32,
	}
	chainSvc := &stubChainService{spec: spec}

	cfg := config.DefaultConfig()
	cfg.EPBSEnabled = true
	planSvc := action_plan.NewPlanService(cfg, chainSvc, log)

	clClient, err := beacon.NewClient(context.Background(), "http://127.0.0.1:1", log)
	require.NoError(t, err)

	svc, err := NewService(cfg, clClient, chainSvc, planSvc, nil, common.Address{}, log)
	require.NoError(t, err)

	svc.ctx = context.Background()

	slot := phase0.Slot(200)
	first := &beacon.PayloadAttributesEvent{
		ProposalSlot:    slot,
		ParentBlockRoot: phase0.Root{0x01},
		ParentBlockHash: phase0.Hash32{0xaa},
	}

	svc.handlePayloadAttributesEvent(first)

	svc.scheduledBuildMu.Lock()
	state := svc.slotBuilds[slot]
	svc.scheduledBuildMu.Unlock()
	require.NotNil(t, state)
	assert.True(t, state.passScheduled, "first attributes event schedules the build pass")

	// Later events for the same slot — same or different parent — accumulate
	// as variants without rescheduling; the pass resolves them at fire time.
	svc.handlePayloadAttributesEvent(first)

	reorged := &beacon.PayloadAttributesEvent{
		ProposalSlot:    slot,
		ParentBlockRoot: phase0.Root{0x02},
		ParentBlockHash: phase0.Hash32{0xbb},
	}
	svc.handlePayloadAttributesEvent(reorged)

	svc.scheduledBuildMu.Lock()
	state = svc.slotBuilds[slot]
	svc.scheduledBuildMu.Unlock()
	assert.True(t, state.passScheduled)
	assert.Empty(t, state.started, "no candidate build starts before the pass fires")
}
