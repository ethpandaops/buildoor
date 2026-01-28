// Package beacon provides a client for interacting with Ethereum consensus layer nodes.
package beacon

import (
	"context"
	"fmt"
	"time"

	eth2client "github.com/attestantio/go-eth2-client"
	"github.com/attestantio/go-eth2-client/api"
	"github.com/attestantio/go-eth2-client/http"
	"github.com/attestantio/go-eth2-client/spec"
	"github.com/attestantio/go-eth2-client/spec/capella"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/rs/zerolog"
	"github.com/sirupsen/logrus"
)

// ChainSpec holds chain specification parameters.
type ChainSpec struct {
	SecondsPerSlot               time.Duration
	SlotsPerEpoch                uint64
	EpochsPerSyncCommitteePeriod uint64
	GenesisTime                  time.Time
	GenesisForkVersion           phase0.Version
	DepositContractAddress       string
	DepositChainID               uint64
}

// Genesis holds genesis information.
type Genesis struct {
	GenesisTime           time.Time
	GenesisValidatorsRoot phase0.Root
	GenesisForkVersion    phase0.Version
}

// BuilderInfo represents information about a builder from the beacon state.
type BuilderInfo struct {
	Index             uint64
	Pubkey            phase0.BLSPubKey
	Balance           uint64
	Active            bool
	DepositEpoch      uint64
	WithdrawableEpoch uint64
}

// Client wraps the consensus layer client for beacon node interactions.
type Client struct {
	client         eth2client.Service
	baseURL        string
	eventStream    *EventStream
	log            logrus.FieldLogger
	buildersCache  []*BuilderInfo // Cached builders from beacon state
	buildersLoaded bool           // Whether builders have been loaded
}

// NewClient creates a new CL client connected to the specified beacon node.
func NewClient(ctx context.Context, baseURL string, log logrus.FieldLogger) (*Client, error) {
	clientLog := log.WithField("component", "cl-client")

	httpClient, err := http.New(ctx,
		http.WithAddress(baseURL),
		http.WithLogLevel(zerolog.WarnLevel),
		http.WithTimeout(30*time.Second),
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

// GetChainSpec fetches the chain specification from the beacon node.
func (c *Client) GetChainSpec(ctx context.Context) (*ChainSpec, error) {
	provider, ok := c.client.(eth2client.SpecProvider)
	if !ok {
		return nil, fmt.Errorf("client does not support spec provider")
	}

	resp, err := provider.Spec(ctx, &api.SpecOpts{})
	if err != nil {
		return nil, fmt.Errorf("failed to get spec: %w", err)
	}

	spec := resp.Data

	secondsPerSlot, ok := spec["SECONDS_PER_SLOT"].(time.Duration)
	if !ok {
		return nil, fmt.Errorf("SECONDS_PER_SLOT not found or invalid type")
	}

	slotsPerEpoch, ok := spec["SLOTS_PER_EPOCH"].(uint64)
	if !ok {
		return nil, fmt.Errorf("SLOTS_PER_EPOCH not found or invalid type")
	}

	return &ChainSpec{
		SecondsPerSlot: secondsPerSlot,
		SlotsPerEpoch:  slotsPerEpoch,
	}, nil
}

// GetGenesis fetches genesis information from the beacon node.
func (c *Client) GetGenesis(ctx context.Context) (*Genesis, error) {
	provider, ok := c.client.(eth2client.GenesisProvider)
	if !ok {
		return nil, fmt.Errorf("client does not support genesis provider")
	}

	resp, err := provider.Genesis(ctx, &api.GenesisOpts{})
	if err != nil {
		return nil, fmt.Errorf("failed to get genesis: %w", err)
	}

	genesis := resp.Data

	return &Genesis{
		GenesisTime:           genesis.GenesisTime,
		GenesisValidatorsRoot: genesis.GenesisValidatorsRoot,
		GenesisForkVersion:    genesis.GenesisForkVersion,
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

// GetCurrentEpoch calculates the current epoch based on slot.
func (c *Client) GetCurrentEpoch(ctx context.Context) (phase0.Epoch, error) {
	slot, err := c.GetHeadSlot(ctx)
	if err != nil {
		return 0, err
	}

	spec, err := c.GetChainSpec(ctx)
	if err != nil {
		return 0, err
	}

	return phase0.Epoch(uint64(slot) / spec.SlotsPerEpoch), nil
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

// SlotToTime converts a slot number to a timestamp.
func (c *Client) SlotToTime(genesis *Genesis, spec *ChainSpec, slot phase0.Slot) time.Time {
	slotDuration := time.Duration(uint64(slot)) * spec.SecondsPerSlot
	return genesis.GenesisTime.Add(slotDuration)
}

// TimeToSlot converts a timestamp to a slot number.
func (c *Client) TimeToSlot(genesis *Genesis, spec *ChainSpec, t time.Time) phase0.Slot {
	if t.Before(genesis.GenesisTime) {
		return 0
	}

	elapsed := t.Sub(genesis.GenesisTime)

	return phase0.Slot(elapsed / spec.SecondsPerSlot)
}

// GetRawClient returns the underlying eth2client.Service for direct API access.
func (c *Client) GetRawClient() eth2client.Service {
	return c.client
}

// IsGloas checks if the beacon node is running the Gloas fork.
func (c *Client) IsGloas(ctx context.Context) (bool, error) {
	state, err := c.fetchBeaconState(ctx, "head")
	if err != nil {
		return false, fmt.Errorf("failed to fetch beacon state: %w", err)
	}

	return state.Version == spec.DataVersionGloas && state.Gloas != nil, nil
}

// LoadBuilders fetches the beacon state once and caches the builders list.
// This should be called once at startup. Returns true if builders were loaded.
func (c *Client) LoadBuilders(ctx context.Context) (bool, error) {
	if c.buildersLoaded {
		return len(c.buildersCache) > 0, nil
	}

	c.log.Info("Loading builders from beacon state...")

	state, err := c.fetchBeaconState(ctx, "head")
	if err != nil {
		return false, fmt.Errorf("failed to fetch beacon state: %w", err)
	}

	c.buildersLoaded = true

	// Only Gloas state has builders
	if state.Version != spec.DataVersionGloas || state.Gloas == nil {
		c.log.WithField("version", state.Version).Info("Beacon state is not Gloas version, no builders available")
		c.buildersCache = nil

		return false, nil
	}

	// Convert and cache builders
	builders := state.Gloas.Builders
	c.buildersCache = make([]*BuilderInfo, len(builders))

	for i, builder := range builders {
		c.buildersCache[i] = builderToInfo(uint64(i), builder)
	}

	c.log.WithField("count", len(c.buildersCache)).Info("Builders loaded from beacon state")

	return len(c.buildersCache) > 0, nil
}

// GetCachedBuilders returns the cached builders list.
func (c *Client) GetCachedBuilders() []*BuilderInfo {
	return c.buildersCache
}

// HasBuildersLoaded returns whether builders have been loaded.
func (c *Client) HasBuildersLoaded() bool {
	return c.buildersLoaded
}

// RefreshBuilders forces a re-fetch of the builders from the beacon state.
// Use this when polling for changes (e.g., waiting for registration).
func (c *Client) RefreshBuilders(ctx context.Context) (bool, error) {
	c.buildersLoaded = false // Reset to force re-fetch

	return c.LoadBuilders(ctx)
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

// GetRandao fetches the RANDAO value for the given state.
func (c *Client) GetRandao(ctx context.Context, stateID string) (phase0.Root, error) {
	provider, ok := c.client.(eth2client.BeaconStateRandaoProvider)
	if !ok {
		return phase0.Root{}, fmt.Errorf("client does not support beacon state randao provider")
	}

	resp, err := provider.BeaconStateRandao(ctx, &api.BeaconStateRandaoOpts{
		State: stateID,
	})
	if err != nil {
		return phase0.Root{}, fmt.Errorf("failed to get randao: %w", err)
	}

	if resp.Data == nil {
		return phase0.Root{}, fmt.Errorf("randao response is nil")
	}

	return *resp.Data, nil
}

// GetBlockWithdrawals fetches the withdrawals from a beacon block's execution payload.
// This is used for pre-Gloas forks where the execution payload is embedded in the block.
func (c *Client) GetBlockWithdrawals(ctx context.Context, blockRoot phase0.Root) ([]*capella.Withdrawal, error) {
	provider, ok := c.client.(eth2client.SignedBeaconBlockProvider)
	if !ok {
		return nil, fmt.Errorf("client does not support signed beacon block provider")
	}

	blockID := fmt.Sprintf("0x%x", blockRoot[:])

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

	// Extract withdrawals based on block version
	switch block.Version {
	case spec.DataVersionCapella:
		if block.Capella != nil && block.Capella.Message != nil &&
			block.Capella.Message.Body != nil && block.Capella.Message.Body.ExecutionPayload != nil {
			return block.Capella.Message.Body.ExecutionPayload.Withdrawals, nil
		}
	case spec.DataVersionDeneb:
		if block.Deneb != nil && block.Deneb.Message != nil &&
			block.Deneb.Message.Body != nil && block.Deneb.Message.Body.ExecutionPayload != nil {
			return block.Deneb.Message.Body.ExecutionPayload.Withdrawals, nil
		}
	case spec.DataVersionElectra:
		if block.Electra != nil && block.Electra.Message != nil &&
			block.Electra.Message.Body != nil && block.Electra.Message.Body.ExecutionPayload != nil {
			return block.Electra.Message.Body.ExecutionPayload.Withdrawals, nil
		}
	case spec.DataVersionFulu:
		if block.Fulu != nil && block.Fulu.Message != nil &&
			block.Fulu.Message.Body != nil && block.Fulu.Message.Body.ExecutionPayload != nil {
			return block.Fulu.Message.Body.ExecutionPayload.Withdrawals, nil
		}
	case spec.DataVersionGloas:
		// Gloas blocks don't have execution payload embedded - use GetPayloadEnvelopeWithdrawals instead
		return nil, fmt.Errorf("gloas blocks do not contain execution payload, use GetPayloadEnvelopeWithdrawals")
	}

	return nil, fmt.Errorf("unsupported block version or missing execution payload: %s", block.Version)
}

// GetPayloadEnvelopeWithdrawals fetches the withdrawals from a payload envelope (Gloas only).
// This fetches the signed execution payload envelope for a block and extracts the withdrawals.
func (c *Client) GetPayloadEnvelopeWithdrawals(
	ctx context.Context,
	blockRoot phase0.Root,
) ([]*capella.Withdrawal, error) {
	provider, ok := c.client.(eth2client.ExecutionPayloadProvider)
	if !ok {
		return nil, fmt.Errorf("client does not support execution payload provider")
	}

	blockID := fmt.Sprintf("0x%x", blockRoot[:])

	resp, err := provider.SignedExecutionPayloadEnvelope(ctx, &api.SignedExecutionPayloadEnvelopeOpts{
		Block: blockID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get payload envelope: %w", err)
	}

	if resp.Data == nil {
		return nil, fmt.Errorf("payload envelope response is nil")
	}

	envelope := resp.Data
	if envelope.Message == nil || envelope.Message.Payload == nil {
		return nil, fmt.Errorf("payload envelope message or payload is nil")
	}

	return envelope.Message.Payload.Withdrawals, nil
}
