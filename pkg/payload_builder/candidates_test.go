package payload_builder

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/capella"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// candidateTestSetup builds a service with an offline event stream and a
// chain stub whose head tracker holds the grandparent/parent chain with the
// parent as current head.
func candidateTestSetup(t *testing.T, gp, parent *beacon.BlockInfo) *Service {
	t.Helper()

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	spec := &chain.ChainSpec{
		SecondsPerSlot: 12 * time.Second,
		SlotsPerEpoch:  32,
		PayloadDueBps:  5000,
	}
	chainSvc := &stubChainService{spec: spec}
	chainSvc.headTracker = newPrimedHeadTracker(spec, gp)
	chainSvc.headTracker.PrimeHead(parent)

	cfg := config.DefaultConfig()
	cfg.EPBSEnabled = true

	planSvc := action_plan.NewPlanService(cfg, chainSvc, log)

	clClient, err := beacon.NewClient(context.Background(), "http://127.0.0.1:1", log)
	require.NoError(t, err)

	svc, err := NewService(cfg, clClient, chainSvc, planSvc, nil, common.Address{}, log)
	require.NoError(t, err)

	svc.ctx = context.Background()

	return svc
}

func TestResolveBuildTargets_PayloadMissAddsEmptyCandidate(t *testing.T) {
	gp, parent := testCandidateChain()
	svc := candidateTestSetup(t, gp, parent)
	events := svc.clClient.Events()

	// The parent block's own slot attributes (withdrawals source for the
	// empty-parent candidate).
	parentSlotWithdrawals := []*capella.Withdrawal{{Index: 7}}
	require.True(t, events.InjectPayloadAttributes(&beacon.PayloadAttributesEvent{
		ProposalSlot:    parent.Slot,
		ParentBlockRoot: gp.Root,
		ParentBlockHash: gp.ExecutionBlockHash,
		Withdrawals:     parentSlotWithdrawals,
	}))

	// The CL emitted only the full-parent variant for the target slot.
	fullVariant := &beacon.PayloadAttributesEvent{
		ProposalSlot:    5,
		ParentBlockRoot: parent.Root,
		ParentBlockHash: parent.ExecutionBlockHash,
		Withdrawals:     []*capella.Withdrawal{{Index: 9}},
	}
	require.True(t, events.InjectPayloadAttributes(fullVariant))

	targets := svc.resolveBuildTargets(5)
	require.Len(t, targets, 1)

	// The parent payload counts as withheld (genesis an hour in the past, no
	// reveal evidence), so sanitization redirects the CL's full-parent
	// variant to the empty-parent tuple — including the withdrawals swap —
	// and the unbuildable full-parent candidate is dropped (a withheld
	// payload cannot be built on and its withdrawals cannot be derived).
	assert.Equal(t, chain.CandidateParentEmpty, targets[0].candidate)
	assert.False(t, targets[0].derived)
	assert.Equal(t, gp.ExecutionBlockHash, targets[0].attrs.ParentBlockHash,
		"empty candidate builds on the grandparent's payload")
	assert.Equal(t, parent.Root, targets[0].attrs.ParentBlockRoot)
	assert.Equal(t, parentSlotWithdrawals, targets[0].attrs.Withdrawals,
		"withdrawals come from the parent block's slot attributes")
}

func TestResolveBuildTargets_NeverModeSuppresses(t *testing.T) {
	gp, parent := testCandidateChain()
	svc := candidateTestSetup(t, gp, parent)
	svc.cfg.Build.CandidateParentEmpty = config.CandidateModeNever

	events := svc.clClient.Events()
	require.True(t, events.InjectPayloadAttributes(&beacon.PayloadAttributesEvent{
		ProposalSlot:    parent.Slot,
		ParentBlockRoot: gp.Root,
		ParentBlockHash: gp.ExecutionBlockHash,
	}))
	require.True(t, events.InjectPayloadAttributes(&beacon.PayloadAttributesEvent{
		ProposalSlot:    5,
		ParentBlockRoot: parent.Root,
		ParentBlockHash: parent.FinalitySafeExecutionBlockHash,
	}))

	// The only received variant classifies as parent_empty, which the policy
	// suppresses — nothing is built for the slot.
	targets := svc.resolveBuildTargets(5)
	assert.Empty(t, targets)
}

func TestResolveBuildTargets_UnknownBranchVariantBuilt(t *testing.T) {
	gp, parent := testCandidateChain()
	svc := candidateTestSetup(t, gp, parent)

	// A variant on a branch the chain view does not know is still built
	// (unclassified) — the beacon node's own suggestion is honored.
	events := svc.clClient.Events()
	require.True(t, events.InjectPayloadAttributes(&beacon.PayloadAttributesEvent{
		ProposalSlot:    5,
		ParentBlockRoot: phase0.Root{0x99},
		ParentBlockHash: phase0.Hash32{0x99},
	}))

	targets := svc.resolveBuildTargets(5)
	require.NotEmpty(t, targets)

	last := targets[len(targets)-1]
	assert.Equal(t, chain.CandidateKey(""), last.candidate)
	assert.Equal(t, phase0.Hash32{0x99}, last.attrs.ParentBlockHash)
}

func TestClassifyCandidate(t *testing.T) {
	gp, parent := testCandidateChain()
	svc := candidateTestSetup(t, gp, parent)

	assert.Equal(t, chain.CandidateParentFull, svc.classifyCandidate(&beacon.PayloadAttributesEvent{
		ProposalSlot:    5,
		ParentBlockRoot: parent.Root,
		ParentBlockHash: parent.ExecutionBlockHash,
	}))
	assert.Equal(t, chain.CandidateParentEmpty, svc.classifyCandidate(&beacon.PayloadAttributesEvent{
		ProposalSlot:    5,
		ParentBlockRoot: parent.Root,
		ParentBlockHash: parent.FinalitySafeExecutionBlockHash,
	}))
	assert.Equal(t, chain.CandidateGrandparentFull, svc.classifyCandidate(&beacon.PayloadAttributesEvent{
		ProposalSlot:    5,
		ParentBlockRoot: gp.Root,
		ParentBlockHash: gp.ExecutionBlockHash,
	}))
	assert.Equal(t, chain.CandidateKey(""), svc.classifyCandidate(&beacon.PayloadAttributesEvent{
		ProposalSlot:    5,
		ParentBlockRoot: phase0.Root{0x99},
		ParentBlockHash: phase0.Hash32{0x99},
	}))
}

func TestPayloadCacheCandidatePriority(t *testing.T) {
	cache := NewPayloadCache(8)

	makePayload := func(key chain.CandidateKey, hash phase0.Hash32) *Payload {
		return &Payload{
			Attributes: &beacon.PayloadAttributesEvent{
				ProposalSlot:    10,
				ParentBlockRoot: phase0.Root{byte(hash[0])},
				ParentBlockHash: hash,
			},
			Candidate:  key,
			BlockHash:  hash,
			BlockValue: big.NewInt(1),
			ReadyAt:    time.Now(),
		}
	}

	empty := makePayload(chain.CandidateParentEmpty, phase0.Hash32{0x02})
	cache.Store(empty)
	assert.Same(t, empty, cache.Get(10), "only candidate wins")

	full := makePayload(chain.CandidateParentFull, phase0.Hash32{0x01})
	cache.Store(full)
	assert.Same(t, full, cache.Get(10), "parent_full outranks parent_empty")

	assert.Same(t, empty, cache.GetCandidate(10, chain.CandidateParentEmpty))
	assert.Len(t, cache.GetSlotPayloads(10), 2)
	assert.Same(t, empty, cache.GetByBlockHash(phase0.Hash32{0x02}))
}

// testCandidateChain builds the grandparent (slot 3, payload E3 on E2) and
// parent (slot 4, committing E4 on E3) blocks.
func testCandidateChain() (gp, parent *beacon.BlockInfo) {
	gp = &beacon.BlockInfo{
		Slot:                           3,
		Root:                           phase0.Root{0x03},
		ParentRoot:                     phase0.Root{0x02},
		ExecutionBlockHash:             phase0.Hash32{0xe3},
		FinalitySafeExecutionBlockHash: phase0.Hash32{0xe2},
		GasLimit:                       30_000_000,
	}
	parent = &beacon.BlockInfo{
		Slot:                           4,
		Root:                           phase0.Root{0x04},
		ParentRoot:                     gp.Root,
		ExecutionBlockHash:             phase0.Hash32{0xe4},
		FinalitySafeExecutionBlockHash: gp.ExecutionBlockHash,
		GasLimit:                       31_000_000,
	}

	return gp, parent
}
