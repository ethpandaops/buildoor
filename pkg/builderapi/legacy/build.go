package legacy

import (
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/capella"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/holiman/uint256"
	"github.com/pk910/dynamic-ssz/hasher"
	"github.com/pk910/dynamic-ssz/sszutils"

	legacytypes "github.com/ethpandaops/buildoor/pkg/builderapi/legacy/types"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

const defaultMaxWithdrawalsPerPayload = 16

// BidSigner signs a BuilderBid and returns the signature.
type BidSigner interface {
	SignWithDomain(root phase0.Root, domain phase0.Domain) (phase0.BLSSignature, error)
}

// BuildSignedBuilderBid builds a SignedBuilderBid for the given fork from a Payload and the
// proposer's pubkey, and signs it with the builder's BLS key using DOMAIN_APPLICATION_BUILDER
// with the provided genesis fork version and a zero genesis validators root (matches
// mev-boost-relay behavior). The bid carries the fork's field set (blob KZG commitments from
// Deneb, execution requests from Electra).
// subsidyGwei is added to the bid value so the proposer sees a higher bid (e.g. for testing).
func BuildSignedBuilderBid(
	event *payload_builder.Payload,
	fork version.DataVersion,
	proposerPubkey phase0.BLSPubKey,
	blsSigner BidSigner,
	subsidyGwei uint64,
	genesisForkVersion phase0.Version,
	maxWithdrawalsPerPayload uint64,
) (*legacytypes.SignedBuilderBid, error) {
	if event == nil || event.ExecutionPayload == nil {
		return nil, nil
	}

	header, err := ExecutionPayloadHeaderFromBeacon(event.ExecutionPayload, fork, maxWithdrawalsPerPayload)
	if err != nil {
		return nil, err
	}

	value, overflow := uint256.FromBig(event.BlockValue)
	if overflow || value == nil {
		value = new(uint256.Int)
	}
	if subsidyGwei > 0 {
		subsidyWei := subsidyGwei * 1_000_000_000
		value.Add(value, new(uint256.Int).SetUint64(subsidyWei))
	}

	bid := &legacytypes.BuilderBid{
		Version: fork,
		Header:  header,
		Value:   value,
		Pubkey:  proposerPubkey,
	}

	if fork >= version.DataVersionDeneb && event.BlobsBundle != nil {
		bid.BlobKZGCommitments = event.BlobsBundle.Commitments
	}

	// Execution requests exist from Electra onwards; builder deposit/exit
	// requests (Gloas+) do not exist in the legacy dialect's spec versions.
	if fork >= version.DataVersionElectra {
		execRequests := &eth2all.ExecutionRequests{Version: fork}
		if event.ExecutionRequests != nil {
			execRequests.Deposits = event.ExecutionRequests.Deposits
			execRequests.Withdrawals = event.ExecutionRequests.Withdrawals
			execRequests.Consolidations = event.ExecutionRequests.Consolidations
		}
		bid.ExecutionRequests = execRequests
	}

	bidRoot, err := bid.HashTreeRoot()
	if err != nil {
		return nil, err
	}

	var root phase0.Root
	copy(root[:], bidRoot[:])
	var zeroRoot phase0.Root

	domain := signer.ComputeDomain(
		signer.DomainApplicationBuilder,
		genesisForkVersion,
		zeroRoot,
	)

	sig, err := blsSigner.SignWithDomain(root, domain)
	if err != nil {
		return nil, err
	}

	return &legacytypes.SignedBuilderBid{
		Version:   fork,
		Message:   bid,
		Signature: sig,
	}, nil
}

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
