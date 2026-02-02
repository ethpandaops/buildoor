// Package fulu provides Fulu (builder-specs) types and helpers for the Builder API.
package fulu

import (
	"github.com/attestantio/go-eth2-client/spec/bellatrix"
	"github.com/attestantio/go-eth2-client/spec/capella"
	"github.com/attestantio/go-eth2-client/spec/deneb"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
	"github.com/pk910/dynamic-ssz/hasher"
	"github.com/pk910/dynamic-ssz/sszutils"

	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
)

// ExecutionPayloadHeaderFromEngine builds a deneb.ExecutionPayloadHeader from engine.ExecutionPayload.
// Used to construct a Fulu BuilderBid for getHeader responses.
func ExecutionPayloadHeaderFromEngine(p *engine.ExecutionPayload) (*deneb.ExecutionPayloadHeader, error) {
	if p == nil {
		return nil, nil
	}

	baseFee := new(uint256.Int)
	if p.BaseFeePerGas != nil {
		baseFee.SetFromBig(p.BaseFeePerGas)
	}

	header := &deneb.ExecutionPayloadHeader{
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

	copy(header.LogsBloom[:], p.LogsBloom[:])
	copy(header.PrevRandao[:], p.PrevRandao[:])

	txRoot, err := transactionsRoot(p.Transactions)
	if err != nil {
		return nil, err
	}
	header.TransactionsRoot = phase0.Root(txRoot)

	withdrawalsRoot, err := withdrawalsRoot(p.Withdrawals)
	if err != nil {
		return nil, err
	}
	header.WithdrawalsRoot = phase0.Root(withdrawalsRoot)

	return header, nil
}

// transactionsRoot computes the SSZ hash tree root of a list of transactions (List[ByteList]).
func transactionsRoot(txs [][]byte) ([32]byte, error) {
	return merkleizeByteLists(txs, 1048576, 1073741824)
}

// withdrawalsRoot computes the SSZ hash tree root of withdrawals (ExecutionPayload uses types.Withdrawal).
func withdrawalsRoot(withdrawals []*types.Withdrawal) ([32]byte, error) {
	list := make([]*capella.Withdrawal, len(withdrawals))
	for i, w := range withdrawals {
		if w == nil {
			list[i] = &capella.Withdrawal{}
			continue
		}
		list[i] = &capella.Withdrawal{
			Index:          capella.WithdrawalIndex(w.Index),
			ValidatorIndex: phase0.ValidatorIndex(w.Validator),
			Address:        bellatrix.ExecutionAddress(w.Address),
			Amount:         phase0.Gwei(w.Amount),
		}
	}
	var root [32]byte
	err := hasher.WithDefaultHasher(func(hh sszutils.HashWalker) error {
		idx := hh.Index()
		for _, w := range list {
			if err := w.HashTreeRootWith(hh); err != nil {
				return err
			}
		}
		vlen := uint64(len(list))
		hh.MerkleizeWithMixin(idx, vlen, sszutils.CalculateLimit(16, vlen, 32))
		var err error
		root, err = hh.HashRoot()
		return err
	})
	return root, err
}

// merkleizeByteLists computes the SSZ hash tree root of a list of byte lists (List[ByteList]).
func merkleizeByteLists(items [][]byte, maxItems, maxBytesPerItem uint64) ([32]byte, error) {
	var root [32]byte
	err := hasher.WithDefaultHasher(func(hh sszutils.HashWalker) error {
		vlen := uint64(len(items))
		if vlen > maxItems {
			return sszutils.ErrListTooBig
		}
		idx := hh.Index()
		for i := range items {
			item := items[i]
			vlenItem := uint64(len(item))
			if vlenItem > maxBytesPerItem {
				return sszutils.ErrListTooBig
			}
			idxItem := hh.Index()
			hh.AppendBytes32(item)
			hh.MerkleizeWithMixin(idxItem, vlenItem, sszutils.CalculateLimit(maxBytesPerItem, vlenItem, 1))
		}
		hh.MerkleizeWithMixin(idx, vlen, sszutils.CalculateLimit(maxItems, vlen, 32))
		var err error
		root, err = hh.HashRoot()
		return err
	})
	return root, err
}
