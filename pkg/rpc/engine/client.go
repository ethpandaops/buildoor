// Package engine provides clients for interacting with Ethereum execution layer engine API.
package engine

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/golang-jwt/jwt/v5"
	"github.com/sirupsen/logrus"
)

// PayloadID is an 8-byte identifier for a payload being built.
type PayloadID [8]byte

// UnmarshalJSON implements json.Unmarshaler for PayloadID.
// Handles hex string format like "0x0123456789abcdef".
func (p *PayloadID) UnmarshalJSON(data []byte) error {
	// Remove quotes
	s := strings.Trim(string(data), "\"")
	s = strings.TrimPrefix(s, "0x")

	b, err := hex.DecodeString(s)
	if err != nil {
		return fmt.Errorf("invalid payload ID hex: %w", err)
	}

	if len(b) != 8 {
		return fmt.Errorf("invalid payload ID length: got %d, want 8", len(b))
	}

	copy(p[:], b)

	return nil
}

// PayloadAttributes contains the attributes for building a new payload.
type PayloadAttributes struct {
	Timestamp             uint64
	PrevRandao            common.Hash
	SuggestedFeeRecipient common.Address
	Withdrawals           []*types.Withdrawal
	ParentBeaconBlockRoot *common.Hash
}

// ExecutionPayload represents an execution layer payload (typed, with JSON marshal/unmarshal for API).
type ExecutionPayload struct {
	ParentHash       common.Hash
	FeeRecipient     common.Address
	StateRoot        common.Hash
	ReceiptsRoot     common.Hash
	LogsBloom        types.Bloom
	PrevRandao       common.Hash
	BlockNumber      uint64
	GasLimit         uint64
	GasUsed          uint64
	Timestamp        uint64
	ExtraData        []byte
	BaseFeePerGas    *big.Int
	BlockHash        common.Hash
	Transactions     [][]byte
	Withdrawals      []*types.Withdrawal
	BlobGasUsed      uint64
	ExcessBlobGas    uint64
	ParentBeaconRoot *common.Hash
}

// executionPayloadJSON is the Engine API JSON shape for executionPayload (used for marshal/unmarshal).
type executionPayloadJSON struct {
	ParentHash       common.Hash         `json:"parentHash"`
	FeeRecipient     common.Address      `json:"feeRecipient"`
	StateRoot        common.Hash         `json:"stateRoot"`
	ReceiptsRoot     common.Hash         `json:"receiptsRoot"`
	LogsBloom        types.Bloom         `json:"logsBloom"`
	PrevRandao       common.Hash         `json:"prevRandao"`
	BlockNumber      hexutil.Uint64      `json:"blockNumber"`
	GasLimit         hexutil.Uint64      `json:"gasLimit"`
	GasUsed          hexutil.Uint64      `json:"gasUsed"`
	Timestamp        hexutil.Uint64      `json:"timestamp"`
	ExtraData        hexutil.Bytes       `json:"extraData"`
	BaseFeePerGas    *hexutil.Big        `json:"baseFeePerGas"`
	BlockHash        common.Hash         `json:"blockHash"`
	Transactions     []hexutil.Bytes     `json:"transactions"`
	Withdrawals      []*types.Withdrawal `json:"withdrawals"`
	BlobGasUsed      hexutil.Uint64      `json:"blobGasUsed,omitempty"`
	ExcessBlobGas    hexutil.Uint64      `json:"excessBlobGas,omitempty"`
	ParentBeaconRoot *common.Hash        `json:"parentBeaconBlockRoot,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler for ExecutionPayload.
func (p *ExecutionPayload) UnmarshalJSON(data []byte) error {
	var j executionPayloadJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}
	p.ParentHash = j.ParentHash
	p.FeeRecipient = j.FeeRecipient
	p.StateRoot = j.StateRoot
	p.ReceiptsRoot = j.ReceiptsRoot
	p.LogsBloom = j.LogsBloom
	p.PrevRandao = j.PrevRandao
	p.BlockNumber = uint64(j.BlockNumber)
	p.GasLimit = uint64(j.GasLimit)
	p.GasUsed = uint64(j.GasUsed)
	p.Timestamp = uint64(j.Timestamp)
	p.ExtraData = j.ExtraData
	if j.BaseFeePerGas != nil {
		p.BaseFeePerGas = (*big.Int)(j.BaseFeePerGas)
	}
	p.BlockHash = j.BlockHash
	p.Transactions = make([][]byte, len(j.Transactions))
	for i, tx := range j.Transactions {
		p.Transactions[i] = tx
	}
	p.Withdrawals = j.Withdrawals
	p.BlobGasUsed = uint64(j.BlobGasUsed)
	p.ExcessBlobGas = uint64(j.ExcessBlobGas)
	p.ParentBeaconRoot = j.ParentBeaconRoot
	return nil
}

// MarshalJSON implements json.Marshaler for ExecutionPayload.
func (p *ExecutionPayload) MarshalJSON() ([]byte, error) {
	j := executionPayloadJSON{
		ParentHash:       p.ParentHash,
		FeeRecipient:     p.FeeRecipient,
		StateRoot:        p.StateRoot,
		ReceiptsRoot:     p.ReceiptsRoot,
		LogsBloom:        p.LogsBloom,
		PrevRandao:       p.PrevRandao,
		BlockNumber:      hexutil.Uint64(p.BlockNumber),
		GasLimit:         hexutil.Uint64(p.GasLimit),
		GasUsed:          hexutil.Uint64(p.GasUsed),
		Timestamp:        hexutil.Uint64(p.Timestamp),
		ExtraData:        p.ExtraData,
		BlockHash:        p.BlockHash,
		Transactions:     make([]hexutil.Bytes, len(p.Transactions)),
		Withdrawals:      p.Withdrawals,
		BlobGasUsed:      hexutil.Uint64(p.BlobGasUsed),
		ExcessBlobGas:    hexutil.Uint64(p.ExcessBlobGas),
		ParentBeaconRoot: p.ParentBeaconRoot,
	}
	if p.BaseFeePerGas != nil {
		j.BaseFeePerGas = (*hexutil.Big)(p.BaseFeePerGas)
	}
	for i, tx := range p.Transactions {
		j.Transactions[i] = tx
	}
	return json.Marshal(j)
}

// BlobsBundle contains the blob-related data for a payload (typed, with JSON marshal/unmarshal for API).
// Commitments and Proofs are 48-byte KZG commitments/proofs, not 32-byte hashes.
type BlobsBundle struct {
	Commitments [][]byte // KZG commitments are 48 bytes
	Proofs      [][]byte // KZG proofs are 48 bytes
	Blobs       [][]byte
}

// blobsBundleJSON is the Engine API JSON shape (hex strings).
type blobsBundleJSON struct {
	Commitments []string `json:"commitments"`
	Proofs      []string `json:"proofs"`
	Blobs       []string `json:"blobs"`
}

// UnmarshalJSON implements json.Unmarshaler for BlobsBundle.
func (b *BlobsBundle) UnmarshalJSON(data []byte) error {
	var j blobsBundleJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}
	b.Commitments = make([][]byte, len(j.Commitments))
	for i, s := range j.Commitments {
		decoded, err := hexutil.Decode(s)
		if err != nil {
			return fmt.Errorf("commitment %d: %w", i, err)
		}
		if len(decoded) != 48 {
			return fmt.Errorf("commitment %d: expected 48 bytes, got %d", i, len(decoded))
		}
		b.Commitments[i] = decoded
	}
	b.Proofs = make([][]byte, len(j.Proofs))
	for i, s := range j.Proofs {
		decoded, err := hexutil.Decode(s)
		if err != nil {
			return fmt.Errorf("proof %d: %w", i, err)
		}
		if len(decoded) != 48 {
			return fmt.Errorf("proof %d: expected 48 bytes, got %d", i, len(decoded))
		}
		b.Proofs[i] = decoded
	}
	b.Blobs = make([][]byte, len(j.Blobs))
	for i, s := range j.Blobs {
		decoded, err := hexutil.Decode(s)
		if err != nil {
			return fmt.Errorf("blob %d: %w", i, err)
		}
		b.Blobs[i] = decoded
	}
	return nil
}

// MarshalJSON implements json.Marshaler for BlobsBundle.
func (b *BlobsBundle) MarshalJSON() ([]byte, error) {
	j := blobsBundleJSON{
		Commitments: make([]string, len(b.Commitments)),
		Proofs:      make([]string, len(b.Proofs)),
		Blobs:       make([]string, len(b.Blobs)),
	}
	for i, c := range b.Commitments {
		j.Commitments[i] = hexutil.Bytes(c).String()
	}
	for i, pr := range b.Proofs {
		j.Proofs[i] = hexutil.Bytes(pr).String()
	}
	for i, blob := range b.Blobs {
		j.Blobs[i] = hexutil.Bytes(blob).String()
	}
	return json.Marshal(j)
}

// ExecutionRequests is the Electra/Fulu execution requests array from the Engine API.
type ExecutionRequests []hexutil.Bytes

// ExecutionPayloadEnvelope wraps an execution payload with additional metadata.
type ExecutionPayloadEnvelope struct {
	ExecutionPayload      *ExecutionPayload
	BlockValue            *big.Int
	BlobsBundle           *BlobsBundle
	ShouldOverrideBuilder bool
}

// Client handles JWT-authenticated Engine API calls for payload building.
type Client struct {
	rpcClient *rpc.Client
	jwtSecret []byte
	engineURL string
	log       logrus.FieldLogger
}

// NewClient creates a new Engine API client with JWT authentication.
func NewClient(
	ctx context.Context,
	engineURL string,
	jwtSecretPath string,
	log logrus.FieldLogger,
) (*Client, error) {
	clientLog := log.WithField("component", "engine-client")

	// Read JWT secret
	jwtData, err := os.ReadFile(jwtSecretPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read JWT secret: %w", err)
	}

	jwtHex := strings.TrimSpace(string(jwtData))
	jwtHex = strings.TrimPrefix(jwtHex, "0x")

	jwtSecret, err := hex.DecodeString(jwtHex)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT secret: %w", err)
	}

	if len(jwtSecret) != 32 {
		return nil, fmt.Errorf("JWT secret must be 32 bytes, got %d", len(jwtSecret))
	}

	client := &Client{
		jwtSecret: jwtSecret,
		engineURL: engineURL,
		log:       clientLog,
	}

	// Connect with JWT auth
	if err := client.connect(ctx); err != nil {
		return nil, err
	}

	return client, nil
}

// connect establishes a connection to the Engine API with JWT auth.
func (c *Client) connect(ctx context.Context) error {
	token, err := c.generateJWT()
	if err != nil {
		return fmt.Errorf("failed to generate JWT: %w", err)
	}

	rpcClient, err := rpc.DialOptions(ctx, c.engineURL,
		rpc.WithHTTPAuth(func(h http.Header) error {
			h.Set("Authorization", "Bearer "+token)
			return nil
		}),
	)
	if err != nil {
		return fmt.Errorf("failed to connect to engine API: %w", err)
	}

	c.rpcClient = rpcClient

	return nil
}

// Close closes the RPC connection.
func (c *Client) Close() {
	if c.rpcClient != nil {
		c.rpcClient.Close()
	}
}

// generateJWT creates a short-lived JWT token for engine API authentication.
func (c *Client) generateJWT() (string, error) {
	claims := jwt.MapClaims{
		"iat": time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	return token.SignedString(c.jwtSecret)
}

// call makes an authenticated RPC call to the engine API.
func (c *Client) call(ctx context.Context, method string, result any, args ...any) error {
	// Regenerate JWT for each call to ensure fresh token
	token, err := c.generateJWT()
	if err != nil {
		return fmt.Errorf("failed to generate JWT: %w", err)
	}

	// Reconnect with fresh token
	rpcClient, err := rpc.DialOptions(ctx, c.engineURL,
		rpc.WithHTTPAuth(func(h http.Header) error {
			h.Set("Authorization", "Bearer "+token)
			return nil
		}),
	)
	if err != nil {
		return fmt.Errorf("failed to connect to engine API: %w", err)
	}
	defer rpcClient.Close()

	return rpcClient.CallContext(ctx, result, method, args...)
}

// ForkchoiceState represents the forkchoice state for engine API calls.
type ForkchoiceState struct {
	HeadBlockHash      common.Hash `json:"headBlockHash"`
	SafeBlockHash      common.Hash `json:"safeBlockHash"`
	FinalizedBlockHash common.Hash `json:"finalizedBlockHash"`
}

// ForkchoiceUpdatedResponse is the response from engine_forkchoiceUpdatedV3.
type ForkchoiceUpdatedResponse struct {
	PayloadStatus PayloadStatus `json:"payloadStatus"`
	PayloadID     *PayloadID    `json:"payloadId"`
}

// PayloadStatus represents the status of a payload.
type PayloadStatus struct {
	Status          string       `json:"status"`
	LatestValidHash *common.Hash `json:"latestValidHash"`
	ValidationError *string      `json:"validationError"`
}

// RequestPayloadBuild triggers payload building without changing forkchoice.
// This uses the current head from the EL state (we don't override it).
// Returns payloadID to retrieve the payload later.
func (c *Client) RequestPayloadBuild(
	ctx context.Context,
	headBlockHash common.Hash,
	safeBlockHash common.Hash,
	finalizedBlockHash common.Hash,
	attrs *PayloadAttributes,
) (PayloadID, error) {
	state := ForkchoiceState{
		HeadBlockHash:      headBlockHash,
		SafeBlockHash:      safeBlockHash,
		FinalizedBlockHash: finalizedBlockHash,
	}

	// Convert payload attributes to engine API format
	attrsMap := map[string]any{
		"timestamp":             fmt.Sprintf("0x%x", attrs.Timestamp),
		"prevRandao":            attrs.PrevRandao.Hex(),
		"suggestedFeeRecipient": attrs.SuggestedFeeRecipient.Hex(),
	}

	// Withdrawals must always be present for post-Capella blocks (even if empty)
	withdrawals := make([]map[string]any, 0, len(attrs.Withdrawals))
	for _, w := range attrs.Withdrawals {
		withdrawals = append(withdrawals, map[string]any{
			"index":          fmt.Sprintf("0x%x", w.Index),
			"validatorIndex": fmt.Sprintf("0x%x", w.Validator),
			"address":        w.Address.Hex(),
			"amount":         fmt.Sprintf("0x%x", w.Amount),
		})
	}

	attrsMap["withdrawals"] = withdrawals

	if attrs.ParentBeaconBlockRoot != nil {
		attrsMap["parentBeaconBlockRoot"] = attrs.ParentBeaconBlockRoot.Hex()
	}

	var response ForkchoiceUpdatedResponse
	if err := c.call(ctx, "engine_forkchoiceUpdatedV3", &response, state, attrsMap); err != nil {
		return PayloadID{}, fmt.Errorf("forkchoiceUpdated failed: %w", err)
	}

	if response.PayloadStatus.Status != "VALID" && response.PayloadStatus.Status != "SYNCING" {
		return PayloadID{}, fmt.Errorf("forkchoice status: %s", response.PayloadStatus.Status)
	}

	if response.PayloadID == nil {
		return PayloadID{}, fmt.Errorf("no payload ID returned")
	}

	return *response.PayloadID, nil
}

// GetPayloadResponse is the response from engine_getPayloadV4/V5.
type GetPayloadResponse struct {
	ExecutionPayload      json.RawMessage `json:"executionPayload"`
	BlockValue            string          `json:"blockValue"`
	BlobsBundle           *BlobsBundleRaw `json:"blobsBundle"`
	ShouldOverrideBuilder bool            `json:"shouldOverrideBuilder"`
	ExecutionRequests     json.RawMessage `json:"executionRequests"` // V5+ (Electra/Fulu)
}

// BlobsBundleRaw is the raw blobs bundle from engine API.
type BlobsBundleRaw struct {
	Commitments []string `json:"commitments"`
	Proofs      []string `json:"proofs"`
	Blobs       []string `json:"blobs"`
}

// GetPayload retrieves a previously requested payload.
func (c *Client) GetPayload(ctx context.Context, payloadID PayloadID) (*ExecutionPayloadEnvelope, error) {
	var response GetPayloadResponse

	payloadIDHex := fmt.Sprintf("0x%x", payloadID[:])
	if err := c.call(ctx, "engine_getPayloadV4", &response, payloadIDHex); err != nil {
		return nil, fmt.Errorf("getPayload failed: %w", err)
	}

	// Parse block value
	blockValue := new(big.Int)
	if _, ok := blockValue.SetString(strings.TrimPrefix(response.BlockValue, "0x"), 16); !ok {
		return nil, fmt.Errorf("failed to parse block value: %s", response.BlockValue)
	}

	var payload ExecutionPayload
	if err := json.Unmarshal(response.ExecutionPayload, &payload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal execution payload: %w", err)
	}

	envelope := &ExecutionPayloadEnvelope{
		ExecutionPayload:      &payload,
		BlockValue:            blockValue,
		ShouldOverrideBuilder: response.ShouldOverrideBuilder,
	}

	c.log.WithField("payload_size", len(response.ExecutionPayload)).Debug("Received execution payload")

	// Parse blobs bundle if present
	if response.BlobsBundle != nil {
		bundle := &BlobsBundle{
			Commitments: make([][]byte, len(response.BlobsBundle.Commitments)),
			Proofs:      make([][]byte, len(response.BlobsBundle.Proofs)),
			Blobs:       make([][]byte, len(response.BlobsBundle.Blobs)),
		}

		for i, commitment := range response.BlobsBundle.Commitments {
			data, err := hexutil.Decode(commitment)
			if err != nil {
				return nil, fmt.Errorf("commitment %d: %w", i, err)
			}
			if len(data) != 48 {
				return nil, fmt.Errorf("commitment %d: expected 48 bytes, got %d", i, len(data))
			}
			bundle.Commitments[i] = data
		}

		for i, proof := range response.BlobsBundle.Proofs {
			data, err := hexutil.Decode(proof)
			if err != nil {
				return nil, fmt.Errorf("proof %d: %w", i, err)
			}
			if len(data) != 48 {
				return nil, fmt.Errorf("proof %d: expected 48 bytes, got %d", i, len(data))
			}
			bundle.Proofs[i] = data
		}

		for i, blob := range response.BlobsBundle.Blobs {
			data, err := hex.DecodeString(strings.TrimPrefix(blob, "0x"))
			if err == nil {
				bundle.Blobs[i] = data
			}
		}

		envelope.BlobsBundle = bundle
	}

	return envelope, nil
}

// GetPayloadRawResult contains typed payload, blobs, and execution requests from engine_getPayload.
type GetPayloadRawResult struct {
	ExecutionPayload  *ExecutionPayload
	BlockValue        *big.Int
	BlobsBundle       *BlobsBundle
	ExecutionRequests ExecutionRequests
}

// GetPayloadRaw retrieves a payload and returns typed ExecutionPayload, BlobsBundle, and ExecutionRequests.
// Tries V5, V4, V3 in order based on fork support.
func (c *Client) GetPayloadRaw(
	ctx context.Context,
	payloadID PayloadID,
) (*GetPayloadRawResult, error) {
	var response GetPayloadResponse

	payloadIDHex := fmt.Sprintf("0x%x", payloadID[:])

	// Try V5 first (Osaka/Fulu), fall back to V4, then V3 if unsupported
	err := c.call(ctx, "engine_getPayloadV5", &response, payloadIDHex)
	if err != nil && strings.Contains(err.Error(), "Unsupported fork") {
		c.log.Debug("engine_getPayloadV5 unsupported, trying V4")

		err = c.call(ctx, "engine_getPayloadV4", &response, payloadIDHex)
	}

	if err != nil && strings.Contains(err.Error(), "Unsupported fork") {
		c.log.Debug("engine_getPayloadV4 unsupported, trying V3")

		err = c.call(ctx, "engine_getPayloadV3", &response, payloadIDHex)
	}

	if err != nil {
		return nil, fmt.Errorf("getPayload failed: %w", err)
	}

	blockValue := new(big.Int)
	if _, ok := blockValue.SetString(strings.TrimPrefix(response.BlockValue, "0x"), 16); !ok {
		return nil, fmt.Errorf("failed to parse block value: %s", response.BlockValue)
	}

	var payload ExecutionPayload
	if err := json.Unmarshal(response.ExecutionPayload, &payload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal execution payload: %w", err)
	}

	var blobsBundle *BlobsBundle
	if response.BlobsBundle != nil {
		blobsBundle = &BlobsBundle{
			Commitments: make([][]byte, len(response.BlobsBundle.Commitments)),
			Proofs:      make([][]byte, len(response.BlobsBundle.Proofs)),
			Blobs:       make([][]byte, len(response.BlobsBundle.Blobs)),
		}
		for i, s := range response.BlobsBundle.Commitments {
			decoded, err := hexutil.Decode(s)
			if err != nil {
				return nil, fmt.Errorf("commitment %d: %w", i, err)
			}
			if len(decoded) != 48 {
				return nil, fmt.Errorf("commitment %d: expected 48 bytes, got %d", i, len(decoded))
			}
			blobsBundle.Commitments[i] = decoded
		}
		for i, s := range response.BlobsBundle.Proofs {
			decoded, err := hexutil.Decode(s)
			if err != nil {
				return nil, fmt.Errorf("proof %d: %w", i, err)
			}
			if len(decoded) != 48 {
				return nil, fmt.Errorf("proof %d: expected 48 bytes, got %d", i, len(decoded))
			}
			blobsBundle.Proofs[i] = decoded
		}
		for i, s := range response.BlobsBundle.Blobs {
			decoded, err := hexutil.Decode(s)
			if err != nil {
				return nil, fmt.Errorf("blob %d: %w", i, err)
			}
			blobsBundle.Blobs[i] = decoded
		}
	}

	var execRequests ExecutionRequests
	if len(response.ExecutionRequests) > 0 {
		if err := json.Unmarshal(response.ExecutionRequests, &execRequests); err != nil {
			return nil, fmt.Errorf("failed to unmarshal execution requests: %w", err)
		}
	}

	return &GetPayloadRawResult{
		ExecutionPayload:  &payload,
		BlockValue:        blockValue,
		BlobsBundle:       blobsBundle,
		ExecutionRequests: execRequests,
	}, nil
}

// BlockResponse represents a block from eth_getBlockByNumber.
type BlockResponse struct {
	Number           string            `json:"number"`
	Hash             string            `json:"hash"`
	ParentHash       string            `json:"parentHash"`
	StateRoot        string            `json:"stateRoot"`
	ReceiptsRoot     string            `json:"receiptsRoot"`
	LogsBloom        string            `json:"logsBloom"`
	MixHash          string            `json:"mixHash"` // prevRandao
	GasLimit         string            `json:"gasLimit"`
	GasUsed          string            `json:"gasUsed"`
	Timestamp        string            `json:"timestamp"`
	ExtraData        string            `json:"extraData"`
	BaseFeePerGas    string            `json:"baseFeePerGas"`
	Miner            string            `json:"miner"` // feeRecipient
	Transactions     []json.RawMessage `json:"transactions"`
	Withdrawals      []json.RawMessage `json:"withdrawals"`
	BlobGasUsed      string            `json:"blobGasUsed"`
	ExcessBlobGas    string            `json:"excessBlobGas"`
	WithdrawalsRoot  string            `json:"withdrawalsRoot"`
	ParentBeaconRoot string            `json:"parentBeaconBlockRoot"`
}

// GetLatestBlock fetches the latest block from the EL via eth_getBlockByNumber.
func (c *Client) GetLatestBlock(ctx context.Context) (*BlockResponse, error) {
	var block BlockResponse
	if err := c.call(ctx, "eth_getBlockByNumber", &block, "latest", false); err != nil {
		return nil, fmt.Errorf("eth_getBlockByNumber failed: %w", err)
	}

	return &block, nil
}

// GetBlockByHash fetches a block by hash from the EL.
func (c *Client) GetBlockByHash(ctx context.Context, hash common.Hash) (*BlockResponse, error) {
	var block BlockResponse
	if err := c.call(ctx, "eth_getBlockByHash", &block, hash.Hex(), false); err != nil {
		return nil, fmt.Errorf("eth_getBlockByHash failed: %w", err)
	}

	return &block, nil
}

// ParseBlockHashFromPayload extracts the block hash from a raw execution payload JSON.
func ParseBlockHashFromPayload(payloadJSON json.RawMessage) (common.Hash, error) {
	var payload struct {
		BlockHash string `json:"blockHash"`
	}

	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return common.Hash{}, fmt.Errorf("failed to unmarshal payload: %w", err)
	}

	if payload.BlockHash == "" {
		return common.Hash{}, fmt.Errorf("blockHash not found in payload")
	}

	hash := common.HexToHash(payload.BlockHash)

	return hash, nil
}

// ParsePayloadFields extracts key fields from a raw execution payload JSON.
func ParsePayloadFields(payloadJSON json.RawMessage) (*ExecutionPayloadFields, error) {
	var fields ExecutionPayloadFields
	if err := json.Unmarshal(payloadJSON, &fields); err != nil {
		return nil, fmt.Errorf("failed to unmarshal payload fields: %w", err)
	}

	return &fields, nil
}

// ExecutionPayloadFields contains parsed fields from an execution payload.
type ExecutionPayloadFields struct {
	ParentHash    string `json:"parentHash"`
	BlockHash     string `json:"blockHash"`
	FeeRecipient  string `json:"feeRecipient"`
	StateRoot     string `json:"stateRoot"`
	BlockNumber   string `json:"blockNumber"`
	GasLimit      string `json:"gasLimit"`
	GasUsed       string `json:"gasUsed"`
	Timestamp     string `json:"timestamp"`
	BaseFeePerGas string `json:"baseFeePerGas"`
}
