package beacon

import (
	"bytes"
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/electra"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	dynssz "github.com/pk910/dynamic-ssz"
	"github.com/stretchr/testify/require"
)

func TestReadStateSnapshotMatchesTheFullState(t *testing.T) {
	state := &gloas.BeaconState{
		Slot:                      12345,
		FinalizedCheckpoint:       &phase0.Checkpoint{Epoch: 381},
		DepositBalanceToConsume:   7_000_000_000,
		ETH1DepositIndex:          99,
		DepositRequestsStartIndex: 42,
		Balances:                  []phase0.Gwei{1, 2, 3},
		PendingDeposits: []*electra.PendingDeposit{
			{Pubkey: phase0.BLSPubKey{0xd1}, WithdrawalCredentials: make([]byte, 32), Amount: 32_000_000_000, Slot: 77},
		},
		Builders: []*gloas.Builder{
			{PublicKey: phase0.BLSPubKey{0xb1}, Balance: 5, DepositEpoch: 3},
		},
	}

	for i := range 3 {
		state.Validators = append(state.Validators, &phase0.Validator{
			PublicKey:        phase0.BLSPubKey{byte(i + 1)},
			EffectiveBalance: phase0.Gwei(32_000_000_000 + i),
			ActivationEpoch:  phase0.Epoch(i),
			ExitEpoch:        phase0.Epoch(1000 + i),
		})
	}

	ds := dynssz.GetGlobalDynSsz()

	// Fill the fixed vectors so the encoding is a valid state.
	full := new(gloas.BeaconState)
	encoded, err := ds.MarshalSSZ(full)
	require.NoError(t, err)
	require.NoError(t, ds.UnmarshalSSZ(full, encoded))

	full.Slot, full.FinalizedCheckpoint = state.Slot, state.FinalizedCheckpoint
	full.DepositBalanceToConsume, full.ETH1DepositIndex = state.DepositBalanceToConsume, state.ETH1DepositIndex
	full.DepositRequestsStartIndex = state.DepositRequestsStartIndex
	full.Validators, full.Balances = state.Validators, state.Balances
	full.PendingDeposits, full.Builders = state.PendingDeposits, state.Builders
	full.RANDAOMixes[5] = phase0.Root{0x55}
	full.ProposerLookahead[2] = 1234
	full.BuilderPendingPayments[1].Weight = 9

	encoded, err = ds.MarshalSSZ(full)
	require.NoError(t, err)

	snap, err := readStateSnapshot(bytes.NewReader(encoded), version.DataVersionGloas)
	require.NoError(t, err)

	require.Equal(t, full.Slot, snap.Slot)
	require.Equal(t, phase0.Epoch(381), snap.FinalizedEpoch)
	require.Equal(t, full.DepositBalanceToConsume, snap.DepositBalanceToConsume)
	require.Equal(t, uint64(99), snap.ETH1DepositIndex)
	require.Equal(t, uint64(42), snap.DepositRequestsStartIndex)
	require.Equal(t, full.RANDAOMixes, snap.RANDAOMixes)
	require.Equal(t, full.ProposerLookahead, snap.ProposerLookahead)
	require.Equal(t, full.PendingDeposits, snap.PendingDeposits)
	require.Equal(t, full.Builders, snap.Builders)
	require.Equal(t, full.BuilderPendingPayments, snap.BuilderPendingPayments)

	require.Equal(t, 3, snap.Validators.Len())
	for i, v := range full.Validators {
		require.Equal(t, v.PublicKey, snap.Validators.Pubkeys[i])
		require.Equal(t, v.EffectiveBalance, snap.Validators.EffectiveBalance[i])
		require.Equal(t, v.ActivationEpoch, snap.Validators.ActivationEpoch[i])
		require.Equal(t, v.ExitEpoch, snap.Validators.ExitEpoch[i])
	}
}

func TestReadStateSnapshotRejectsATruncatedStream(t *testing.T) {
	ds := dynssz.GetGlobalDynSsz()

	encoded, err := ds.MarshalSSZ(new(gloas.BeaconState))
	require.NoError(t, err)

	_, err = readStateSnapshot(bytes.NewReader(encoded[:len(encoded)/2]), version.DataVersionGloas)
	require.Error(t, err)
}
