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

	"github.com/ethpandaops/buildoor/pkg/builderapi/validators"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/proposerpreferences"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// PayloadBuilder handles execution payload building via the Engine API.
type PayloadBuilder struct {
	clClient     *beacon.Client
	engineClient EngineClient
	chainSvc     chain.Service
	feeRecipient common.Address

	validatorStore *validators.Store          // optional: use fee recipient from validator registrations (pre-Gloas)
	propPrefCache  *proposerpreferences.Cache // optional: proposer preferences cache (Gloas+)
	cfg            *config.Config             // shared config; mutable settings are read live, never cached
	log            logrus.FieldLogger

	// Active build tracking
	activeBuild *activeBuild
	mu          sync.Mutex
}

// activeBuild tracks an in-progress payload build.
type activeBuild struct {
	slot      phase0.Slot
	payloadID paris.PayloadID
	cancelFn  context.CancelFunc
}

// NewPayloadBuilder creates a new payload builder.
// cfg is the shared config pointer; mutable settings (e.g. PayloadBuildTime) are read live from it.
// When validatorStore is set (pre-Gloas), fee recipient is taken from the proposer's validator registration.
// When propPrefCache is set and the build epoch is Gloas+, fee recipient and gas limit come from proposer preferences instead.
func NewPayloadBuilder(
	clClient *beacon.Client,
	engineClient EngineClient,
	chainSvc chain.Service,
	feeRecipient common.Address,
	cfg *config.Config,
	log logrus.FieldLogger,
	validatorStore *validators.Store,
	propPrefCache *proposerpreferences.Cache,
) *PayloadBuilder {
	return &PayloadBuilder{
		clClient:       clClient,
		chainSvc:       chainSvc,
		engineClient:   engineClient,
		feeRecipient:   feeRecipient,
		validatorStore: validatorStore,
		propPrefCache:  propPrefCache,
		cfg:            cfg,
		log:            log.WithField("component", "payload-builder"),
	}
}

// BuildPayloadFromAttributes builds a payload using data from a payload_attributes event.
// This is the primary build path, triggered when the beacon node emits payload_attributes.
// The event contains all necessary information: timestamp, randao, withdrawals, etc.
func (b *PayloadBuilder) BuildPayloadFromAttributes(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
) (*Payload, error) {
	b.mu.Lock()

	// Cancel any existing build for a different slot
	if b.activeBuild != nil && b.activeBuild.slot != attrs.ProposalSlot {
		b.activeBuild.cancelFn()
		b.activeBuild = nil
	}

	buildCtx, cancel := context.WithCancel(ctx)

	b.activeBuild = &activeBuild{
		slot:     attrs.ProposalSlot,
		cancelFn: cancel,
	}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		if b.activeBuild != nil && b.activeBuild.slot == attrs.ProposalSlot {
			b.activeBuild = nil
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

	// Resolve the fee recipient (and target gas limit) for the build.
	// Post-Gloas: use proposer preferences (fee_recipient + gas_limit), falling
	//             back to SuggestedFeeRecipient from payload_attributes.
	// Pre-Gloas:  use validator registrations (fee_recipient from registerValidator).
	// Fallback:   the builder's configured fee recipient.
	proposerFeeRecipient := b.feeRecipient
	var targetGasLimit uint64

	if beaconFork >= version.DataVersionGloas {
		if b.propPrefCache != nil {
			if prefs, ok := b.propPrefCache.Get(attrs.ProposalSlot); ok && prefs.Message != nil {
				proposerFeeRecipient = common.Address(prefs.Message.FeeRecipient)
				targetGasLimit = prefs.Message.TargetGasLimit
				b.log.WithFields(logrus.Fields{
					"proposer_index":   attrs.ProposerIndex,
					"fee_recipient":    proposerFeeRecipient.Hex(),
					"target_gas_limit": prefs.Message.TargetGasLimit,
				}).Debug("Using fee recipient and gas limit from proposer preferences")
			}
		}
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
	} else if b.validatorStore != nil {
		// Pre-Gloas: look up fee recipient from validator registrations.
		pubkeyPtr := b.chainSvc.GetValidatorPubkeyByIndex(attrs.ProposerIndex)
		if pubkeyPtr != nil {
			reg := b.validatorStore.Get(*pubkeyPtr)
			if reg != nil && reg.Message != nil {
				proposerFeeRecipient = common.Address(reg.Message.FeeRecipient)
				b.log.WithFields(logrus.Fields{
					"proposer_index": attrs.ProposerIndex,
					"pubkey":         fmt.Sprintf("%x", pubkeyPtr[:8]),
					"fee_recipient":  proposerFeeRecipient.Hex(),
				}).Debug("Using fee recipient from validator registration")
			}
		}
	}

	// Build the fork-agnostic payload attributes and forkchoice request. The
	// engine client dispatches to the correct engine_forkchoiceUpdated version.
	payloadAttrs := &engineall.PayloadAttributes{
		Version:               engineVersion,
		Timestamp:             attrs.Timestamp,
		PrevRandao:            paris.Hash32(attrs.PrevRandao),
		SuggestedFeeRecipient: paris.Address(b.feeRecipient),
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
	if b.activeBuild != nil && b.activeBuild.slot == attrs.ProposalSlot {
		b.activeBuild.payloadID = payloadID
	}
	b.mu.Unlock()

	b.log.WithFields(logrus.Fields{
		"slot":       attrs.ProposalSlot,
		"payload_id": fmt.Sprintf("%x", payloadID[:]),
	}).Debug("Payload build requested from attributes")

	// Read the build time live from config so UI overrides take effect immediately.
	payloadBuildTime := b.cfg.PayloadBuildTime

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

	// Inject our extra-data marker and recompute the block hash on the typed payload.
	newHash, err := ModifyPayloadExtraData(
		enginePayload,
		resp.ExecutionRequests,
		[]byte("buildoor/"),
		common.Hash(attrs.ParentBeaconBlockRoot),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to modify payload extra data: %w", err)
	}

	// Single fork-independent conversions to the beacon types: the execution
	// payload and (Electra+) the execution requests are converted here, once,
	// so consumers never touch the raw engine forms.
	beaconPayload := beaconPayloadFromEngine(enginePayload, beaconFork)

	execRequests, err := ParseExecutionRequests(resp.ExecutionRequests)
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
		"parent_hash":       finalityInfo.HeadExecutionBlockHash,
		"block_value":       blockValue.String(),
		"has_blobs":         resp.BlobsBundle != nil,
		"has_exec_requests": len(resp.ExecutionRequests) > 0,
		"txs_in_payload":    len(beaconPayload.Transactions),
		"target_gas_limit":  targetGasLimit,
		"payload_gas_limit": beaconPayload.GasLimit,
	}).Info("Payload built from attributes")

	return event, nil
}

// AbortBuild aborts any active build for the given slot.
func (b *PayloadBuilder) AbortBuild(slot phase0.Slot) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.activeBuild != nil && b.activeBuild.slot == slot {
		b.activeBuild.cancelFn()
		b.activeBuild = nil

		b.log.WithField("slot", slot).Debug("Build aborted")
	}
}
