package builder

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"

	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
)

const maxExtraDataSize = 32

// fullPayloadJSON is the full execution payload as returned by the engine API.
// All fields are in engine API format (camelCase, hex-encoded numerics).
type fullPayloadJSON struct {
	ParentHash    string          `json:"parentHash"`
	FeeRecipient  string          `json:"feeRecipient"`
	StateRoot     string          `json:"stateRoot"`
	ReceiptsRoot  string          `json:"receiptsRoot"`
	LogsBloom     string          `json:"logsBloom"`
	PrevRandao    string          `json:"prevRandao"`
	BlockNumber   string          `json:"blockNumber"`
	GasLimit      string          `json:"gasLimit"`
	GasUsed       string          `json:"gasUsed"`
	Timestamp     string          `json:"timestamp"`
	ExtraData     string          `json:"extraData"`
	BaseFeePerGas string          `json:"baseFeePerGas"`
	BlockHash     string          `json:"blockHash"`
	Transactions  []string        `json:"transactions"`
	Withdrawals   json.RawMessage `json:"withdrawals"`
	BlobGasUsed   string          `json:"blobGasUsed"`
	ExcessBlobGas string          `json:"excessBlobGas"`
}

// engineWithdrawalJSON is the JSON format for withdrawals in the engine API.
type engineWithdrawalJSON struct {
	Index          string `json:"index"`
	ValidatorIndex string `json:"validatorIndex"`
	Address        string `json:"address"`
	Amount         string `json:"amount"`
}

// ModifyPayloadExtraData modifies the extraData field of an execution payload
// and recomputes the block hash. It prepends the given prefix to the existing
// extra data, truncating the original if necessary to stay within the 32-byte limit.
//
// The parentBeaconBlockRoot is required because it is part of the block header
// (and therefore affects the block hash) but is NOT included in the execution
// payload JSON from the engine API.
//
// executionRequests contains the raw EIP-7685 execution requests from the
// engine API response (Electra+). These are needed to compute the requestsHash
// header field. Pass nil for pre-Electra payloads.
//
// The function first verifies that it can correctly reconstruct the original
// block hash from the payload fields. If verification fails (e.g., due to an
// unhandled fork adding new header fields), it returns an error rather than
// producing an incorrect hash.
func ModifyPayloadExtraData(
	payloadJSON json.RawMessage,
	extraDataPrefix []byte,
	parentBeaconBlockRoot common.Hash,
	executionRequests engine.ExecutionRequests,
) (json.RawMessage, common.Hash, error) {
	// Parse the full execution payload
	var payload fullPayloadJSON
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return nil, common.Hash{}, fmt.Errorf("failed to unmarshal payload: %w", err)
	}

	// Build a types.Header from the payload fields
	header, err := buildHeaderFromPayload(&payload, parentBeaconBlockRoot, executionRequests)
	if err != nil {
		return nil, common.Hash{}, fmt.Errorf("failed to build header from payload: %w", err)
	}

	// Verify our reconstruction matches the original block hash.
	// This catches cases where the fork has added new header fields
	// that we don't handle yet.
	originalHash := common.HexToHash(payload.BlockHash)
	computedHash := header.Hash()

	if computedHash != originalHash {
		// Fallback: try toggling requestsHash.
		// The engine API may not always clearly signal whether executionRequests
		// are present (e.g., field absent vs null vs empty array). Try the
		// opposite setting to handle edge cases.
		if header.RequestsHash == nil {
			emptyReqHash := types.CalcRequestsHash(nil)
			header.RequestsHash = &emptyReqHash
		} else {
			header.RequestsHash = nil
		}

		computedHash = header.Hash()
		if computedHash != originalHash {
			return nil, common.Hash{}, fmt.Errorf(
				"hash verification failed: computed %s but payload has %s "+
					"(this may indicate an unhandled fork with new header fields)",
				computedHash.Hex(), originalHash.Hex(),
			)
		}
	}

	// Build new extra data: prefix + original (truncated to fit)
	newExtraData := make([]byte, 0, maxExtraDataSize)
	newExtraData = append(newExtraData, extraDataPrefix...)

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

	// Update the header and compute new hash
	header.Extra = newExtraData
	newHash := header.Hash()

	// Update the JSON payload preserving all existing fields
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(payloadJSON, &rawMap); err != nil {
		return nil, common.Hash{}, fmt.Errorf("failed to unmarshal payload to map: %w", err)
	}

	newExtraDataHex := "0x" + hex.EncodeToString(newExtraData)

	rawMap["extraData"], _ = json.Marshal(newExtraDataHex)
	rawMap["blockHash"], _ = json.Marshal(newHash.Hex())

	modifiedJSON, err := json.Marshal(rawMap)
	if err != nil {
		return nil, common.Hash{}, fmt.Errorf("failed to marshal modified payload: %w", err)
	}

	return modifiedJSON, newHash, nil
}

// buildHeaderFromPayload reconstructs a go-ethereum types.Header from the
// execution payload JSON fields. This includes deriving transactionsRoot and
// withdrawalsRoot from the raw arrays, computing requestsHash from execution
// requests (Electra+), and setting post-merge constants (empty uncle hash,
// zero difficulty, zero nonce).
func buildHeaderFromPayload(
	payload *fullPayloadJSON,
	parentBeaconBlockRoot common.Hash,
	executionRequests engine.ExecutionRequests,
) (*types.Header, error) {
	blockNumber, err := parseHexUint64(payload.BlockNumber)
	if err != nil {
		return nil, fmt.Errorf("invalid blockNumber: %w", err)
	}

	gasLimit, err := parseHexUint64(payload.GasLimit)
	if err != nil {
		return nil, fmt.Errorf("invalid gasLimit: %w", err)
	}

	gasUsed, err := parseHexUint64(payload.GasUsed)
	if err != nil {
		return nil, fmt.Errorf("invalid gasUsed: %w", err)
	}

	timestamp, err := parseHexUint64(payload.Timestamp)
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp: %w", err)
	}

	baseFeePerGas := new(big.Int)

	cleaned := strings.TrimPrefix(payload.BaseFeePerGas, "0x")
	if _, ok := baseFeePerGas.SetString(cleaned, 16); !ok {
		return nil, fmt.Errorf("invalid baseFeePerGas: %s", payload.BaseFeePerGas)
	}

	extraData := common.FromHex(payload.ExtraData)

	// Parse logs bloom (256 bytes)
	bloomBytes := common.FromHex(payload.LogsBloom)

	var bloom types.Bloom

	copy(bloom[:], bloomBytes)

	// Parse optional blob gas fields
	var blobGasUsed *uint64

	if payload.BlobGasUsed != "" {
		v, err := parseHexUint64(payload.BlobGasUsed)
		if err != nil {
			return nil, fmt.Errorf("invalid blobGasUsed: %w", err)
		}

		blobGasUsed = &v
	}

	var excessBlobGas *uint64

	if payload.ExcessBlobGas != "" {
		v, err := parseHexUint64(payload.ExcessBlobGas)
		if err != nil {
			return nil, fmt.Errorf("invalid excessBlobGas: %w", err)
		}

		excessBlobGas = &v
	}

	// Decode transactions and compute Merkle root
	txs := make(types.Transactions, 0, len(payload.Transactions))

	for i, txHex := range payload.Transactions {
		txBytes := common.FromHex(txHex)

		tx := new(types.Transaction)
		if err := tx.UnmarshalBinary(txBytes); err != nil {
			return nil, fmt.Errorf("failed to decode transaction %d: %w", i, err)
		}

		txs = append(txs, tx)
	}

	txRoot := types.DeriveSha(txs, trie.NewStackTrie(nil))

	// Parse withdrawals and compute Merkle root
	var withdrawalsHash *common.Hash

	if len(payload.Withdrawals) > 0 {
		var engineWithdrawals []engineWithdrawalJSON
		if err := json.Unmarshal(payload.Withdrawals, &engineWithdrawals); err != nil {
			return nil, fmt.Errorf("failed to parse withdrawals: %w", err)
		}

		ws := make(types.Withdrawals, 0, len(engineWithdrawals))

		for i, w := range engineWithdrawals {
			idx, err := parseHexUint64(w.Index)
			if err != nil {
				return nil, fmt.Errorf("invalid withdrawal %d index: %w", i, err)
			}

			valIdx, err := parseHexUint64(w.ValidatorIndex)
			if err != nil {
				return nil, fmt.Errorf("invalid withdrawal %d validatorIndex: %w", i, err)
			}

			amount, err := parseHexUint64(w.Amount)
			if err != nil {
				return nil, fmt.Errorf("invalid withdrawal %d amount: %w", i, err)
			}

			ws = append(ws, &types.Withdrawal{
				Index:     idx,
				Validator: valIdx,
				Address:   common.HexToAddress(w.Address),
				Amount:    amount,
			})
		}

		wRoot := types.DeriveSha(ws, trie.NewStackTrie(nil))
		withdrawalsHash = &wRoot
	}

	// Compute requestsHash from execution requests (EIP-7685, Electra+).
	// executionRequests is non-nil (possibly empty) when the engine API response
	// included the field, indicating Electra+ where requestsHash is always
	// required in the block header.
	var requestsHash *common.Hash

	if executionRequests != nil {
		reqBytes := make([][]byte, len(executionRequests))
		for i, req := range executionRequests {
			reqBytes[i] = req
		}
		h := types.CalcRequestsHash(reqBytes)
		requestsHash = &h
	}

	// Construct the full block header with post-merge constants
	header := &types.Header{
		ParentHash:       common.HexToHash(payload.ParentHash),
		UncleHash:        types.EmptyUncleHash,
		Coinbase:         common.HexToAddress(payload.FeeRecipient),
		Root:             common.HexToHash(payload.StateRoot),
		TxHash:           txRoot,
		ReceiptHash:      common.HexToHash(payload.ReceiptsRoot),
		Bloom:            bloom,
		Difficulty:       big.NewInt(0),
		Number:           new(big.Int).SetUint64(blockNumber),
		GasLimit:         gasLimit,
		GasUsed:          gasUsed,
		Time:             timestamp,
		Extra:            extraData,
		MixDigest:        common.HexToHash(payload.PrevRandao),
		Nonce:            types.BlockNonce{},
		BaseFee:          baseFeePerGas,
		WithdrawalsHash:  withdrawalsHash,
		BlobGasUsed:      blobGasUsed,
		ExcessBlobGas:    excessBlobGas,
		ParentBeaconRoot: &parentBeaconBlockRoot,
		RequestsHash:     requestsHash,
	}

	return header, nil
}

// parseHexUint64 parses a hex string (with or without 0x prefix) as uint64.
func parseHexUint64(s string) (uint64, error) {
	cleaned := strings.TrimPrefix(s, "0x")
	return strconv.ParseUint(cleaned, 16, 64)
}
