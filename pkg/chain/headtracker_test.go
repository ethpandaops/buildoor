package chain

import (
	"context"
	"testing"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// newTestHeadTracker creates an offline head tracker (no beacon client; cache
// misses error out) on a Gloas-from-genesis chain spec.
func newTestHeadTracker(genesisTime time.Time, fork version.DataVersion) *HeadTracker {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	spec := &ChainSpec{
		SecondsPerSlot: 12 * time.Second,
		SlotsPerEpoch:  32,
		PayloadDueBps:  5000,
		ForkSchedule: []ForkSchedule{
			{Fork: fork, Version: phase0.Version{0x01}, Epoch: 0},
		},
	}

	return NewHeadTracker(nil, spec, &beacon.Genesis{GenesisTime: genesisTime}, log)
}

// testChain builds a small Gloas chain: grandparent (slot 3) on E2, parent
// (slot 4) committing E4 on top of the grandparent's E3.
func testChain() (gp, parent *beacon.BlockInfo) {
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

func TestHeadTracker_PayloadStatusEvidence(t *testing.T) {
	// Genesis far in the past: every slot's reveal deadline has passed, so
	// blocks without evidence resolve to empty.
	tracker := newTestHeadTracker(time.Now().Add(-time.Hour), version.DataVersionGloas)
	gp, parent := testChain()
	tracker.PrimeBlock(gp)
	tracker.PrimeBlock(parent)

	// Priming the parent derived full-evidence for the grandparent (the
	// parent committed to the grandparent's payload hash).
	assert.Equal(t, PayloadStatusRevealed, tracker.GetPayloadStatus(gp.Root))

	// No evidence for the parent and past the deadline: provisional empty.
	assert.Equal(t, PayloadStatusEmpty, tracker.GetPayloadStatus(parent.Root))

	// A child that built past the parent's payload marks it empty explicitly.
	childEmpty := &beacon.BlockInfo{
		Slot:                           5,
		Root:                           phase0.Root{0x05},
		ParentRoot:                     parent.Root,
		ExecutionBlockHash:             phase0.Hash32{0xe5},
		FinalitySafeExecutionBlockHash: parent.FinalitySafeExecutionBlockHash,
	}
	tracker.PrimeBlock(childEmpty)
	assert.Equal(t, PayloadStatusEmpty, tracker.GetPayloadStatus(parent.Root))

	// A payload-available event flips it to revealed and clears the empty
	// evidence.
	tracker.markPayloadRevealed(parent.Root)
	assert.Equal(t, PayloadStatusRevealed, tracker.GetPayloadStatus(parent.Root))

	// Unknown blocks report pending.
	assert.Equal(t, PayloadStatusPending, tracker.GetPayloadStatus(phase0.Root{0xff}))
}

func TestHeadTracker_PayloadStatusTiming(t *testing.T) {
	// Genesis now: slot 0 is in progress, its reveal deadline (50%) is ahead.
	tracker := newTestHeadTracker(time.Now(), version.DataVersionGloas)
	block := &beacon.BlockInfo{
		Slot:                           0,
		Root:                           phase0.Root{0x01},
		ExecutionBlockHash:             phase0.Hash32{0xe1},
		FinalitySafeExecutionBlockHash: phase0.Hash32{0xe0},
	}
	tracker.PrimeBlock(block)

	assert.Equal(t, PayloadStatusPending, tracker.GetPayloadStatus(block.Root))
}

func TestHeadTracker_PayloadStatusPreGloas(t *testing.T) {
	tracker := newTestHeadTracker(time.Now().Add(-time.Hour), version.DataVersionElectra)
	block := &beacon.BlockInfo{
		Slot:               4,
		Root:               phase0.Root{0x04},
		ExecutionBlockHash: phase0.Hash32{0xe4},
	}
	tracker.PrimeBlock(block)

	// Pre-Gloas payloads are embedded in the block and always revealed.
	assert.Equal(t, PayloadStatusRevealed, tracker.GetPayloadStatus(block.Root))
}

func TestHeadTracker_ResolveCandidatesGloas(t *testing.T) {
	tracker := newTestHeadTracker(time.Now().Add(-time.Hour), version.DataVersionGloas)
	gp, parent := testChain()
	tracker.PrimeBlock(gp)
	tracker.PrimeBlock(parent)
	tracker.head = parent

	candidates, err := tracker.ResolveCandidates(context.Background(), 5)
	require.NoError(t, err)

	byKey := make(map[CandidateKey]*CandidateParent, len(candidates))
	for _, c := range candidates {
		byKey[c.Key] = c
	}

	require.Contains(t, byKey, CandidateParentFull)
	assert.Equal(t, parent.Root, byKey[CandidateParentFull].ParentBlockRoot)
	assert.Equal(t, parent.ExecutionBlockHash, byKey[CandidateParentFull].ParentBlockHash)
	assert.Equal(t, parent.GasLimit, byKey[CandidateParentFull].ELParentGasLimit)

	require.Contains(t, byKey, CandidateParentEmpty)
	assert.Equal(t, parent.Root, byKey[CandidateParentEmpty].ParentBlockRoot)
	assert.Equal(t, gp.ExecutionBlockHash, byKey[CandidateParentEmpty].ParentBlockHash)
	assert.Equal(t, gp.GasLimit, byKey[CandidateParentEmpty].ELParentGasLimit,
		"empty-parent EL gas limit must come from the grandparent's committed payload")

	require.Contains(t, byKey, CandidateGrandparentFull)
	assert.Equal(t, gp.Root, byKey[CandidateGrandparentFull].ParentBlockRoot)
	assert.Equal(t, gp.ExecutionBlockHash, byKey[CandidateGrandparentFull].ParentBlockHash)

	require.Contains(t, byKey, CandidateGrandparentEmpty)
	assert.Equal(t, gp.Root, byKey[CandidateGrandparentEmpty].ParentBlockRoot)
	assert.Equal(t, gp.FinalitySafeExecutionBlockHash, byKey[CandidateGrandparentEmpty].ParentBlockHash)
}

func TestHeadTracker_ResolveCandidatesPreGloas(t *testing.T) {
	tracker := newTestHeadTracker(time.Now().Add(-time.Hour), version.DataVersionElectra)

	// Pre-Gloas blocks: the committed and finality-safe hashes are identical
	// (payload embedded), so no empty variants exist.
	gp := &beacon.BlockInfo{
		Slot: 3, Root: phase0.Root{0x03}, ParentRoot: phase0.Root{0x02},
		ExecutionBlockHash:             phase0.Hash32{0xe3},
		FinalitySafeExecutionBlockHash: phase0.Hash32{0xe3},
	}
	parent := &beacon.BlockInfo{
		Slot: 4, Root: phase0.Root{0x04}, ParentRoot: gp.Root,
		ExecutionBlockHash:             phase0.Hash32{0xe4},
		FinalitySafeExecutionBlockHash: phase0.Hash32{0xe4},
	}
	tracker.PrimeBlock(gp)
	tracker.PrimeBlock(parent)
	tracker.head = parent

	candidates, err := tracker.ResolveCandidates(context.Background(), 5)
	require.NoError(t, err)

	keys := make([]CandidateKey, 0, len(candidates))
	for _, c := range candidates {
		keys = append(keys, c.Key)
	}

	assert.ElementsMatch(t, []CandidateKey{CandidateParentFull, CandidateGrandparentFull}, keys)
}

func TestHeadTracker_ResolveCandidatesHeadNotBelowSlot(t *testing.T) {
	tracker := newTestHeadTracker(time.Now().Add(-time.Hour), version.DataVersionGloas)
	_, parent := testChain()
	tracker.PrimeBlock(parent)
	tracker.head = parent

	_, err := tracker.ResolveCandidates(context.Background(), parent.Slot)
	assert.Error(t, err)
}

func TestHeadTracker_ResolveReorg(t *testing.T) {
	tracker := newTestHeadTracker(time.Now().Add(-time.Hour), version.DataVersionGloas)
	gp, parent := testChain()
	competing := &beacon.BlockInfo{
		Slot:                           4,
		Root:                           phase0.Root{0x44},
		ParentRoot:                     gp.Root,
		ExecutionBlockHash:             phase0.Hash32{0xee},
		FinalitySafeExecutionBlockHash: gp.ExecutionBlockHash,
	}
	tracker.PrimeBlock(gp)
	tracker.PrimeBlock(parent)
	tracker.PrimeBlock(competing)

	depth, ancestor := tracker.resolveReorg(parent, competing)
	assert.Equal(t, uint64(1), depth)
	assert.Equal(t, gp.Root, ancestor)
}
