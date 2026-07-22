package payload_builder

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/trie"

	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth-engine-client/spec/paris"
	"github.com/ethpandaops/go-eth-engine-client/spec/prague"
	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"
)

const maxExtraDataSize = 32

// ModifyPayload rewrites header-only fields of a built execution payload in
// place and recomputes the block hash: the extraData field gets the given
// prefix prepended (truncating the original to stay within the 32-byte
// limit), and a non-zero gasLimitOverride replaces the gas limit the EL built
// with (used when the EL ignores the requested target gas limit; the caller
// must ensure the override stays >= gasUsed and within the EIP-1559 bounds of
// the parent). The payload's BlockHash field is updated and the new hash
// returned.
//
// The parentBeaconBlockRoot is required because it is part of the block header
// (and therefore affects the block hash) but is not carried in the execution
// payload itself.
//
// executionRequests carries the EIP-7685 execution requests from the engine
// API response (Electra/Prague+); they are needed to compute the requestsHash
// header field. Pass nil for pre-Electra payloads.
//
// The function first verifies it can reconstruct the original block hash from
// the payload fields. If verification fails (e.g. an unhandled fork added new
// header fields) it returns an error rather than producing an incorrect hash.
func ModifyPayload(
	p *engineall.ExecutionPayload,
	executionRequests []prague.ExecutionRequest,
	extraDataPrefix []byte,
	gasLimitOverride uint64,
	parentBeaconBlockRoot common.Hash,
) (common.Hash, error) {
	header, err := buildHeaderFromPayload(p, parentBeaconBlockRoot, executionRequests)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to build header from payload: %w", err)
	}

	// Verify our reconstruction matches the original block hash. This catches
	// cases where the fork added new header fields we don't handle yet.
	originalHash := common.Hash(p.BlockHash)
	computedHash := header.Hash()

	if computedHash != originalHash {
		// Fallback: try toggling requestsHash. The engine API may not always
		// clearly signal whether executionRequests are present (field absent
		// vs null vs empty array). Try the opposite setting.
		if header.RequestsHash == nil {
			emptyReqHash := types.CalcRequestsHash(nil)
			header.RequestsHash = &emptyReqHash
		} else {
			header.RequestsHash = nil
		}

		computedHash = header.Hash()
		if computedHash != originalHash {
			return common.Hash{}, fmt.Errorf(
				"hash verification failed: computed %s but payload has %s "+
					"(this may indicate an unhandled fork with new header fields)",
				computedHash.Hex(), originalHash.Hex(),
			)
		}
	}

	if gasLimitOverride > 0 {
		header.GasLimit = gasLimitOverride
		p.GasLimit = gasLimitOverride
	}

	// Build new extra data: prefix + "/" separator + original (truncated to
	// fit). The separator keeps the prefix readable when concatenated with the
	// EL's original extra data (e.g. "buildoor-0/ethrex 17.0.0" rather than
	// "buildoor-0ethrex 17.0.0").
	newExtraData := make([]byte, 0, maxExtraDataSize)
	newExtraData = append(newExtraData, extraDataPrefix...)

	if len(extraDataPrefix) > 0 && len(newExtraData) < maxExtraDataSize {
		newExtraData = append(newExtraData, '/')
	}

	if remaining := maxExtraDataSize - len(newExtraData); remaining > 0 && len(header.Extra) > 0 {
		if len(header.Extra) > remaining {
			newExtraData = append(newExtraData, header.Extra[:remaining]...)
		} else {
			newExtraData = append(newExtraData, header.Extra...)
		}
	}

	if len(newExtraData) > maxExtraDataSize {
		newExtraData = newExtraData[:maxExtraDataSize]
	}

	header.Extra = newExtraData
	newHash := header.Hash()

	p.ExtraData = newExtraData
	p.BlockHash = paris.Hash32(newHash)

	return newHash, nil
}

// buildHeaderFromPayload reconstructs a go-ethereum types.Header from the
// engine execution payload fields: deriving transactionsRoot and
// withdrawalsRoot from the typed arrays, computing requestsHash from execution
// requests (Electra+), the block-access-list hash and slot number (Amsterdam+),
// and setting post-merge constants (empty uncle hash, zero difficulty/nonce).
func buildHeaderFromPayload(
	p *engineall.ExecutionPayload,
	parentBeaconBlockRoot common.Hash,
	executionRequests []prague.ExecutionRequest,
) (*types.Header, error) {
	baseFee := new(big.Int)
	if p.BaseFeePerGas != nil {
		baseFee = p.BaseFeePerGas.ToBig()
	}

	// Decode transactions and compute Merkle root.
	txs := make(types.Transactions, 0, len(p.Transactions))
	for i, txBytes := range p.Transactions {
		tx := new(types.Transaction)
		if err := tx.UnmarshalBinary(txBytes); err != nil {
			return nil, fmt.Errorf("failed to decode transaction %d: %w", i, err)
		}

		txs = append(txs, tx)
	}

	var bloom types.Bloom
	copy(bloom[:], p.LogsBloom[:])

	header := &types.Header{
		ParentHash:  common.Hash(p.ParentHash),
		UncleHash:   types.EmptyUncleHash,
		Coinbase:    common.Address(p.FeeRecipient),
		Root:        common.Hash(p.StateRoot),
		TxHash:      types.DeriveSha(txs, trie.NewStackTrie(nil)),
		ReceiptHash: common.Hash(p.ReceiptsRoot),
		Bloom:       bloom,
		Difficulty:  big.NewInt(0),
		Number:      new(big.Int).SetUint64(p.BlockNumber),
		GasLimit:    p.GasLimit,
		GasUsed:     p.GasUsed,
		Time:        p.Timestamp,
		Extra:       p.ExtraData,
		MixDigest:   common.Hash(p.PrevRandao),
		Nonce:       types.BlockNonce{},
		BaseFee:     baseFee,
	}

	// add shanghai specific fields
	if p.Version >= enginev.DataVersionShanghai {
		ws := make(types.Withdrawals, len(p.Withdrawals))
		for i, w := range p.Withdrawals {
			ws[i] = &types.Withdrawal{
				Index:     w.Index,
				Validator: w.ValidatorIndex,
				Address:   common.Address(w.Address),
				Amount:    w.Amount,
			}
		}

		wRoot := types.DeriveSha(ws, trie.NewStackTrie(nil))
		header.WithdrawalsHash = &wRoot
	}

	// add cancun specific fields
	if p.Version >= enginev.DataVersionCancun {
		header.BlobGasUsed = &p.BlobGasUsed
		header.ExcessBlobGas = &p.ExcessBlobGas
		header.ParentBeaconRoot = &parentBeaconBlockRoot
	}

	// add prague specific fields
	if p.Version >= enginev.DataVersionPrague {
		reqBytes := make([][]byte, len(executionRequests))
		for i, req := range executionRequests {
			reqBytes[i] = req
		}

		h := types.CalcRequestsHash(reqBytes)
		header.RequestsHash = &h
	}

	// add amsterdam specific fields
	if p.Version >= enginev.DataVersionAmsterdam {
		sn := p.SlotNumber
		header.SlotNumber = &sn

		if len(p.BlockAccessList) > 0 {
			h := crypto.Keccak256Hash(p.BlockAccessList)
			header.BlockAccessListHash = &h
		}
	}

	return header, nil
}
