package payload_builder

// Test for NM-13: slotBuildState.started (the per-candidate build dedup map)
// used to be set before a build attempt and never cleared on failure, so a
// single transient engine error permanently blocked any retry of that
// (slot, parent-tuple) candidate for the rest of the slot -- including a
// legitimate CL-client attributes redelivery for the exact same parent.

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// unknownForkChainService forces every build attempt to fail deterministically
// and immediately: chain.EngineVersion(DataVersionUnknown) errors before any
// engine or beacon-API call is made, so engineClient/clClient can stay nil,
// matching the existing newSkipTestService pattern in this package.
type unknownForkChainService struct {
	stubChainService
}

func (s *unknownForkChainService) ActiveForkAtEpoch(phase0.Epoch) version.DataVersion {
	return version.DataVersionUnknown
}

func TestExecuteCandidateBuild_RetriesAfterAFailedAttempt(t *testing.T) {
	chainSvc := &unknownForkChainService{stubChainService{spec: &chain.ChainSpec{
		SecondsPerSlot: 12 * time.Second,
		SlotsPerEpoch:  32,
	}}}

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	cfg := config.DefaultConfig()
	planSvc := action_plan.NewPlanService(cfg, chainSvc, log)

	svc, err := NewService(cfg, nil, chainSvc, planSvc, nil, common.Address{}, log)
	require.NoError(t, err)

	// Set up the state Start() would normally set up, without calling Start()
	// itself (which needs a live beacon client for its event stream).
	svc.ctx = context.Background()
	svc.payloadBuilder = NewPayloadBuilder(nil, nil, chainSvc, common.Address{}, cfg, log, nil)

	sub := svc.SubscribePayloadBuildFailed(4, false)
	defer sub.Unsubscribe()

	slot := phase0.Slot(500)
	attrs := &beacon.PayloadAttributesEvent{
		ProposalSlot:    slot,
		ParentBlockRoot: phase0.Root{0x11},
		ParentBlockHash: phase0.Hash32{0x22},
	}
	target := &buildTarget{candidate: chain.CandidateParentFull, attrs: attrs}

	// First attempt fails (the unknown-fork stand-in for a transient engine
	// error) and marks the tuple started.
	svc.executeCandidateBuild(slot, target)

	select {
	case event := <-sub.Channel():
		require.Equal(t, slot, event.Slot)
	case <-time.After(time.Second):
		t.Fatal("expected the first build attempt to fail and fire buildFailedDispatcher")
	}

	// A fresh payload_attributes redelivery for the EXACT SAME parent tuple
	// arrives -- a real CL client behavior (reorgs, retries, some clients
	// simply re-emit payload_attributes). NM-13 fixed: this must actually
	// retry, not be silently dropped because the tuple is still marked
	// "started" from the failed attempt.
	svc.executeCandidateBuild(slot, target)

	select {
	case event := <-sub.Channel():
		require.Equal(t, slot, event.Slot)
	case <-time.After(time.Second):
		t.Fatal("NM-13 regression: the second attempt for the same parent tuple never ran " +
			"-- the started marker was not cleared after the first failure")
	}
}
