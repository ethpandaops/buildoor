package beacon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/attestantio/go-eth2-client/spec/phase0"
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

// SubmitExecutionPayloadEnvelope submits a signed execution payload envelope.
func (c *Client) SubmitExecutionPayloadEnvelope(ctx context.Context, envelope json.RawMessage) error {
	url := fmt.Sprintf("%s/eth/v1/beacon/execution_payload_envelope", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(envelope))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

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

// SubmitVoluntaryExit submits a signed voluntary exit to the beacon node.
func (c *Client) SubmitVoluntaryExit(ctx context.Context, exit *phase0.SignedVoluntaryExit) error {
	url := fmt.Sprintf("%s/eth/v1/beacon/pool/voluntary_exits", c.baseURL)

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

	return nil
}
