// Package beacon provides a client for interacting with Ethereum consensus layer nodes.
package beacon

import (
	"context"
	"fmt"
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

	cs := &ChainSpec{
		SecondsPerSlot: secondsPerSlot,
		SlotsPerEpoch:  slotsPerEpoch,
	}

	// Parse duty calculation parameters (use defaults if not present)
	if v, ok := spec["SHUFFLE_ROUND_COUNT"].(uint64); ok {
		cs.ShuffleRoundCount = v
	}

	if v, ok := spec["TARGET_COMMITTEE_SIZE"].(uint64); ok {
		cs.TargetCommitteeSize = v
	}

	if v, ok := spec["MAX_COMMITTEES_PER_SLOT"].(uint64); ok {
		cs.MaxCommitteesPerSlot = v
	}

	if v, ok := spec["MAX_EFFECTIVE_BALANCE"].(uint64); ok {
		cs.MaxEffectiveBalance = v
	}

	if v, ok := spec["MAX_EFFECTIVE_BALANCE_ELECTRA"].(uint64); ok {
		cs.MaxEffectiveBalanceElectra = v
	}

	if v, ok := spec["EPOCHS_PER_HISTORICAL_VECTOR"].(uint64); ok {
		cs.EpochsPerHistoricalVector = v
	}

	if v, ok := spec["MIN_SEED_LOOKAHEAD"].(uint64); ok {
		cs.MinSeedLookahead = v
	}

	// Parse domain types
	if v, ok := spec["DOMAIN_BEACON_PROPOSER"].(phase0.DomainType); ok {
		cs.DomainBeaconProposer = v
	}

	if v, ok := spec["DOMAIN_BEACON_ATTESTER"].(phase0.DomainType); ok {
		cs.DomainBeaconAttester = v
	}

	if v, ok := spec["DOMAIN_PTC_ATTESTER"].(phase0.DomainType); ok {
		cs.DomainPtcAttester = v
	}

	// Parse fork epochs
	if v, ok := spec["ELECTRA_FORK_EPOCH"].(uint64); ok {
		cs.ElectraForkEpoch = &v
	}

	// Parse ePBS parameters
	if v, ok := spec["PTC_SIZE"].(uint64); ok {
		cs.PtcSize = v
	}

	return cs, nil
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
