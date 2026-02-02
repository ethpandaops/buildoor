// Package fulu provides engine->deneb ExecutionPayload conversion for unblinding.
package fulu

import (
	"github.com/attestantio/go-eth2-client/spec/bellatrix"
	"github.com/attestantio/go-eth2-client/spec/capella"
	"github.com/attestantio/go-eth2-client/spec/deneb"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"

	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
)

var _ = types.Withdrawal{} // used for conversion in Withdrawals

// ExecutionPayloadFromEngine builds deneb.ExecutionPayload from engine.ExecutionPayload.
// Used when unblinding a Fulu block for publishing.
func ExecutionPayloadFromEngine(p *engine.ExecutionPayload) (*deneb.ExecutionPayload, error) {
	if p == nil {
		return nil, nil
	}

	baseFee := new(uint256.Int)
	if p.BaseFeePerGas != nil {
		baseFee.SetFromBig(p.BaseFeePerGas)
	}

	payload := &deneb.ExecutionPayload{
		ParentHash:    phase0.Hash32(p.ParentHash),
		FeeRecipient:  bellatrix.ExecutionAddress(p.FeeRecipient),
		StateRoot:     phase0.Root(p.StateRoot),
		ReceiptsRoot:  phase0.Root(p.ReceiptsRoot),
		BlockNumber:   p.BlockNumber,
		GasLimit:      p.GasLimit,
		GasUsed:       p.GasUsed,
		Timestamp:     p.Timestamp,
		ExtraData:     p.ExtraData,
		BaseFeePerGas: baseFee,
		BlockHash:     phase0.Hash32(p.BlockHash),
		BlobGasUsed:   p.BlobGasUsed,
		ExcessBlobGas: p.ExcessBlobGas,
	}

	copy(payload.LogsBloom[:], p.LogsBloom[:])
	copy(payload.PrevRandao[:], p.PrevRandao[:])

	payload.Transactions = make([]bellatrix.Transaction, len(p.Transactions))
	for i, tx := range p.Transactions {
		payload.Transactions[i] = tx
	}

	payload.Withdrawals = make([]*capella.Withdrawal, len(p.Withdrawals))
	for i, w := range p.Withdrawals {
		if w == nil {
			payload.Withdrawals[i] = &capella.Withdrawal{}
			continue
		}
		payload.Withdrawals[i] = &capella.Withdrawal{
			Index:          capella.WithdrawalIndex(w.Index),
			ValidatorIndex: phase0.ValidatorIndex(w.Validator),
			Address:        bellatrix.ExecutionAddress(w.Address),
			Amount:         phase0.Gwei(w.Amount),
		}
	}

	return payload, nil
}
