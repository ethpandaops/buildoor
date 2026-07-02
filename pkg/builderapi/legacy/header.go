package legacy

import (
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/capella"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/holiman/uint256"
	"github.com/pk910/dynamic-ssz/hasher"
	"github.com/pk910/dynamic-ssz/sszutils"
)

const defaultMaxWithdrawalsPerPayload = 16

// ExecutionPayloadHeaderFromBeacon builds a fork-agnostic execution payload
// header, pinned to the given fork, from the fork-agnostic beacon execution
// payload. Used to construct BuilderBids for getHeader responses (Bellatrix
// onwards).
func ExecutionPayloadHeaderFromBeacon(
	p *eth2all.ExecutionPayload,
	fork version.DataVersion,
	maxWithdrawalsPerPayload uint64,
) (*eth2all.ExecutionPayloadHeader, error) {
	if p == nil {
		return nil, nil
	}

	header := &eth2all.ExecutionPayloadHeader{
		Version:       fork,
		ParentHash:    p.ParentHash,
		FeeRecipient:  p.FeeRecipient,
		StateRoot:     p.StateRoot,
		ReceiptsRoot:  p.ReceiptsRoot,
		LogsBloom:     p.LogsBloom,
		PrevRandao:    p.PrevRandao,
		BlockNumber:   p.BlockNumber,
		GasLimit:      p.GasLimit,
		GasUsed:       p.GasUsed,
		Timestamp:     p.Timestamp,
		ExtraData:     p.ExtraData,
		BlockHash:     p.BlockHash,
		BlobGasUsed:   p.BlobGasUsed,
		ExcessBlobGas: p.ExcessBlobGas,
	}

	// Fill both base-fee representations of the agnostic union: the uint256
	// (Deneb onwards) and the little-endian bytes (Bellatrix/Capella wire
	// format), from whichever the payload carries.
	baseFee := new(uint256.Int)

	if p.BaseFeePerGas != nil {
		baseFee.Set(p.BaseFeePerGas)

		be := p.BaseFeePerGas.Bytes32()
		for i := range 32 {
			header.BaseFeePerGasLE[i] = be[31-i]
		}
	} else {
		header.BaseFeePerGasLE = p.BaseFeePerGasLE

		be := make([]byte, 32)
		for i := range 32 {
			be[i] = p.BaseFeePerGasLE[31-i]
		}
		baseFee.SetBytes(be)
	}

	header.BaseFeePerGas = baseFee

	txs := make([][]byte, len(p.Transactions))
	for i, tx := range p.Transactions {
		txs[i] = tx
	}

	txRoot, err := transactionsRoot(txs)
	if err != nil {
		return nil, err
	}
	header.TransactionsRoot = phase0.Root(txRoot)

	// Withdrawals exist from Capella onwards; the Bellatrix header view has
	// no withdrawals root.
	if fork >= version.DataVersionCapella {
		withdrawalsRoot, err := withdrawalsRoot(p.Withdrawals, maxWithdrawalsPerPayload)
		if err != nil {
			return nil, err
		}
		header.WithdrawalsRoot = phase0.Root(withdrawalsRoot)
	}

	return header, nil
}

// transactionsRoot computes the SSZ hash tree root of a list of transactions (List[ByteList]).
func transactionsRoot(txs [][]byte) ([32]byte, error) {
	return merkleizeByteLists(txs, 1048576, 1073741824)
}

// withdrawalsRoot computes the SSZ hash tree root of the withdrawals list.
func withdrawalsRoot(list []*capella.Withdrawal, maxWithdrawalsPerPayload uint64) ([32]byte, error) {
	if maxWithdrawalsPerPayload == 0 {
		maxWithdrawalsPerPayload = defaultMaxWithdrawalsPerPayload
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
		hh.MerkleizeWithMixin(idx, vlen, sszutils.CalculateLimit(maxWithdrawalsPerPayload, vlen, 32))
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
