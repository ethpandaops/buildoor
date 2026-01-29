package legacybuilder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
)

// ValidatorRegistration represents a validator registration from a relay.
type ValidatorRegistration struct {
	Slot           phase0.Slot
	ValidatorIndex phase0.ValidatorIndex
	Entry          ValidatorRegistrationEntry
}

// ValidatorRegistrationEntry is the registration entry from the relay response.
type ValidatorRegistrationEntry struct {
	Message   ValidatorRegistrationMessage `json:"message"`
	Signature string                       `json:"signature"`
}

// ValidatorRegistrationMessage is the registration message from the relay.
type ValidatorRegistrationMessage struct {
	FeeRecipient string `json:"fee_recipient"`
	GasLimit     string `json:"gas_limit"`
	Timestamp    string `json:"timestamp"`
	Pubkey       string `json:"pubkey"`
}

// validatorRegistrationJSON is the raw JSON format from relay API.
type validatorRegistrationJSON struct {
	Slot           string                     `json:"slot"`
	ValidatorIndex string                     `json:"validator_index"`
	Entry          ValidatorRegistrationEntry `json:"entry"`
}

// RelaySubmitResult contains the result of a block submission to a single relay.
type RelaySubmitResult struct {
	RelayURL string
	Success  bool
	Error    string
}

// RelayClient is an HTTP client for interacting with relay APIs.
type RelayClient struct {
	relayURLs  []string
	httpClient *http.Client
	log        logrus.FieldLogger
}

// NewRelayClient creates a new relay client.
func NewRelayClient(relayURLs []string, log logrus.FieldLogger) *RelayClient {
	return &RelayClient{
		relayURLs: relayURLs,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		log: log.WithField("component", "relay-client"),
	}
}

// GetValidatorRegistrations polls all relays for validator registrations and merges results.
func (c *RelayClient) GetValidatorRegistrations(ctx context.Context) ([]ValidatorRegistration, error) {
	type result struct {
		regs []ValidatorRegistration
		err  error
	}

	results := make(chan result, len(c.relayURLs))

	for _, relayURL := range c.relayURLs {
		go func(url string) {
			regs, err := c.getValidatorsFromRelay(ctx, url)
			results <- result{regs: regs, err: err}
		}(relayURL)
	}

	// Merge results, dedup by slot
	merged := make(map[phase0.Slot]ValidatorRegistration, 64)

	for range c.relayURLs {
		r := <-results
		if r.err != nil {
			c.log.WithError(r.err).Warn("Failed to get validators from relay")
			continue
		}

		for _, reg := range r.regs {
			merged[reg.Slot] = reg
		}
	}

	regs := make([]ValidatorRegistration, 0, len(merged))
	for _, reg := range merged {
		regs = append(regs, reg)
	}

	return regs, nil
}

// getValidatorsFromRelay fetches validator registrations from a single relay.
func (c *RelayClient) getValidatorsFromRelay(
	ctx context.Context,
	relayURL string,
) ([]ValidatorRegistration, error) {
	url := relayURL + "/relay/v1/builder/validators"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to %s failed: %w", relayURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("relay %s returned status %d: %s", relayURL, resp.StatusCode, string(body))
	}

	var rawRegs []validatorRegistrationJSON
	if err := json.NewDecoder(resp.Body).Decode(&rawRegs); err != nil {
		return nil, fmt.Errorf("failed to decode response from %s: %w", relayURL, err)
	}

	regs := make([]ValidatorRegistration, 0, len(rawRegs))

	for _, raw := range rawRegs {
		slot, err := strconv.ParseUint(raw.Slot, 10, 64)
		if err != nil {
			c.log.WithField("slot", raw.Slot).Warn("Invalid slot in validator registration")
			continue
		}

		valIdx, err := strconv.ParseUint(raw.ValidatorIndex, 10, 64)
		if err != nil {
			c.log.WithField("validator_index", raw.ValidatorIndex).Warn("Invalid validator index")
			continue
		}

		regs = append(regs, ValidatorRegistration{
			Slot:           phase0.Slot(slot),
			ValidatorIndex: phase0.ValidatorIndex(valIdx),
			Entry:          raw.Entry,
		})
	}

	c.log.WithFields(logrus.Fields{
		"relay": relayURL,
		"count": len(regs),
	}).Debug("Fetched validator registrations")

	return regs, nil
}

// SubmitBlock submits a block to all relays concurrently.
func (c *RelayClient) SubmitBlock(ctx context.Context, submission []byte, consensusVersion string) []RelaySubmitResult {
	var wg sync.WaitGroup

	results := make([]RelaySubmitResult, len(c.relayURLs))

	for i, relayURL := range c.relayURLs {
		wg.Add(1)

		go func(idx int, url string) {
			defer wg.Done()

			results[idx] = c.submitBlockToRelay(ctx, url, submission, consensusVersion)
		}(i, relayURL)
	}

	wg.Wait()

	return results
}

// submitBlockToRelay submits a block to a single relay.
func (c *RelayClient) submitBlockToRelay(
	ctx context.Context,
	relayURL string,
	submission []byte,
	consensusVersion string,
) RelaySubmitResult {
	url := relayURL + "/relay/v1/builder/blocks"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(submission))
	if err != nil {
		return RelaySubmitResult{
			RelayURL: relayURL,
			Error:    fmt.Sprintf("failed to create request: %v", err),
		}
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Eth-Consensus-Version", consensusVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return RelaySubmitResult{
			RelayURL: relayURL,
			Error:    fmt.Sprintf("request failed: %v", err),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return RelaySubmitResult{
			RelayURL: relayURL,
			Error:    fmt.Sprintf("relay returned status %d: %s", resp.StatusCode, string(body)),
		}
	}

	return RelaySubmitResult{
		RelayURL: relayURL,
		Success:  true,
	}
}
