// Package chain provides epoch-level state management and builder information caching.
package chain

import (
	"time"

	"github.com/attestantio/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// BuilderInfo represents information about a builder from the beacon state.
type BuilderInfo struct {
	Index             uint64
	Pubkey            phase0.BLSPubKey
	Balance           uint64
	Active            bool
	DepositEpoch      uint64
	WithdrawableEpoch uint64
}

// SlotToTime converts a slot number to a timestamp.
func SlotToTime(genesis *beacon.Genesis, spec *beacon.ChainSpec, slot phase0.Slot) time.Time {
	slotDuration := time.Duration(uint64(slot)) * spec.SecondsPerSlot
	return genesis.GenesisTime.Add(slotDuration)
}

// TimeToSlot converts a timestamp to a slot number.
func TimeToSlot(genesis *beacon.Genesis, spec *beacon.ChainSpec, t time.Time) phase0.Slot {
	if t.Before(genesis.GenesisTime) {
		return 0
	}

	elapsed := t.Sub(genesis.GenesisTime)

	return phase0.Slot(elapsed / spec.SecondsPerSlot)
}

// EpochStats holds cached statistics for an epoch computed from beacon state.
type EpochStats struct {
	Epoch     phase0.Epoch
	StateSlot phase0.Slot
	StateRoot phase0.Root
	IsGloas   bool

	// Validator data
	ActiveValidators  uint64
	ValidatorCount    uint64
	ActiveIndices     []phase0.ValidatorIndex
	EffectiveBalances []uint32 // Effective balance in full ETH units

	// Builder data (for lifecycle management, Gloas only)
	Builders       []*BuilderInfo
	BuildersLoaded bool

	// Pre-computed duties
	RandaoMix      phase0.Hash32
	NextRandaoMix  phase0.Hash32
	ProposerDuties []phase0.ValidatorIndex // [slot_index] -> validator index
	AttesterDuties [][][]ActiveIndiceIndex // [slot_index][committee_index][member] -> active indice index
	PtcDuties      [][]ActiveIndiceIndex   // [slot_index][ptc_member] -> active indice index (Gloas only)
}
