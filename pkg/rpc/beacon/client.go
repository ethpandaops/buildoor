// Package beacon provides a client for interacting with Ethereum consensus layer nodes.
package beacon

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	nethttp "net/http"
	"strconv"
	"strings"
	"time"

	eth2client "github.com/ethpandaops/go-eth2-client"
	"github.com/ethpandaops/go-eth2-client/api"
	"github.com/ethpandaops/go-eth2-client/http"
	"github.com/ethpandaops/go-eth2-client/spec"
	"github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	dynssz "github.com/pk910/dynamic-ssz"
	"github.com/rs/zerolog"
	"github.com/sirupsen/logrus"
)

// Genesis holds genesis information.
type Genesis struct {
	GenesisTime           time.Time
	GenesisValidatorsRoot phase0.Root
	GenesisForkVersion    phase0.Version
}

// Client wraps the consensus layer client for beacon node interactions.
type Client struct {
	client      eth2client.Service
	baseURL     string
	eventStream *EventStream
	log         logrus.FieldLogger
}

// NewClient creates a new CL client connected to the specified beacon node.
// The client allows delayed start so it can be created even when the beacon node
// is not yet reachable; callers should retry API calls until the node is ready.
func NewClient(ctx context.Context, baseURL string, log logrus.FieldLogger) (*Client, error) {
	clientLog := log.WithField("component", "cl-client")

	httpClient, err := http.New(ctx,
		http.WithAddress(baseURL),
		http.WithLogLevel(zerolog.WarnLevel),
		http.WithTimeout(30*time.Second),
		http.WithAllowDelayedStart(true),
		http.WithCustomSpecSupport(true),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	c := &Client{
		client:  httpClient,
		baseURL: baseURL,
		log:     clientLog,
	}

	c.eventStream = NewEventStream(c)

	return c, nil
}

// Close closes the client and stops the event stream.
func (c *Client) Close() {
	if c.eventStream != nil {
		c.eventStream.Stop()
	}
}

// Events returns the event stream for subscribing to beacon events.
func (c *Client) Events() *EventStream {
	return c.eventStream
}

// GetBaseURL returns the base URL of the beacon node.
func (c *Client) GetBaseURL() string {
	return c.baseURL
}

// InitGlobalSSZSpecs configures the global dynssz instance with this network's
// spec values. The go-eth2-client SSZ codecs (e.g. block.Root()) route through
// dynssz.GetGlobalDynSsz(), which otherwise defaults to mainnet preset sizes —
// producing wrong hash-tree-roots on minimal-preset networks. We reuse the spec
// map go-eth2-client already parses (SpecProvider.Spec), which is directly
// compatible with dynssz.SetGlobalSpecs.
func (c *Client) InitGlobalSSZSpecs(ctx context.Context) error {
	provider, ok := c.client.(eth2client.SpecProvider)
	if !ok {
		return fmt.Errorf("client does not support spec provider")
	}

	resp, err := provider.Spec(ctx, &api.SpecOpts{})
	if err != nil {
		return fmt.Errorf("failed to get spec: %w", err)
	}

	dynssz.SetGlobalSpecs(resp.Data)

	return nil
}

// GetRawSpecData fetches /eth/v1/config/spec via direct HTTP, bypassing go-eth2-client.
// Returns both a string map (for simple values) and the raw JSON map (for complex values like BLOB_SCHEDULE).
func (c *Client) GetRawSpecData(ctx context.Context) (map[string]string, map[string]json.RawMessage, error) {
	url := fmt.Sprintf("%s/eth/v1/config/spec", c.baseURL)

	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := (&nethttp.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Extract only string values, skip arrays and other complex types.
	out := make(map[string]string, len(result.Data))
	for k, v := range result.Data {
		var s string
		if json.Unmarshal(v, &s) == nil {
			out[k] = s
		}
	}

	return out, result.Data, nil
}

// GetGenesis fetches genesis information from the beacon node via direct HTTP.
// This bypasses go-eth2-client's active check so it works before the node is fully ready.
func (c *Client) GetGenesis(ctx context.Context) (*Genesis, error) {
	url := fmt.Sprintf("%s/eth/v1/beacon/genesis", c.baseURL)

	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := (&nethttp.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get genesis: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get genesis: status %d: %s",
			resp.StatusCode, string(body))
	}

	var result struct {
		Data struct {
			GenesisTime           string `json:"genesis_time"`
			GenesisValidatorsRoot string `json:"genesis_validators_root"`
			GenesisForkVersion    string `json:"genesis_fork_version"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode genesis: %w", err)
	}

	genesisTimeSec, err := strconv.ParseInt(result.Data.GenesisTime, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid genesis_time: %w", err)
	}

	validatorsRoot, err := parseRoot(result.Data.GenesisValidatorsRoot)
	if err != nil {
		return nil, fmt.Errorf("invalid genesis_validators_root: %w", err)
	}

	forkVersionBytes, err := hex.DecodeString(
		strings.TrimPrefix(result.Data.GenesisForkVersion, "0x"))
	if err != nil {
		return nil, fmt.Errorf("invalid genesis_fork_version: %w", err)
	}

	var forkVersion phase0.Version
	copy(forkVersion[:], forkVersionBytes)

	return &Genesis{
		GenesisTime:           time.Unix(genesisTimeSec, 0),
		GenesisValidatorsRoot: validatorsRoot,
		GenesisForkVersion:    forkVersion,
	}, nil
}

// GetForkVersion returns the current fork version.
func (c *Client) GetForkVersion(ctx context.Context) (phase0.Version, error) {
	provider, ok := c.client.(eth2client.ForkProvider)
	if !ok {
		return phase0.Version{}, fmt.Errorf("client does not support fork provider")
	}

	resp, err := provider.Fork(ctx, &api.ForkOpts{
		State: "head",
	})
	if err != nil {
		return phase0.Version{}, fmt.Errorf("failed to get fork: %w", err)
	}

	return resp.Data.CurrentVersion, nil
}

// NodeIdentity holds the beacon node's P2P identity information.
type NodeIdentity struct {
	PeerID       string   `json:"peer_id"`
	P2PAddresses []string `json:"p2p_addresses"`
}

// GetNodeIdentity fetches the beacon node's P2P identity via /eth/v1/node/identity.
func (c *Client) GetNodeIdentity(ctx context.Context) (*NodeIdentity, error) {
	provider, ok := c.client.(eth2client.NodeIdentityProvider)
	if !ok {
		return nil, fmt.Errorf("client does not support node identity provider")
	}

	resp, err := provider.NodeIdentity(ctx, &api.NodeIdentityOpts{})
	if err != nil {
		return nil, fmt.Errorf("failed to get node identity: %w", err)
	}

	if resp.Data == nil {
		return nil, fmt.Errorf("node identity response is nil")
	}

	return &NodeIdentity{
		PeerID:       resp.Data.PeerID,
		P2PAddresses: resp.Data.P2PAddresses,
	}, nil
}

// GetRawClient returns the underlying eth2client.Service for direct API access.
func (c *Client) GetRawClient() eth2client.Service {
	return c.client
}

// SubmitProposal submits a full signed proposal (e.g. SignedBlockContents) to the beacon node.
// Used by the builder API after unblinding a blinded block.
func (c *Client) SubmitProposal(ctx context.Context, opts *api.SubmitProposalOpts) error {
	submitter, ok := c.client.(eth2client.ProposalSubmitter)
	if !ok {
		return fmt.Errorf("client does not support proposal submission")
	}
	return submitter.SubmitProposal(ctx, opts)
}

// BlockInfo contains execution-relevant information from a beacon block.
type BlockInfo struct {
	Slot               phase0.Slot
	Root               phase0.Root
	ExecutionBlockHash phase0.Hash32
	// Execution block hash safe to use as an FCU safe/finalized hash; always
	// present on the EL. See agnosticFinalitySafeExecutionBlockHash.
	FinalitySafeExecutionBlockHash phase0.Hash32
	ParentRoot                     phase0.Root
	StateRoot                      phase0.Root
	// Gas limit committed for the block's execution payload: the builder
	// bid's gas_limit from Gloas on (present even when the payload was
	// withheld), the embedded payload's gas_limit before.
	GasLimit uint64
}

// FinalityInfo contains finality checkpoint execution block hashes.
type FinalityInfo struct {
	HeadExecutionBlockHash      phase0.Hash32
	SafeExecutionBlockHash      phase0.Hash32
	FinalizedExecutionBlockHash phase0.Hash32
}

// GetBlockInfo fetches beacon block info at the given block ID.
//
// It uses the fork-agnostic block type and computes the block root via dynssz,
// which honours the network's spec (set via InitGlobalSSZSpecs). The versioned
// type's Root() routes through fastssz code generated for mainnet preset sizes,
// so it returns wrong roots on non-mainnet presets (e.g. minimal).
func (c *Client) GetBlockInfo(ctx context.Context, blockID string) (*BlockInfo, error) {
	provider, ok := c.client.(eth2client.SignedBeaconBlockProvider)
	if !ok {
		return nil, fmt.Errorf("client does not support signed beacon block provider")
	}

	resp, err := provider.AgnosticSignedBeaconBlock(ctx, &api.SignedBeaconBlockOpts{
		Block: blockID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get beacon block: %w", err)
	}

	if resp.Data == nil || resp.Data.Message == nil || resp.Data.Message.Body == nil {
		return nil, fmt.Errorf("beacon block response is nil")
	}

	msg := resp.Data.Message

	root, err := dynssz.GetGlobalDynSsz().HashTreeRoot(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to compute block root: %w", err)
	}

	execBlockHash, err := agnosticExecutionBlockHash(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to get execution block hash: %w", err)
	}

	finalitySafeHash, err := agnosticFinalitySafeExecutionBlockHash(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to get finality-safe execution block hash: %w", err)
	}

	gasLimit, err := agnosticExecutionGasLimit(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to get execution gas limit: %w", err)
	}

	return &BlockInfo{
		Slot:                           msg.Slot,
		Root:                           root,
		ExecutionBlockHash:             execBlockHash,
		FinalitySafeExecutionBlockHash: finalitySafeHash,
		ParentRoot:                     msg.ParentRoot,
		StateRoot:                      msg.StateRoot,
		GasLimit:                       gasLimit,
	}, nil
}

// agnosticExecutionBlockHash extracts the execution block hash from a
// fork-agnostic beacon block. Pre-Gloas the payload is embedded in the block;
// from Gloas on the block carries only the builder's bid, so the committed
// block hash comes from the bid instead.
func agnosticExecutionBlockHash(msg *all.BeaconBlock) (phase0.Hash32, error) {
	body := msg.Body

	if msg.Version >= version.DataVersionGloas {
		if body.SignedExecutionPayloadBid == nil || body.SignedExecutionPayloadBid.Message == nil {
			return phase0.Hash32{}, fmt.Errorf("no execution payload bid in block")
		}

		return body.SignedExecutionPayloadBid.Message.BlockHash, nil
	}

	if body.ExecutionPayload == nil {
		return phase0.Hash32{}, fmt.Errorf("no execution payload in block")
	}

	return body.ExecutionPayload.BlockHash, nil
}

// agnosticFinalitySafeExecutionBlockHash returns an execution block hash safe to
// use as a forkchoiceUpdated safe/finalized hash: one the EL is guaranteed to
// have. A Gloas block's committed bid block_hash may belong to a withheld payload
// the EL never imported, so use the bid's parent_block_hash instead — the block
// the builder built upon, an already-revealed ancestor. Pre-Gloas the payload is
// embedded, so the block hash is always present.
func agnosticFinalitySafeExecutionBlockHash(msg *all.BeaconBlock) (phase0.Hash32, error) {
	body := msg.Body

	if msg.Version >= version.DataVersionGloas {
		if body.SignedExecutionPayloadBid == nil || body.SignedExecutionPayloadBid.Message == nil {
			return phase0.Hash32{}, fmt.Errorf("no execution payload bid in block")
		}

		return body.SignedExecutionPayloadBid.Message.ParentBlockHash, nil
	}

	if body.ExecutionPayload == nil {
		return phase0.Hash32{}, fmt.Errorf("no execution payload in block")
	}

	return body.ExecutionPayload.BlockHash, nil
}

// agnosticExecutionGasLimit extracts the committed execution gas limit from a
// fork-agnostic beacon block. From Gloas on it is the builder bid's gas_limit
// (the value consensus gas-limit checks compare a child bid against, even when
// the payload was withheld); before Gloas it is the embedded payload's.
func agnosticExecutionGasLimit(msg *all.BeaconBlock) (uint64, error) {
	body := msg.Body

	if msg.Version >= version.DataVersionGloas {
		if body.SignedExecutionPayloadBid == nil || body.SignedExecutionPayloadBid.Message == nil {
			return 0, fmt.Errorf("no execution payload bid in block")
		}

		return body.SignedExecutionPayloadBid.Message.GasLimit, nil
	}

	if body.ExecutionPayload == nil {
		return 0, fmt.Errorf("no execution payload in block")
	}

	return body.ExecutionPayload.GasLimit, nil
}

// GetFinalityInfo fetches finality checkpoints and returns execution block hashes.
func (c *Client) GetFinalityInfo(ctx context.Context) (*FinalityInfo, error) {
	// Get finality checkpoints
	finalityProvider, ok := c.client.(eth2client.FinalityProvider)
	if !ok {
		return nil, fmt.Errorf("client does not support finality provider")
	}

	finalityResp, err := finalityProvider.Finality(ctx, &api.FinalityOpts{
		State: "head",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get finality: %w", err)
	}

	if finalityResp.Data == nil {
		return nil, fmt.Errorf("finality response is nil")
	}

	// Get head execution block hash
	headInfo, err := c.GetBlockInfo(ctx, "head")
	if err != nil {
		return nil, fmt.Errorf("failed to get head block info: %w", err)
	}

	// Get safe (justified) execution block hash
	var safeBlockHash phase0.Hash32

	if finalityResp.Data.Justified != nil && finalityResp.Data.Justified.Root != (phase0.Root{}) {
		safeInfo, err := c.GetBlockInfo(ctx, fmt.Sprintf("0x%x", finalityResp.Data.Justified.Root[:]))
		if err != nil {
			c.log.WithError(err).Warn("Failed to get safe block info, using head")
			safeBlockHash = headInfo.ExecutionBlockHash
		} else {
			safeBlockHash = safeInfo.FinalitySafeExecutionBlockHash
		}
	} else {
		safeBlockHash = headInfo.ExecutionBlockHash
	}

	// Get finalized execution block hash
	var finalizedBlockHash phase0.Hash32

	if finalityResp.Data.Finalized != nil && finalityResp.Data.Finalized.Root != (phase0.Root{}) {
		finalizedInfo, err := c.GetBlockInfo(ctx, fmt.Sprintf("0x%x", finalityResp.Data.Finalized.Root[:]))
		if err != nil {
			c.log.WithError(err).Warn("Failed to get finalized block info, using safe")
			finalizedBlockHash = safeBlockHash
		} else {
			finalizedBlockHash = finalizedInfo.FinalitySafeExecutionBlockHash
		}
	} else {
		finalizedBlockHash = safeBlockHash
	}

	return &FinalityInfo{
		HeadExecutionBlockHash:      headInfo.ExecutionBlockHash,
		SafeExecutionBlockHash:      safeBlockHash,
		FinalizedExecutionBlockHash: finalizedBlockHash,
	}, nil
}

// GetValidatorIndexToPubkeyMap fetches the beacon state once and returns a map of validator index to pubkey.
// Used to refresh an index→pubkey cache once per epoch instead of querying per payload build.
func (c *Client) GetValidatorIndexToPubkeyMap(ctx context.Context, stateID string) (map[phase0.ValidatorIndex]phase0.BLSPubKey, error) {
	provider, ok := c.client.(eth2client.BeaconStateProvider)
	if !ok {
		return nil, fmt.Errorf("client does not support beacon state provider")
	}

	resp, err := provider.BeaconState(ctx, &api.BeaconStateOpts{
		State: stateID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get beacon state: %w", err)
	}

	if resp.Data == nil {
		return nil, fmt.Errorf("beacon state response is nil")
	}

	validators := getValidatorsFromState(resp.Data)
	if validators == nil {
		return nil, fmt.Errorf("beacon state has no validators")
	}

	out := make(map[phase0.ValidatorIndex]phase0.BLSPubKey, len(validators))
	for i, v := range validators {
		if v != nil {
			out[phase0.ValidatorIndex(i)] = v.PublicKey
		}
	}
	return out, nil
}

// getValidatorsFromState extracts the validator list from a versioned beacon state.
func getValidatorsFromState(state *spec.VersionedBeaconState) []*phase0.Validator {
	if state == nil {
		return nil
	}
	switch state.Version {
	case spec.DataVersionPhase0:
		if state.Phase0 != nil {
			return state.Phase0.Validators
		}
	case spec.DataVersionAltair:
		if state.Altair != nil {
			return state.Altair.Validators
		}
	case spec.DataVersionBellatrix:
		if state.Bellatrix != nil {
			return state.Bellatrix.Validators
		}
	case spec.DataVersionCapella:
		if state.Capella != nil {
			return state.Capella.Validators
		}
	case spec.DataVersionDeneb:
		if state.Deneb != nil {
			return state.Deneb.Validators
		}
	case spec.DataVersionElectra:
		if state.Electra != nil {
			return state.Electra.Validators
		}
	case spec.DataVersionFulu:
		if state.Fulu != nil {
			return state.Fulu.Validators
		}
	case spec.DataVersionGloas:
		if state.Gloas != nil {
			return state.Gloas.Validators
		}
	}
	return nil
}
