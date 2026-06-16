package builder

import (
	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth-engine-client/spec/paris"
	"github.com/ethpandaops/go-eth-engine-client/spec/shanghai"
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/capella"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
)

// beaconPayloadFromEngine is the single, fork-independent conversion from the
// Engine API execution payload to the beacon (consensus) fork-agnostic
// ExecutionPayload. Both sides hold the union of all fork fields, so the same
// copy works for every fork; the fork is recorded in Version, which downstream
// consumers use to derive the fork-specific view (deneb/gloas) via ToVersioned.
func beaconPayloadFromEngine(
	p *engineall.ExecutionPayload,
	beaconVersion version.DataVersion,
) *eth2all.ExecutionPayload {
	out := &eth2all.ExecutionPayload{
		Version:         beaconVersion,
		ParentHash:      phase0.Hash32(p.ParentHash),
		FeeRecipient:    bellatrix.ExecutionAddress(p.FeeRecipient),
		StateRoot:       phase0.Root(p.StateRoot),
		ReceiptsRoot:    phase0.Root(p.ReceiptsRoot),
		LogsBloom:       [256]byte(p.LogsBloom),
		PrevRandao:      [32]byte(p.PrevRandao),
		BlockNumber:     p.BlockNumber,
		GasLimit:        p.GasLimit,
		GasUsed:         p.GasUsed,
		Timestamp:       p.Timestamp,
		ExtraData:       p.ExtraData,
		BaseFeePerGas:   p.BaseFeePerGas,
		BlockHash:       phase0.Hash32(p.BlockHash),
		BlobGasUsed:     p.BlobGasUsed,
		ExcessBlobGas:   p.ExcessBlobGas,
		BlockAccessList: gloas.BlockAccessList(p.BlockAccessList),
		SlotNumber:      p.SlotNumber,
	}

	out.Transactions = make([]bellatrix.Transaction, len(p.Transactions))
	for i, tx := range p.Transactions {
		out.Transactions[i] = bellatrix.Transaction(tx)
	}

	out.Withdrawals = make([]*capella.Withdrawal, len(p.Withdrawals))
	for i, w := range p.Withdrawals {
		if w == nil {
			out.Withdrawals[i] = &capella.Withdrawal{}
			continue
		}
		out.Withdrawals[i] = &capella.Withdrawal{
			Index:          capella.WithdrawalIndex(w.Index),
			ValidatorIndex: phase0.ValidatorIndex(w.ValidatorIndex),
			Address:        bellatrix.ExecutionAddress(w.Address),
			Amount:         phase0.Gwei(w.Amount),
		}
	}

	return out
}

// convertWithdrawalsToEngineFormat converts CL withdrawals from a
// payload_attributes event into the engine API shanghai withdrawal type used
// in the fork-agnostic payload attributes. Always returns a non-nil slice
// (empty if input is nil).
func convertWithdrawalsToEngineFormat(clWithdrawals []*capella.Withdrawal) []*shanghai.Withdrawal {
	result := make([]*shanghai.Withdrawal, len(clWithdrawals))
	for i, w := range clWithdrawals {
		if w == nil {
			result[i] = &shanghai.Withdrawal{}
			continue
		}
		result[i] = &shanghai.Withdrawal{
			Index:          uint64(w.Index),
			ValidatorIndex: uint64(w.ValidatorIndex),
			Address:        paris.Address(w.Address),
			Amount:         uint64(w.Amount),
		}
	}

	return result
}
