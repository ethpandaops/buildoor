package payload_builder

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// TestApplyAttributesFallback re-uses the parent slot's payload attributes
// when a slot's block is missing entirely and the beacon node never emitted
// fresh attributes: only the proposal slot and timestamp advance.
func TestApplyAttributesFallback(t *testing.T) {
	cfg := config.DefaultConfig()

	chainSvc := &stubChainService{spec: &chain.ChainSpec{
		SecondsPerSlot: 12 * time.Second,
		SlotsPerEpoch:  32,
	}}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	// The client never connects (delayed start); only its event stream is used.
	clClient, err := beacon.NewClient(context.Background(), "http://127.0.0.1:1", log)
	require.NoError(t, err)

	planSvc := action_plan.NewPlanService(cfg, chainSvc, log)

	svc, err := NewService(cfg, clClient, chainSvc, planSvc, nil, common.Address{}, log)
	require.NoError(t, err)

	svc.ctx = context.Background()

	events := clClient.Events()
	parent := &beacon.PayloadAttributesEvent{
		Version:           "gloas",
		ProposalSlot:      20,
		ProposerIndex:     7,
		ParentBlockRoot:   phase0.Root{0xbb},
		ParentBlockNumber: 15,
		ParentBlockHash:   phase0.Hash32{0xaa},
		Timestamp:         1000,
		PrevRandao:        phase0.Root{0xcc},
	}
	require.True(t, events.InjectPayloadAttributes(parent))

	sub := events.SubscribePayloadAttributes()
	defer sub.Unsubscribe()

	svc.applyAttributesFallback(21)

	synthesized := events.GetLatestPayloadAttributes(21)
	require.NotNil(t, synthesized, "fallback must synthesize attributes for the empty slot")
	assert.Equal(t, phase0.Slot(21), synthesized.ProposalSlot)
	assert.Equal(t, uint64(1012), synthesized.Timestamp, "timestamp advances one slot")
	assert.Equal(t, parent.ParentBlockHash, synthesized.ParentBlockHash, "parent unchanged: no new block on top")
	assert.Equal(t, parent.ParentBlockRoot, synthesized.ParentBlockRoot)
	assert.Equal(t, parent.ParentBlockNumber, synthesized.ParentBlockNumber)
	assert.Equal(t, parent.PrevRandao, synthesized.PrevRandao)

	select {
	case got := <-sub.Channel():
		assert.Equal(t, phase0.Slot(21), got.ProposalSlot, "synthesized event must be dispatched")
	default:
		t.Fatal("expected the synthesized event on the dispatcher")
	}

	// With attributes now present, a second fallback run must not dispatch again.
	svc.applyAttributesFallback(21)

	select {
	case <-sub.Channel():
		t.Fatal("fallback must not re-dispatch when attributes exist")
	default:
	}

	// Without parent attributes there is nothing to synthesize from.
	svc.applyAttributesFallback(30)
	assert.Nil(t, events.GetLatestPayloadAttributes(30))
}

// TestApplyAttributesFallbackMultiSlotGap covers runs of consecutive missing
// blocks: the last available attributes are used and the timestamp advances
// by the number of skipped slots.
func TestApplyAttributesFallbackMultiSlotGap(t *testing.T) {
	cfg := config.DefaultConfig()

	chainSvc := &stubChainService{spec: &chain.ChainSpec{
		SecondsPerSlot: 12 * time.Second,
		SlotsPerEpoch:  32,
	}}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	clClient, err := beacon.NewClient(context.Background(), "http://127.0.0.1:1", log)
	require.NoError(t, err)

	planSvc := action_plan.NewPlanService(cfg, chainSvc, log)

	svc, err := NewService(cfg, clClient, chainSvc, planSvc, nil, common.Address{}, log)
	require.NoError(t, err)

	svc.ctx = context.Background()

	events := clClient.Events()
	require.True(t, events.InjectPayloadAttributes(&beacon.PayloadAttributesEvent{
		ProposalSlot:    40,
		Timestamp:       2000,
		ParentBlockHash: phase0.Hash32{0xdd},
	}))

	// Slots 41-43 are all missing: the fallback for 43 reaches back to 40.
	svc.applyAttributesFallback(43)

	synthesized := events.GetLatestPayloadAttributes(43)
	require.NotNil(t, synthesized)
	assert.Equal(t, phase0.Slot(43), synthesized.ProposalSlot)
	assert.Equal(t, uint64(2000+3*12), synthesized.Timestamp, "timestamp advances by the skipped slots")
	assert.Equal(t, phase0.Hash32{0xdd}, synthesized.ParentBlockHash)

	// Beyond the lookback window (past both slot 40 and the synthesized 43)
	// nothing is synthesized.
	svc.applyAttributesFallback(43 + attrFallbackLookback + 1)
	assert.Nil(t, events.GetLatestPayloadAttributes(43+attrFallbackLookback+1))
}
