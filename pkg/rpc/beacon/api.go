package beacon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

// ExecutionPayloadBidResponse represents the response from getting a bid template.
type ExecutionPayloadBidResponse struct {
	Data json.RawMessage `json:"data"`
}

// SubmitExecutionPayloadBid submits a signed execution payload bid to the beacon node.
func (c *Client) SubmitExecutionPayloadBid(ctx context.Context, bid json.RawMessage) error {
	url := fmt.Sprintf("%s/eth/v1/beacon/execution_payload_bid", c.baseURL)

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

// signedExecutionPayloadEnvelopeContents is the stateless publish body: the signed
// envelope nested under "signed_execution_payload_envelope" alongside kzg_proofs and blobs
// (mirrors Prysm's SignedExecutionPayloadEnvelopeContents struct).
type signedExecutionPayloadEnvelopeContents struct {
	SignedExecutionPayloadEnvelope json.RawMessage `json:"signed_execution_payload_envelope"`
	KzgProofs                      []string        `json:"kzg_proofs,omitempty"`
	Blobs                          []string        `json:"blobs,omitempty"`
}

// SubmitExecutionPayloadEnvelope submits a signed execution payload envelope.
// When blobs and kzg proofs are provided they are wrapped in the
// SignedExecutionPayloadEnvelopeContents body so the beacon node can derive
// and broadcast data column sidecars; otherwise the bare signed envelope is sent.
func (c *Client) SubmitExecutionPayloadEnvelope(ctx context.Context, envelope json.RawMessage, blobs [][]byte, kzgProofs [][]byte) error {
	url := fmt.Sprintf("%s/eth/v1/beacon/execution_payload_envelopes", c.baseURL)

	var bodyJSON []byte
	var err error

	if len(blobs) > 0 {
		contents := signedExecutionPayloadEnvelopeContents{
			SignedExecutionPayloadEnvelope: envelope,
		}
		contents.Blobs = make([]string, len(blobs))
		for i, b := range blobs {
			contents.Blobs[i] = fmt.Sprintf("0x%x", b)
		}
		contents.KzgProofs = make([]string, len(kzgProofs))
		for i, p := range kzgProofs {
			contents.KzgProofs[i] = fmt.Sprintf("0x%x", p)
		}
		bodyJSON, err = json.Marshal(contents)
	} else {
		bodyJSON = envelope
	}

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
	url := fmt.Sprintf("%s/eth/v1/beacon/execution_payload_envelopes/%s", c.baseURL, blockID)

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

	exitJSON, err := json.Marshal(exit)
	if err != nil {
		return fmt.Errorf("failed to marshal the exit: %w", err)
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
