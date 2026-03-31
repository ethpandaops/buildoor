package beacon

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/attestantio/go-eth2-client/spec/phase0"
)

// ChainStatusResult holds the data needed to populate a libp2p StatusV2 message.
type ChainStatusResult struct {
	ForkDigest            [4]byte
	FinalizedRoot         phase0.Root
	FinalizedEpoch        uint64
	HeadRoot              phase0.Root
	HeadSlot              uint64
	// EarliestAvailableSlot is the earliest slot we can serve data for.
	// As a builder (not a full archive node) we set this to 0.
	EarliestAvailableSlot uint64
}

// GetChainStatus fetches the current chain status from the beacon node.
// It queries the head block header, finality checkpoints, and current fork version
// to build a StatusV2 message suitable for the Fulu/Gloas p2p Status RPC.
func (c *Client) GetChainStatus(ctx context.Context) (*ChainStatusResult, error) {
	head, err := c.getHeadBlockHeader(ctx)
	if err != nil {
		return nil, fmt.Errorf("get head block header: %w", err)
	}

	finality, err := c.getFinalityCheckpoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("get finality checkpoints: %w", err)
	}

	forkVersion, err := c.getCurrentForkVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("get current fork version: %w", err)
	}

	genesis, err := c.GetGenesis(ctx)
	if err != nil {
		return nil, fmt.Errorf("get genesis: %w", err)
	}

	// ForkDigest = compute_fork_data_root(current_version, genesis_validators_root)[:4]
	forkData := &phase0.ForkData{
		CurrentVersion:        forkVersion,
		GenesisValidatorsRoot: genesis.GenesisValidatorsRoot,
	}

	forkDataRoot, err := forkData.HashTreeRoot()
	if err != nil {
		return nil, fmt.Errorf("compute fork data root: %w", err)
	}

	var forkDigest [4]byte
	copy(forkDigest[:], forkDataRoot[:4])

	return &ChainStatusResult{
		ForkDigest:            forkDigest,
		FinalizedRoot:         finality.FinalizedRoot,
		FinalizedEpoch:        finality.FinalizedEpoch,
		HeadRoot:              head.Root,
		HeadSlot:              head.Slot,
		EarliestAvailableSlot: 0, // builder doesn't serve historical data
	}, nil
}

type headBlockHeader struct {
	Root phase0.Root
	Slot uint64
}

func (c *Client) getHeadBlockHeader(ctx context.Context) (*headBlockHeader, error) {
	url := fmt.Sprintf("%s/eth/v1/beacon/headers/head", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data struct {
			Root   string `json:"root"`
			Header struct {
				Message struct {
					Slot string `json:"slot"`
				} `json:"message"`
			} `json:"header"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	root, err := parseHexRoot(result.Data.Root)
	if err != nil {
		return nil, fmt.Errorf("parse root: %w", err)
	}

	slot, err := strconv.ParseUint(result.Data.Header.Message.Slot, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse slot: %w", err)
	}

	return &headBlockHeader{Root: root, Slot: slot}, nil
}

type finalityCheckpoints struct {
	FinalizedRoot  phase0.Root
	FinalizedEpoch uint64
}

func (c *Client) getFinalityCheckpoints(ctx context.Context) (*finalityCheckpoints, error) {
	url := fmt.Sprintf("%s/eth/v1/beacon/states/head/finality_checkpoints", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data struct {
			Finalized struct {
				Epoch string `json:"epoch"`
				Root  string `json:"root"`
			} `json:"finalized"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	root, err := parseHexRoot(result.Data.Finalized.Root)
	if err != nil {
		return nil, fmt.Errorf("parse finalized root: %w", err)
	}

	epoch, err := strconv.ParseUint(result.Data.Finalized.Epoch, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse finalized epoch: %w", err)
	}

	return &finalityCheckpoints{FinalizedRoot: root, FinalizedEpoch: epoch}, nil
}

func (c *Client) getCurrentForkVersion(ctx context.Context) (phase0.Version, error) {
	url := fmt.Sprintf("%s/eth/v1/beacon/states/head/fork", c.baseURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return phase0.Version{}, fmt.Errorf("create request: %w", err)
	}

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return phase0.Version{}, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return phase0.Version{}, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data struct {
			CurrentVersion string `json:"current_version"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return phase0.Version{}, fmt.Errorf("decode response: %w", err)
	}

	b, err := hex.DecodeString(strings.TrimPrefix(result.Data.CurrentVersion, "0x"))
	if err != nil {
		return phase0.Version{}, fmt.Errorf("decode fork version hex: %w", err)
	}

	if len(b) != 4 {
		return phase0.Version{}, fmt.Errorf("invalid fork version length: %d", len(b))
	}

	var v phase0.Version
	copy(v[:], b)

	return v, nil
}

func parseHexRoot(s string) (phase0.Root, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		return phase0.Root{}, err
	}

	if len(b) != 32 {
		return phase0.Root{}, fmt.Errorf("expected 32 bytes, got %d", len(b))
	}

	var r phase0.Root
	copy(r[:], b)

	return r, nil
}
