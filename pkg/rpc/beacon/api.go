package beacon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/attestantio/go-eth2-client/spec/phase0"
)

// ExecutionPayloadBidResponse represents the response from getting a bid template.
type ExecutionPayloadBidResponse struct {
	Data json.RawMessage `json:"data"`
}

// SubmitExecutionPayloadBid submits a signed execution payload bid to the beacon node.
func (c *Client) SubmitExecutionPayloadBid(ctx context.Context, bid json.RawMessage) error {
	url := fmt.Sprintf("%s/eth/v1/beacon/execution_payload/bid", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bid))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Eth-Consensus-Version", "gloas")

	httpClient := &http.Client{}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to submit bid: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)

		return fmt.Errorf("failed to submit bid: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// GetExecutionPayloadBidTemplate fetches an execution payload bid template.
func (c *Client) GetExecutionPayloadBidTemplate(
	ctx context.Context,
	slot phase0.Slot,
	builderIndex uint64,
) (json.RawMessage, error) {
	url := fmt.Sprintf("%s/eth/v1/validator/execution_payload_bid/%d/%d", c.baseURL, slot, builderIndex)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpClient := &http.Client{}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get bid template: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		return nil, fmt.Errorf("failed to get bid template: status %d: %s", resp.StatusCode, string(body))
	}

	var response ExecutionPayloadBidResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return response.Data, nil
}

// ConstructExecutionPayloadEnvelopeRequest is the request body for the construct endpoint.
// Prysm expects a flat structure with beacon_block_root, execution_payload, and execution_requests.
type ConstructExecutionPayloadEnvelopeRequest struct {
	BeaconBlockRoot   string          `json:"beacon_block_root"`
	ExecutionPayload  json.RawMessage `json:"execution_payload"`
	ExecutionRequests json.RawMessage `json:"execution_requests"`
}

// ConstructExecutionPayloadEnvelopeResponse is the response from the construct endpoint.
type ConstructExecutionPayloadEnvelopeResponse struct {
	Version string          `json:"version"`
	Data    json.RawMessage `json:"data"`
}

// ConstructExecutionPayloadEnvelope calls POST /eth/v1/builder/execution_payload_envelope
// to have the beacon node derive the state_root and return a complete ExecutionPayloadEnvelope.
func (c *Client) ConstructExecutionPayloadEnvelope(
	ctx context.Context,
	beaconBlockRoot string,
	executionPayload json.RawMessage,
	executionRequests json.RawMessage,
) (json.RawMessage, error) {
	url := fmt.Sprintf("%s/eth/v1/builder/execution_payload_envelope", c.baseURL)

	reqBody := ConstructExecutionPayloadEnvelopeRequest{
		BeaconBlockRoot:   beaconBlockRoot,
		ExecutionPayload:  executionPayload,
		ExecutionRequests: executionRequests,
	}

	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal construct request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Eth-Consensus-Version", "gloas")

	httpClient := &http.Client{}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to construct envelope: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to construct envelope: status %d: %s", resp.StatusCode, string(body))
	}

	var response ConstructExecutionPayloadEnvelopeResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode construct response: %w", err)
	}

	return response.Data, nil
}

// publishEnvelopeRequest embeds the signed envelope JSON fields and adds optional
// blobs + cell_proofs for data column broadcasting (Prysm's PublishExecutionPayloadEnvelopeRequest).
type publishEnvelopeRequest struct {
	Message    json.RawMessage `json:"message"`
	Signature  json.RawMessage `json:"signature"`
	Blobs      []string        `json:"blobs,omitempty"`
	CellProofs []string        `json:"cell_proofs,omitempty"`
}

// SubmitExecutionPayloadEnvelope submits a signed execution payload envelope.
// When blobs and cell proofs are provided, they are included so the beacon node
// can compute and broadcast data column sidecars alongside the envelope.
func (c *Client) SubmitExecutionPayloadEnvelope(ctx context.Context, envelope json.RawMessage, blobs [][]byte, cellProofs [][]byte) error {
	url := fmt.Sprintf("%s/eth/v1/beacon/execution_payload_envelope", c.baseURL)

	// Unmarshal the signed envelope to extract message and signature fields,
	// then re-marshal with blobs/cell_proofs at the same level.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(envelope, &raw); err != nil {
		return fmt.Errorf("failed to parse signed envelope: %w", err)
	}

	reqBody := publishEnvelopeRequest{
		Message:   raw["message"],
		Signature: raw["signature"],
	}

	if len(blobs) > 0 {
		reqBody.Blobs = make([]string, len(blobs))
		for i, b := range blobs {
			reqBody.Blobs[i] = fmt.Sprintf("0x%x", b)
		}
	}

	if len(cellProofs) > 0 {
		reqBody.CellProofs = make([]string, len(cellProofs))
		for i, p := range cellProofs {
			reqBody.CellProofs[i] = fmt.Sprintf("0x%x", p)
		}
	}

	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal publish request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyJSON))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Eth-Consensus-Version", "gloas")

	httpClient := &http.Client{}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to submit envelope: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)

		return fmt.Errorf("failed to submit envelope: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// GetExecutionPayloadEnvelopeTemplate fetches an execution payload envelope template.
func (c *Client) GetExecutionPayloadEnvelopeTemplate(
	ctx context.Context,
	slot phase0.Slot,
	builderIndex uint64,
) (json.RawMessage, error) {
	url := fmt.Sprintf("%s/eth/v1/validator/execution_payload_envelope/%d/%d", c.baseURL, slot, builderIndex)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpClient := &http.Client{}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get envelope template: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		return nil, fmt.Errorf("failed to get envelope template: status %d: %s", resp.StatusCode, string(body))
	}

	var response struct {
		Data json.RawMessage `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return response.Data, nil
}

// PayloadEnvelopeInfo contains key fields from a fetched execution payload envelope.
type PayloadEnvelopeInfo struct {
	Slot         phase0.Slot
	BlockRoot    phase0.Root
	BlockHash    phase0.Hash32
	BuilderIndex uint64
}

// GetExecutionPayloadEnvelope fetches the signed execution payload envelope for a block.
// The blockID can be a block root (hex), slot number, or "head"/"finalized"/"genesis".
func (c *Client) GetExecutionPayloadEnvelope(
	ctx context.Context,
	blockID string,
) (*PayloadEnvelopeInfo, error) {
	url := fmt.Sprintf("%s/eth/v1/beacon/execution_payload_envelope/%s", c.baseURL, blockID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpClient := &http.Client{}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get payload envelope: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get payload envelope: status %d: %s",
			resp.StatusCode, string(body))
	}

	var response struct {
		Data struct {
			Message struct {
				Payload struct {
					BlockHash string `json:"block_hash"`
				} `json:"payload"`
				BuilderIndex    string `json:"builder_index"`
				BeaconBlockRoot string `json:"beacon_block_root"`
				Slot            string `json:"slot"`
			} `json:"message"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	msg := &response.Data.Message

	slot, err := strconv.ParseUint(msg.Slot, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid slot: %w", err)
	}

	blockRoot, err := parseRoot(msg.BeaconBlockRoot)
	if err != nil {
		return nil, fmt.Errorf("invalid beacon_block_root: %w", err)
	}

	blockHash, err := parseHash32(msg.Payload.BlockHash)
	if err != nil {
		return nil, fmt.Errorf("invalid block_hash: %w", err)
	}

	builderIndex, err := strconv.ParseUint(msg.BuilderIndex, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid builder_index: %w", err)
	}

	return &PayloadEnvelopeInfo{
		Slot:         phase0.Slot(slot),
		BlockRoot:    blockRoot,
		BlockHash:    blockHash,
		BuilderIndex: builderIndex,
	}, nil
}

// SubmitVoluntaryExit submits a signed voluntary exit to the beacon node.
func (c *Client) SubmitVoluntaryExit(ctx context.Context, exit *phase0.SignedVoluntaryExit) error {
	url := fmt.Sprintf("%s/eth/v1/beacon/pool/voluntary_exits", c.baseURL)
	c.log.Infof("Submitting exit to %s!", url)

	exitJSON, err := json.Marshal(exit)
	if err != nil {
		return fmt.Errorf("failed to marshal exit: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(exitJSON))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to submit exit: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)

		return fmt.Errorf("failed to submit exit: status %d: %s", resp.StatusCode, string(body))
	}

	c.log.Info("Submitted exit!")

	return nil
}
