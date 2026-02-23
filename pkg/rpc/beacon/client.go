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

	eth2client "github.com/attestantio/go-eth2-client"
	"github.com/attestantio/go-eth2-client/api"
	apiv1fulu "github.com/attestantio/go-eth2-client/api/v1/fulu"
	"github.com/attestantio/go-eth2-client/http"
	"github.com/attestantio/go-eth2-client/spec"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/rs/zerolog"
	"github.com/sirupsen/logrus"
)

// ChainSpec holds chain specification parameters.
type ChainSpec struct {
	SecondsPerSlot time.Duration
	SlotsPerEpoch  uint64

	// Duty calculation parameters
	ShuffleRoundCount          uint64
	TargetCommitteeSize        uint64
	MaxCommitteesPerSlot       uint64
	MaxEffectiveBalance        uint64
	MaxEffectiveBalanceElectra uint64
	EpochsPerHistoricalVector  uint64
	MinSeedLookahead           uint64

	// Domain types
	DomainBeaconProposer phase0.DomainType
	DomainBeaconAttester phase0.DomainType
	DomainPtcAttester    phase0.DomainType

	// Fork epochs (nil if not configured)
	ElectraForkEpoch *uint64
	GloasForkEpoch   *uint64

	// ePBS parameters
	PtcSize uint64
}

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

// GetChainSpec fetches the chain specification from the beacon node via direct HTTP.
// This bypasses go-eth2-client's active check so it works before the node is fully ready.
func (c *Client) GetChainSpec(ctx context.Context) (*ChainSpec, error) {
	specData, err := c.fetchSpecDirect(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get spec: %w", err)
	}

	secondsPerSlotStr, ok := specData["SECONDS_PER_SLOT"]
	if !ok {
		return nil, fmt.Errorf("SECONDS_PER_SLOT not found")
	}

	secondsPerSlot, err := strconv.ParseUint(secondsPerSlotStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid SECONDS_PER_SLOT: %w", err)
	}

	slotsPerEpochStr, ok := specData["SLOTS_PER_EPOCH"]
	if !ok {
		return nil, fmt.Errorf("SLOTS_PER_EPOCH not found")
	}

	slotsPerEpoch, err := strconv.ParseUint(slotsPerEpochStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid SLOTS_PER_EPOCH: %w", err)
	}

	cs := &ChainSpec{
		SecondsPerSlot: time.Duration(secondsPerSlot) * time.Second,
		SlotsPerEpoch:  slotsPerEpoch,
	}

	// Parse duty calculation parameters (use defaults if not present)
	if v, err := parseSpecUint64(specData, "SHUFFLE_ROUND_COUNT"); err == nil {
		cs.ShuffleRoundCount = v
	}

	if v, err := parseSpecUint64(specData, "TARGET_COMMITTEE_SIZE"); err == nil {
		cs.TargetCommitteeSize = v
	}

	if v, err := parseSpecUint64(specData, "MAX_COMMITTEES_PER_SLOT"); err == nil {
		cs.MaxCommitteesPerSlot = v
	}

	if v, err := parseSpecUint64(specData, "MAX_EFFECTIVE_BALANCE"); err == nil {
		cs.MaxEffectiveBalance = v
	}

	if v, err := parseSpecUint64(specData, "MAX_EFFECTIVE_BALANCE_ELECTRA"); err == nil {
		cs.MaxEffectiveBalanceElectra = v
	}

	if v, err := parseSpecUint64(specData, "EPOCHS_PER_HISTORICAL_VECTOR"); err == nil {
		cs.EpochsPerHistoricalVector = v
	}

	if v, err := parseSpecUint64(specData, "MIN_SEED_LOOKAHEAD"); err == nil {
		cs.MinSeedLookahead = v
	}

	// Parse domain types
	if v, err := parseSpecDomainType(specData, "DOMAIN_BEACON_PROPOSER"); err == nil {
		cs.DomainBeaconProposer = v
	}

	if v, err := parseSpecDomainType(specData, "DOMAIN_BEACON_ATTESTER"); err == nil {
		cs.DomainBeaconAttester = v
	}

	if v, err := parseSpecDomainType(specData, "DOMAIN_PTC_ATTESTER"); err == nil {
		cs.DomainPtcAttester = v
	}

	// Parse fork epochs
	if v, err := parseSpecUint64(specData, "ELECTRA_FORK_EPOCH"); err == nil {
		cs.ElectraForkEpoch = &v
	}

	if v, err := parseSpecUint64(specData, "GLOAS_FORK_EPOCH"); err == nil {
		cs.GloasForkEpoch = &v
	}

	// Parse ePBS parameters
	if v, err := parseSpecUint64(specData, "PTC_SIZE"); err == nil {
		cs.PtcSize = v
	}

	return cs, nil
}

// fetchSpecDirect fetches /eth/v1/config/spec via direct HTTP, bypassing go-eth2-client.
func (c *Client) fetchSpecDirect(ctx context.Context) (map[string]string, error) {
	url := fmt.Sprintf("%s/eth/v1/config/spec", c.baseURL)

	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := (&nethttp.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return result.Data, nil
}

// parseSpecUint64 parses a uint64 value from the spec data map.
func parseSpecUint64(data map[string]string, key string) (uint64, error) {
	s, ok := data[key]
	if !ok {
		return 0, fmt.Errorf("%s not found", key)
	}

	return strconv.ParseUint(s, 10, 64)
}

// parseSpecDomainType parses a 4-byte domain type from the spec data map (hex with 0x prefix).
func parseSpecDomainType(data map[string]string, key string) (phase0.DomainType, error) {
	s, ok := data[key]
	if !ok {
		return phase0.DomainType{}, fmt.Errorf("%s not found", key)
	}

	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		return phase0.DomainType{}, err
	}

	if len(b) != 4 {
		return phase0.DomainType{}, fmt.Errorf("invalid domain type length: %d", len(b))
	}

	var dt phase0.DomainType
	copy(dt[:], b)

	return dt, nil
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

// GetHeadSlot fetches the current head slot.
func (c *Client) GetHeadSlot(ctx context.Context) (phase0.Slot, error) {
	provider, ok := c.client.(eth2client.BeaconBlockHeadersProvider)
	if !ok {
		return 0, fmt.Errorf("client does not support block headers provider")
	}

	resp, err := provider.BeaconBlockHeader(ctx, &api.BeaconBlockHeaderOpts{
		Block: "head",
	})
	if err != nil {
		return 0, fmt.Errorf("failed to get head block header: %w", err)
	}

	return resp.Data.Header.Message.Slot, nil
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

// GetRawClient returns the underlying eth2client.Service for direct API access.
func (c *Client) GetRawClient() eth2client.Service {
	return c.client
}

// SubmitProposal submits a full signed proposal (e.g. Fulu SignedBlockContents) to the beacon node.
// Used by the builder API after unblinding a blinded block.
func (c *Client) SubmitProposal(ctx context.Context, opts *api.SubmitProposalOpts) error {
	submitter, ok := c.client.(eth2client.ProposalSubmitter)
	if !ok {
		return fmt.Errorf("client does not support proposal submission")
	}
	return submitter.SubmitProposal(ctx, opts)
}

// SubmitFuluBlock submits a Fulu SignedBlockContents (full unblinded block + blobs) to the beacon node.
func (c *Client) SubmitFuluBlock(ctx context.Context, contents *apiv1fulu.SignedBlockContents) error {
	if contents == nil {
		return fmt.Errorf("fulu block contents is nil")
	}
	return c.SubmitProposal(ctx, &api.SubmitProposalOpts{
		Proposal: &api.VersionedSignedProposal{
			Version: spec.DataVersionFulu,
			Blinded: false,
			Fulu:    contents,
		},
	})
}

// BlockInfo contains execution-relevant information from a beacon block.
type BlockInfo struct {
	Slot               phase0.Slot
	ExecutionBlockHash phase0.Hash32
	ParentRoot         phase0.Root
	StateRoot          phase0.Root
}

// FinalityInfo contains finality checkpoint execution block hashes.
type FinalityInfo struct {
	HeadExecutionBlockHash      phase0.Hash32
	SafeExecutionBlockHash      phase0.Hash32
	FinalizedExecutionBlockHash phase0.Hash32
}

// GetBlockInfo fetches beacon block info at the given block ID.
func (c *Client) GetBlockInfo(ctx context.Context, blockID string) (*BlockInfo, error) {
	provider, ok := c.client.(eth2client.SignedBeaconBlockProvider)
	if !ok {
		return nil, fmt.Errorf("client does not support signed beacon block provider")
	}

	resp, err := provider.SignedBeaconBlock(ctx, &api.SignedBeaconBlockOpts{
		Block: blockID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get beacon block: %w", err)
	}

	if resp.Data == nil {
		return nil, fmt.Errorf("beacon block response is nil")
	}

	block := resp.Data

	slot, err := block.Slot()
	if err != nil {
		return nil, fmt.Errorf("failed to get slot: %w", err)
	}

	execBlockHash, err := block.ExecutionBlockHash()
	if err != nil {
		return nil, fmt.Errorf("failed to get execution block hash: %w", err)
	}

	parentRoot, err := block.ParentRoot()
	if err != nil {
		return nil, fmt.Errorf("failed to get parent root: %w", err)
	}

	stateRoot, err := block.StateRoot()
	if err != nil {
		return nil, fmt.Errorf("failed to get state root: %w", err)
	}

	return &BlockInfo{
		Slot:               slot,
		ExecutionBlockHash: execBlockHash,
		ParentRoot:         parentRoot,
		StateRoot:          stateRoot,
	}, nil
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
			safeBlockHash = safeInfo.ExecutionBlockHash
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
			finalizedBlockHash = finalizedInfo.ExecutionBlockHash
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

// GetValidatorPubkeyByIndex fetches the validator pubkey at the given index from the beacon state.
// stateID is typically "head" or a slot/block root. Used to resolve proposer index to pubkey for fee recipient lookup.
func (c *Client) GetValidatorPubkeyByIndex(ctx context.Context, stateID string, index phase0.ValidatorIndex) (phase0.BLSPubKey, error) {
	provider, ok := c.client.(eth2client.BeaconStateProvider)
	if !ok {
		return phase0.BLSPubKey{}, fmt.Errorf("client does not support beacon state provider")
	}

	resp, err := provider.BeaconState(ctx, &api.BeaconStateOpts{
		State: stateID,
	})
	if err != nil {
		return phase0.BLSPubKey{}, fmt.Errorf("failed to get beacon state: %w", err)
	}

	if resp.Data == nil {
		return phase0.BLSPubKey{}, fmt.Errorf("beacon state response is nil")
	}

	validators := getValidatorsFromState(resp.Data)
	if validators == nil {
		return phase0.BLSPubKey{}, fmt.Errorf("beacon state has no validators")
	}

	if uint64(index) >= uint64(len(validators)) {
		return phase0.BLSPubKey{}, fmt.Errorf("validator index %d out of range (len=%d)", index, len(validators))
	}

	return validators[index].PublicKey, nil
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
