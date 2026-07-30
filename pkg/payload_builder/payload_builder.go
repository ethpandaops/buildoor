package payload_builder

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth-engine-client/spec/paris"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// PayloadBuilder handles execution payload building via the Engine API.
type PayloadBuilder struct {
	clClient     *beacon.Client
	engineClient EngineClient
	chainSvc     chain.Service
	feeRecipient common.Address

	settingsResolvers []ProposerSettingsResolver // asked in order for proposer settings; first match wins
	cfg               *config.Config             // shared config; mutable settings are read live, never cached
	log               logrus.FieldLogger

	// Active build tracking: multiple candidate builds may run for the same
	// slot (one per parent tuple); builds for older slots are cancelled when
	// a newer slot starts.
	activeBuilds map[activeBuildKey]*activeBuild
	mu           sync.Mutex
}

// activeBuildKey identifies one in-progress build: a slot may build several
// candidate payloads on different execution parents concurrently.
type activeBuildKey struct {
	slot       phase0.Slot
	parentHash phase0.Hash32
}

// activeBuild tracks an in-progress payload build.
type activeBuild struct {
	payloadID paris.PayloadID
	cancelFn  context.CancelFunc
}

// NewPayloadBuilder creates a new payload builder.
// cfg is the shared config pointer; mutable settings (e.g. PayloadBuildTime) are read live from it.
// settingsResolvers are asked in order for the proposer's announced fee recipient and gas
// limit; the first match wins.
func NewPayloadBuilder(
	clClient *beacon.Client,
	engineClient EngineClient,
	chainSvc chain.Service,
	feeRecipient common.Address,
	cfg *config.Config,
	log logrus.FieldLogger,
	settingsResolvers []ProposerSettingsResolver,
) *PayloadBuilder {
	return &PayloadBuilder{
		clClient:          clClient,
		chainSvc:          chainSvc,
		engineClient:      engineClient,
		feeRecipient:      feeRecipient,
		settingsResolvers: settingsResolvers,
		cfg:               cfg,
		activeBuilds:      make(map[activeBuildKey]*activeBuild, 4),
		log:               log.WithField("component", "payload-builder"),
	}
}

// BuildPayloadFromAttributes builds a payload using data from a payload_attributes event.
// This is the primary build path, triggered when the beacon node emits payload_attributes.
// The event contains all necessary information: timestamp, randao, withdrawals, etc.
//
// The attributes may be an effective copy with the parent fields redirected to
// another candidate parent (reorg / payload-miss handling); this method treats
// whatever parent it is given as authoritative and stores it on the returned
// Payload, so the bid built from that payload advertises the same parent it
// built on.
//
// buildTimeMs is the EL build wait; 0 uses the live-configured
// PayloadBuildTime.
func (b *PayloadBuilder) BuildPayloadFromAttributes(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
	buildTimeMs uint64,
) (*Payload, error) {
	buildKey := activeBuildKey{slot: attrs.ProposalSlot, parentHash: attrs.ParentBlockHash}

	b.mu.Lock()

	// Cancel builds for older slots and any earlier build of this exact
	// parent tuple; concurrent candidate builds of the same slot on other
	// parents keep running.
	for key, build := range b.activeBuilds {
		if key.slot != attrs.ProposalSlot || key == buildKey {
			build.cancelFn()
			delete(b.activeBuilds, key)
		}
	}

	buildCtx, cancel := context.WithCancel(ctx)

	b.activeBuilds[buildKey] = &activeBuild{cancelFn: cancel}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		if build, ok := b.activeBuilds[buildKey]; ok {
			build.cancelFn()
			delete(b.activeBuilds, buildKey)
		}
		b.mu.Unlock()
	}()

	// Resolve the fork active at the build epoch and the engine method version it implies.
	buildEpoch := b.chainSvc.GetEpochOfSlot(attrs.ProposalSlot)
	beaconFork := b.chainSvc.ActiveForkAtEpoch(buildEpoch)

	engineVersion, err := chain.EngineVersion(beaconFork)
	if err != nil {
		return nil, fmt.Errorf("cannot build payload for fork %s: %w", beaconFork, err)
	}

	// Get finality info (still need safe/finalized block hashes).
	finalityInfo, err := b.clClient.GetFinalityInfo(buildCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to get finality info: %w", err)
	}

	// Resolve the fee recipient (and target gas limit) for the build. The
	// registered resolvers are asked in order (each self-scoped to its fork /
	// data source: gossip preferences post-Gloas, validator registrations
	// pre-Gloas); the first match wins.
	// Post-Gloas fallbacks: TargetGasLimit / SuggestedFeeRecipient from the
	//                       payload_attributes event.
	// Final fallback:       the builder's configured fee recipient.
	proposerFeeRecipient := b.feeRecipient
	var targetGasLimit uint64

	for _, resolver := range b.settingsResolvers {
		settings, ok := resolver.ResolveProposerSettings(attrs.ProposalSlot, attrs.ProposerIndex)
		if !ok {
			continue
		}

		proposerFeeRecipient = settings.FeeRecipient
		targetGasLimit = settings.TargetGasLimit

		b.log.WithFields(logrus.Fields{
			"proposer_index":   attrs.ProposerIndex,
			"fee_recipient":    proposerFeeRecipient.Hex(),
			"target_gas_limit": targetGasLimit,
		}).Debug("Using proposer settings from resolver")

		break
	}

	if beaconFork >= version.DataVersionGloas {
		if targetGasLimit == 0 {
			targetGasLimit = attrs.TargetGasLimit
		}

		// If we still have the default fee recipient, use SuggestedFeeRecipient from
		// payload_attributes. This ensures bids match the proposer's expected fee
		// recipient even when preferences aren't received via SSE (e.g. same-node
		// P2P broadcast doesn't loop back).
		if proposerFeeRecipient == b.feeRecipient && attrs.SuggestedFeeRecipient != (common.Address{}) {
			proposerFeeRecipient = attrs.SuggestedFeeRecipient
			b.log.WithFields(logrus.Fields{
				"slot":           attrs.ProposalSlot,
				"proposer_index": attrs.ProposerIndex,
				"fee_recipient":  proposerFeeRecipient.Hex(),
			}).Debug("Using suggested fee recipient from payload_attributes")
		}
	}

	// Build the fork-agnostic payload attributes and forkchoice request. The
	// engine client dispatches to the correct engine_forkchoiceUpdated version.
	payloadAttrs := &engineall.PayloadAttributes{
		Version:               engineVersion,
		Timestamp:             attrs.Timestamp,
		PrevRandao:            paris.Hash32(attrs.PrevRandao),
		SuggestedFeeRecipient: paris.Address(proposerFeeRecipient),
		Withdrawals:           convertWithdrawalsToEngineFormat(attrs.Withdrawals),
		ParentBeaconBlockRoot: paris.Hash32(attrs.ParentBeaconBlockRoot),
		SlotNumber:            uint64(attrs.ProposalSlot),
		TargetGasLimit:        targetGasLimit,
	}

	if len(attrs.InclusionListTransactions) > 0 {
		payloadAttrs.InclusionListTransactions = make([]paris.Transaction, len(attrs.InclusionListTransactions))
		for i, tx := range attrs.InclusionListTransactions {
			payloadAttrs.InclusionListTransactions[i] = paris.Transaction(tx)
		}
	}

	fcuReq := &engineall.ForkchoiceUpdatedRequest{
		Version: engineVersion,
		ForkchoiceState: &paris.ForkchoiceState{
			HeadBlockHash:      paris.Hash32(attrs.ParentBlockHash),
			SafeBlockHash:      paris.Hash32(finalityInfo.SafeExecutionBlockHash),
			FinalizedBlockHash: paris.Hash32(finalityInfo.FinalizedExecutionBlockHash),
		},
		PayloadAttributes: payloadAttrs,
	}

	b.log.WithFields(logrus.Fields{
		"slot":             attrs.ProposalSlot,
		"timestamp":        attrs.Timestamp,
		"withdrawal_count": len(payloadAttrs.Withdrawals),
		"parent_hash":      fmt.Sprintf("%x", attrs.ParentBlockHash[:8]),
		"engine_version":   engineVersion,
		"target_gas_limit": targetGasLimit,
	}).Debug("Building payload from attributes")

	fcuResp, err := b.engineClient.ForkchoiceUpdatedAgnostic(buildCtx, fcuReq)
	if err != nil {
		return nil, fmt.Errorf("forkchoiceUpdated failed: %w", err)
	}

	status := fcuResp.PayloadStatus.Status
	if status != paris.PayloadValidationStatusValid && status != paris.PayloadValidationStatusSyncing {
		return nil, fmt.Errorf("forkchoice status: %s", status)
	}

	if fcuResp.PayloadID == nil {
		return nil, fmt.Errorf("no payload ID returned")
	}

	payloadID := *fcuResp.PayloadID

	b.mu.Lock()
	if build, ok := b.activeBuilds[buildKey]; ok {
		build.payloadID = payloadID
	}
	b.mu.Unlock()

	b.log.WithFields(logrus.Fields{
		"slot":       attrs.ProposalSlot,
		"payload_id": fmt.Sprintf("%x", payloadID[:]),
	}).Debug("Payload build requested from attributes")

	// Read the build time live from config so UI overrides take effect
	// immediately; an explicit per-build time (speculative candidates) wins.
	payloadBuildTime := b.cfg.PayloadBuildTime
	if buildTimeMs != 0 {
		payloadBuildTime = buildTimeMs
	}

	b.log.Infof("Allowing payload to build for: %dms", payloadBuildTime)

	// Wait for the EL to accumulate transactions, but abort early (with an error)
	// if the build is cancelled by a newer slot or the context deadline is hit,
	// rather than sleeping into a doomed getPayload call.
	buildTimer := time.NewTimer(time.Duration(payloadBuildTime) * time.Millisecond)
	defer buildTimer.Stop()

	select {
	case <-buildCtx.Done():
		return nil, fmt.Errorf("build aborted while waiting for payload: %w", buildCtx.Err())
	case <-buildTimer.C:
	}

	// Retrieve the built payload as the fork-agnostic union.
	resp, err := b.engineClient.GetPayloadAgnostic(buildCtx, engineVersion, payloadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get payload: %w", err)
	}

	enginePayload := resp.ExecutionPayload
	if enginePayload == nil {
		return nil, fmt.Errorf("getPayload returned no execution payload")
	}

	gasLimitOverride := b.resolveGasLimitOverride(buildCtx, attrs, beaconFork,
		targetGasLimit, enginePayload.GasLimit, enginePayload.GasUsed)

	// Inject our extra-data marker (and the gas limit override, if any) and
	// recompute the block hash on the typed payload.
	newHash, err := ModifyPayloadExtraData(
		enginePayload,
		resp.ExecutionRequests,
		[]byte(b.cfg.ExtraData),
		common.Hash(attrs.ParentBeaconBlockRoot),
		gasLimitOverride,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to modify payload extra data: %w", err)
	}

	// Single fork-independent conversions to the beacon types: the execution
	// payload and (Electra+) the execution requests are converted here, once,
	// so consumers never touch the raw engine forms.
	beaconPayload := beaconPayloadFromEngine(enginePayload, beaconFork)

	execRequests, err := ParseExecutionRequests(resp.ExecutionRequests, beaconFork)
	if err != nil {
		return nil, fmt.Errorf("failed to parse execution requests: %w", err)
	}

	blockValue := new(big.Int)
	if resp.BlockValue != nil {
		blockValue = resp.BlockValue.ToBig()
	}

	event := &Payload{
		Attributes:        attrs,
		ExecutionPayload:  beaconPayload,
		BlobsBundle:       beaconBlobsBundleFromEngine(resp.BlobsBundle),
		ExecutionRequests: execRequests,
		BlockHash:         phase0.Hash32(newHash),
		FeeRecipient:      proposerFeeRecipient,
		BlockValue:        blockValue,
		ReadyAt:           time.Now(),
	}

	b.log.WithFields(logrus.Fields{
		"slot":              attrs.ProposalSlot,
		"block_hash":        fmt.Sprintf("%x", newHash[:8]),
		"parent_hash":       fmt.Sprintf("%x", attrs.ParentBlockHash[:8]),
		"block_value":       blockValue.String(),
		"has_blobs":         resp.BlobsBundle != nil,
		"has_exec_requests": len(resp.ExecutionRequests) > 0,
		"txs_in_payload":    len(beaconPayload.Transactions),
		"target_gas_limit":  targetGasLimit,
		"payload_gas_limit": beaconPayload.GasLimit,
	}).Info("Payload built from attributes")

	return event, nil
}

// resolveGasLimitOverride returns the gas limit the built payload must carry
// per the bid gossip rules (the EL parent's gas limit stepped toward the
// proposer's target), or 0 when no override applies: the rule is disabled,
// the EL already produced the exact value, the parent gas limit is unknown,
// or the payload's gas usage exceeds the required limit.
func (b *PayloadBuilder) resolveGasLimitOverride(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
	beaconFork version.DataVersion,
	targetGasLimit, payloadGasLimit, payloadGasUsed uint64,
) uint64 {
	if !b.cfg.Build.EnforceBidGasLimit || beaconFork < version.DataVersionGloas || targetGasLimit == 0 {
		return 0
	}

	headTracker := b.chainSvc.GetHeadTracker()
	if headTracker == nil {
		return 0
	}

	_, parentGasLimit := headTracker.ResolveELParentMeta(ctx, attrs.ParentBlockRoot, attrs.ParentBlockHash)
	if parentGasLimit == 0 {
		return 0
	}

	expected := expectedBidGasLimit(parentGasLimit, targetGasLimit)
	if expected == payloadGasLimit {
		return 0
	}

	if payloadGasUsed > expected {
		b.log.WithFields(logrus.Fields{
			"slot":     attrs.ProposalSlot,
			"expected": expected,
			"built":    payloadGasLimit,
			"gas_used": payloadGasUsed,
		}).Error("Cannot enforce bid gas limit: payload gas usage exceeds the required limit")

		return 0
	}

	b.log.WithFields(logrus.Fields{
		"slot":     attrs.ProposalSlot,
		"parent":   parentGasLimit,
		"target":   targetGasLimit,
		"built":    payloadGasLimit,
		"enforced": expected,
	}).Warn("Overriding payload gas limit to the bid-gossip-required value")

	return expected
}

// AbortBuild aborts every active build for the given slot.
func (b *PayloadBuilder) AbortBuild(slot phase0.Slot) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for key, build := range b.activeBuilds {
		if key.slot == slot {
			build.cancelFn()
			delete(b.activeBuilds, key)

			b.log.WithField("slot", slot).Debug("Build aborted")
		}
	}
}
