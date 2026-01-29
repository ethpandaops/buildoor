package legacybuilder

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/signer"
)

// DomainApplicationBuilder is the domain type for builder API signatures.
var DomainApplicationBuilder = phase0.DomainType{0x00, 0x00, 0x00, 0x01}

// BidTrace contains the fields for a builder bid submission.
type BidTrace struct {
	Slot                 uint64
	ParentHash           [32]byte
	BlockHash            [32]byte
	BuilderPubkey        [48]byte
	ProposerPubkey       [48]byte
	ProposerFeeRecipient [20]byte
	GasLimit             uint64
	GasUsed              uint64
	Value                *big.Int // uint256, wei
}

// BidTraceJSON is the JSON representation of BidTrace for relay submission.
type BidTraceJSON struct {
	Slot                 string `json:"slot"`
	ParentHash           string `json:"parent_hash"`
	BlockHash            string `json:"block_hash"`
	BuilderPubkey        string `json:"builder_pubkey"`
	ProposerPubkey       string `json:"proposer_pubkey"`
	ProposerFeeRecipient string `json:"proposer_fee_recipient"`
	GasLimit             string `json:"gas_limit"`
	GasUsed              string `json:"gas_used"`
	Value                string `json:"value"`
}

// BlockSubmission is the complete submission to a relay.
type BlockSubmission struct {
	Message           *BidTraceJSON   `json:"message"`
	ExecutionPayload  json.RawMessage `json:"execution_payload"`
	ExecutionRequests json.RawMessage `json:"execution_requests,omitempty"`
	BlobsBundle       json.RawMessage `json:"blobs_bundle,omitempty"`
	Signature         string          `json:"signature"`
}

// BlockSubmitter handles BidTrace SSZ hashing, BLS signing, and relay submission.
type BlockSubmitter struct {
	blsSigner             *signer.BLSSigner
	relayClient           *RelayClient
	genesisValidatorsRoot phase0.Root
	genesisForkVersion    phase0.Version
	builderPubkey         phase0.BLSPubKey
	log                   logrus.FieldLogger
}

// NewBlockSubmitter creates a new block submitter.
func NewBlockSubmitter(
	blsSigner *signer.BLSSigner,
	relayClient *RelayClient,
	genesisValidatorsRoot phase0.Root,
	genesisForkVersion phase0.Version,
	builderPubkey phase0.BLSPubKey,
	log logrus.FieldLogger,
) *BlockSubmitter {
	return &BlockSubmitter{
		blsSigner:             blsSigner,
		relayClient:           relayClient,
		genesisValidatorsRoot: genesisValidatorsRoot,
		genesisForkVersion:    genesisForkVersion,
		builderPubkey:         builderPubkey,
		log:                   log.WithField("component", "block-submitter"),
	}
}

// Submit assembles a block submission, signs it, and submits to all relays.
func (s *BlockSubmitter) Submit(
	ctx context.Context,
	trace *BidTrace,
	executionPayload json.RawMessage,
	blobsBundle json.RawMessage,
	executionRequests [][]byte,
	consensusVersion string,
) ([]RelaySubmitResult, error) {
	// Compute BidTrace hash tree root
	root := computeBidTraceRoot(trace)

	// Compute domain for signing
	domain := signer.ComputeDomain(
		DomainApplicationBuilder,
		s.genesisForkVersion,
		s.genesisValidatorsRoot,
	)

	// Sign the bid trace root with domain
	var rootPhase0 phase0.Root
	copy(rootPhase0[:], root[:])

	sig, err := s.blsSigner.SignWithDomain(rootPhase0, domain)
	if err != nil {
		return nil, fmt.Errorf("failed to sign bid trace: %w", err)
	}

	// Convert execution payload from engine API format (camelCase) to builder API format (snake_case)
	builderPayload, err := convertPayloadToBuilderAPI(executionPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to convert payload to builder API format: %w", err)
	}

	// Build JSON submission
	submission := &BlockSubmission{
		Message: &BidTraceJSON{
			Slot:                 fmt.Sprintf("%d", trace.Slot),
			ParentHash:           fmt.Sprintf("0x%x", trace.ParentHash),
			BlockHash:            fmt.Sprintf("0x%x", trace.BlockHash),
			BuilderPubkey:        fmt.Sprintf("0x%x", trace.BuilderPubkey),
			ProposerPubkey:       fmt.Sprintf("0x%x", trace.ProposerPubkey),
			ProposerFeeRecipient: fmt.Sprintf("0x%x", trace.ProposerFeeRecipient),
			GasLimit:             fmt.Sprintf("%d", trace.GasLimit),
			GasUsed:              fmt.Sprintf("%d", trace.GasUsed),
			Value:                trace.Value.String(),
		},
		ExecutionPayload: builderPayload,
		Signature:        fmt.Sprintf("0x%x", sig[:]),
	}

	// Convert execution requests from engine API format (type-prefixed SSZ)
	// to builder API format (structured object with deposits/withdrawals/consolidations).
	if executionRequests != nil {
		reqJSON, err := convertExecutionRequestsToBuilderAPI(executionRequests)
		if err != nil {
			return nil, fmt.Errorf("failed to convert execution requests: %w", err)
		}

		submission.ExecutionRequests = reqJSON
	}

	if len(blobsBundle) > 0 {
		submission.BlobsBundle = blobsBundle
	}

	submissionJSON, err := json.Marshal(submission)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal block submission: %w", err)
	}

	s.log.WithFields(logrus.Fields{
		"slot":              trace.Slot,
		"block_hash":        fmt.Sprintf("0x%x", trace.BlockHash[:8]),
		"value":             trace.Value.String(),
		"consensus_version": consensusVersion,
	}).Info("Submitting block to relays")

	// Submit to all relays
	results := s.relayClient.SubmitBlock(ctx, submissionJSON, consensusVersion)

	return results, nil
}

// computeBidTraceRoot computes the SSZ hash tree root of a BidTrace.
// BidTrace has 9 fields, padded to 16 leaves for Merkle tree.
//
// Fields:
//  1. Slot (uint64)
//  2. ParentHash (Bytes32)
//  3. BlockHash (Bytes32)
//  4. BuilderPubkey (BLSPubKey, 48 bytes)
//  5. ProposerPubkey (BLSPubKey, 48 bytes)
//  6. ProposerFeeRecipient (ExecutionAddress, 20 bytes)
//  7. GasLimit (uint64)
//  8. GasUsed (uint64)
//  9. Value (uint256)
func computeBidTraceRoot(trace *BidTrace) [32]byte {
	var leaves [16][32]byte

	// 1. Slot: uint64 LE padded to 32 bytes
	binary.LittleEndian.PutUint64(leaves[0][:8], trace.Slot)

	// 2. ParentHash: already 32 bytes
	leaves[1] = trace.ParentHash

	// 3. BlockHash: already 32 bytes
	leaves[2] = trace.BlockHash

	// 4. BuilderPubkey: 48 bytes -> split into 2x32 chunks, hash
	leaves[3] = hashBLSPubkey(trace.BuilderPubkey)

	// 5. ProposerPubkey: 48 bytes -> split into 2x32 chunks, hash
	leaves[4] = hashBLSPubkey(trace.ProposerPubkey)

	// 6. ProposerFeeRecipient: 20 bytes padded to 32
	copy(leaves[5][:20], trace.ProposerFeeRecipient[:])

	// 7. GasLimit: uint64 LE padded to 32 bytes
	binary.LittleEndian.PutUint64(leaves[6][:8], trace.GasLimit)

	// 8. GasUsed: uint64 LE padded to 32 bytes
	binary.LittleEndian.PutUint64(leaves[7][:8], trace.GasUsed)

	// 9. Value: uint256 LE padded to 32 bytes
	if trace.Value != nil {
		b := trace.Value.Bytes()
		// Convert big-endian to little-endian
		for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
			b[i], b[j] = b[j], b[i]
		}

		copy(leaves[8][:], b)
	}

	// Leaves 9-15 are zero (padding to next power of 2)

	// Build 4-level Merkle tree (16 leaves)
	return merkleRoot(leaves[:])
}

// hashBLSPubkey hashes a 48-byte BLS public key into a 32-byte SSZ leaf.
// The key is split into two 32-byte chunks (padded), then hashed together.
func hashBLSPubkey(pubkey [48]byte) [32]byte {
	var chunks [64]byte

	copy(chunks[:32], pubkey[:32])
	copy(chunks[32:48], pubkey[32:])
	// remaining bytes are already zero

	return sha256.Sum256(chunks[:])
}

// hexToDecimal converts a hex string (with or without 0x prefix) to a decimal string.
// Returns an empty string if the input is empty.
func hexToDecimal(hexStr string) (string, error) {
	if hexStr == "" {
		return "", nil
	}

	cleaned := strings.TrimPrefix(hexStr, "0x")

	val, err := strconv.ParseUint(cleaned, 16, 64)
	if err != nil {
		return "", fmt.Errorf("failed to parse hex %q: %w", hexStr, err)
	}

	return strconv.FormatUint(val, 10), nil
}

// convertPayloadToBuilderAPI converts an execution payload from engine API format
// (camelCase, hex numerics) to builder API format (snake_case, decimal numerics) as expected by relays.
func convertPayloadToBuilderAPI(enginePayload json.RawMessage) (json.RawMessage, error) {
	// Engine API format (camelCase)
	var engine struct {
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

	if err := json.Unmarshal(enginePayload, &engine); err != nil {
		return nil, fmt.Errorf("failed to unmarshal engine payload: %w", err)
	}

	// Convert numeric fields from hex to decimal
	blockNumber, err := hexToDecimal(engine.BlockNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to convert block_number: %w", err)
	}

	gasLimit, err := hexToDecimal(engine.GasLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to convert gas_limit: %w", err)
	}

	gasUsed, err := hexToDecimal(engine.GasUsed)
	if err != nil {
		return nil, fmt.Errorf("failed to convert gas_used: %w", err)
	}

	timestamp, err := hexToDecimal(engine.Timestamp)
	if err != nil {
		return nil, fmt.Errorf("failed to convert timestamp: %w", err)
	}

	baseFeePerGas, err := hexToDecimal(engine.BaseFeePerGas)
	if err != nil {
		return nil, fmt.Errorf("failed to convert base_fee_per_gas: %w", err)
	}

	blobGasUsed, err := hexToDecimal(engine.BlobGasUsed)
	if err != nil {
		return nil, fmt.Errorf("failed to convert blob_gas_used: %w", err)
	}

	excessBlobGas, err := hexToDecimal(engine.ExcessBlobGas)
	if err != nil {
		return nil, fmt.Errorf("failed to convert excess_blob_gas: %w", err)
	}

	// Convert withdrawals from engine format to builder format
	var builderWithdrawals json.RawMessage

	if len(engine.Withdrawals) > 0 {
		var engineWithdrawals []struct {
			Index          string `json:"index"`
			ValidatorIndex string `json:"validatorIndex"`
			Address        string `json:"address"`
			Amount         string `json:"amount"`
		}

		if err := json.Unmarshal(engine.Withdrawals, &engineWithdrawals); err != nil {
			return nil, fmt.Errorf("failed to unmarshal withdrawals: %w", err)
		}

		type builderWithdrawal struct {
			Index          string `json:"index"`
			ValidatorIndex string `json:"validator_index"`
			Address        string `json:"address"`
			Amount         string `json:"amount"`
		}

		bw := make([]builderWithdrawal, len(engineWithdrawals))
		for i, w := range engineWithdrawals {
			idx, err := hexToDecimal(w.Index)
			if err != nil {
				return nil, fmt.Errorf("failed to convert withdrawal index: %w", err)
			}

			valIdx, err := hexToDecimal(w.ValidatorIndex)
			if err != nil {
				return nil, fmt.Errorf("failed to convert withdrawal validator_index: %w", err)
			}

			amount, err := hexToDecimal(w.Amount)
			if err != nil {
				return nil, fmt.Errorf("failed to convert withdrawal amount: %w", err)
			}

			bw[i] = builderWithdrawal{
				Index:          idx,
				ValidatorIndex: valIdx,
				Address:        w.Address,
				Amount:         amount,
			}
		}

		var err error

		builderWithdrawals, err = json.Marshal(bw)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal builder withdrawals: %w", err)
		}
	}

	// Builder API format (snake_case with decimal numerics)
	builder := struct {
		ParentHash    string          `json:"parent_hash"`
		FeeRecipient  string          `json:"fee_recipient"`
		StateRoot     string          `json:"state_root"`
		ReceiptsRoot  string          `json:"receipts_root"`
		LogsBloom     string          `json:"logs_bloom"`
		PrevRandao    string          `json:"prev_randao"`
		BlockNumber   string          `json:"block_number"`
		GasLimit      string          `json:"gas_limit"`
		GasUsed       string          `json:"gas_used"`
		Timestamp     string          `json:"timestamp"`
		ExtraData     string          `json:"extra_data"`
		BaseFeePerGas string          `json:"base_fee_per_gas"`
		BlockHash     string          `json:"block_hash"`
		Transactions  []string        `json:"transactions"`
		Withdrawals   json.RawMessage `json:"withdrawals"`
		BlobGasUsed   string          `json:"blob_gas_used,omitempty"`
		ExcessBlobGas string          `json:"excess_blob_gas,omitempty"`
	}{
		ParentHash:    engine.ParentHash,
		FeeRecipient:  engine.FeeRecipient,
		StateRoot:     engine.StateRoot,
		ReceiptsRoot:  engine.ReceiptsRoot,
		LogsBloom:     engine.LogsBloom,
		PrevRandao:    engine.PrevRandao,
		BlockNumber:   blockNumber,
		GasLimit:      gasLimit,
		GasUsed:       gasUsed,
		Timestamp:     timestamp,
		ExtraData:     engine.ExtraData,
		BaseFeePerGas: baseFeePerGas,
		BlockHash:     engine.BlockHash,
		Transactions:  engine.Transactions,
		Withdrawals:   builderWithdrawals,
		BlobGasUsed:   blobGasUsed,
		ExcessBlobGas: excessBlobGas,
	}

	result, err := json.Marshal(builder)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal builder payload: %w", err)
	}

	return result, nil
}

// Execution request type prefixes (EIP-7685).
const (
	depositRequestType       = 0x00
	withdrawalRequestType    = 0x01
	consolidationRequestType = 0x02

	depositRequestSSZSize       = 192 // 48+32+8+96+8
	withdrawalRequestSSZSize    = 76  // 20+48+8
	consolidationRequestSSZSize = 116 // 20+48+48
)

// depositRequestJSON is the beacon API format for a deposit request.
type depositRequestJSON struct {
	Pubkey                string `json:"pubkey"`
	WithdrawalCredentials string `json:"withdrawal_credentials"`
	Amount                string `json:"amount"`
	Signature             string `json:"signature"`
	Index                 string `json:"index"`
}

// withdrawalRequestJSON is the beacon API format for a withdrawal request.
type withdrawalRequestJSON struct {
	SourceAddress   string `json:"source_address"`
	ValidatorPubkey string `json:"validator_pubkey"`
	Amount          string `json:"amount"`
}

// consolidationRequestJSON is the beacon API format for a consolidation request.
type consolidationRequestJSON struct {
	SourceAddress string `json:"source_address"`
	SourcePubkey  string `json:"source_pubkey"`
	TargetPubkey  string `json:"target_pubkey"`
}

// executionRequestsBuilderJSON is the builder API format for execution requests.
type executionRequestsBuilderJSON struct {
	Deposits       []depositRequestJSON       `json:"deposits"`
	Withdrawals    []withdrawalRequestJSON    `json:"withdrawals"`
	Consolidations []consolidationRequestJSON `json:"consolidations"`
}

// convertExecutionRequestsToBuilderAPI converts execution requests from engine API
// format (type-prefixed concatenated SSZ per EIP-7685) to the builder/beacon API
// format (structured object with deposits, withdrawals, consolidations arrays).
func convertExecutionRequestsToBuilderAPI(requests [][]byte) (json.RawMessage, error) {
	result := executionRequestsBuilderJSON{
		Deposits:       make([]depositRequestJSON, 0),
		Withdrawals:    make([]withdrawalRequestJSON, 0),
		Consolidations: make([]consolidationRequestJSON, 0),
	}

	for _, reqData := range requests {
		if len(reqData) < 1 {
			continue
		}

		reqType := reqData[0]
		data := reqData[1:]

		switch reqType {
		case depositRequestType:
			for i := 0; i+depositRequestSSZSize <= len(data); i += depositRequestSSZSize {
				d := data[i : i+depositRequestSSZSize]
				result.Deposits = append(result.Deposits, depositRequestJSON{
					Pubkey:                "0x" + hex.EncodeToString(d[0:48]),
					WithdrawalCredentials: "0x" + hex.EncodeToString(d[48:80]),
					Amount:                strconv.FormatUint(binary.LittleEndian.Uint64(d[80:88]), 10),
					Signature:             "0x" + hex.EncodeToString(d[88:184]),
					Index:                 strconv.FormatUint(binary.LittleEndian.Uint64(d[184:192]), 10),
				})
			}

		case withdrawalRequestType:
			for i := 0; i+withdrawalRequestSSZSize <= len(data); i += withdrawalRequestSSZSize {
				d := data[i : i+withdrawalRequestSSZSize]
				result.Withdrawals = append(result.Withdrawals, withdrawalRequestJSON{
					SourceAddress:   "0x" + hex.EncodeToString(d[0:20]),
					ValidatorPubkey: "0x" + hex.EncodeToString(d[20:68]),
					Amount:          strconv.FormatUint(binary.LittleEndian.Uint64(d[68:76]), 10),
				})
			}

		case consolidationRequestType:
			for i := 0; i+consolidationRequestSSZSize <= len(data); i += consolidationRequestSSZSize {
				d := data[i : i+consolidationRequestSSZSize]
				result.Consolidations = append(result.Consolidations, consolidationRequestJSON{
					SourceAddress: "0x" + hex.EncodeToString(d[0:20]),
					SourcePubkey:  "0x" + hex.EncodeToString(d[20:68]),
					TargetPubkey:  "0x" + hex.EncodeToString(d[68:116]),
				})
			}
		}
	}

	return json.Marshal(result)
}

// merkleRoot computes the Merkle root of the given leaves using SHA-256.
func merkleRoot(leaves [][32]byte) [32]byte {
	if len(leaves) == 1 {
		return leaves[0]
	}

	// Pair up and hash
	next := make([][32]byte, len(leaves)/2)

	for i := 0; i < len(leaves); i += 2 {
		var combined [64]byte

		copy(combined[:32], leaves[i][:])
		copy(combined[32:], leaves[i+1][:])

		next[i/2] = sha256.Sum256(combined[:])
	}

	return merkleRoot(next)
}
