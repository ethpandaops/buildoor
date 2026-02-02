package builder

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/attestantio/go-eth2-client/spec/capella"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
)

// PayloadBuilder handles execution payload building via the Engine API.
type PayloadBuilder struct {
	clClient         *beacon.Client
	engineClient     *engine.Client
	feeRecipient     common.Address
	payloadBuildTime uint64
	log              logrus.FieldLogger

	// Active build tracking
	activeBuild *activeBuild
	mu          sync.Mutex
}

// activeBuild tracks an in-progress payload build.
type activeBuild struct {
	slot      phase0.Slot
	payloadID engine.PayloadID
	cancelFn  context.CancelFunc
}

// NewPayloadBuilder creates a new payload builder.
func NewPayloadBuilder(
	clClient *beacon.Client,
	engineClient *engine.Client,
	feeRecipient common.Address,
	payloadBuildTime uint64,
	log logrus.FieldLogger,
) *PayloadBuilder {
	return &PayloadBuilder{
		clClient:         clClient,
		engineClient:     engineClient,
		feeRecipient:     feeRecipient,
		payloadBuildTime: payloadBuildTime,
		log:              log.WithField("component", "payload-builder"),
	}
}

// BuildPayloadFromAttributes builds a payload using data from a payload_attributes event.
// This is the primary build path, triggered when the beacon node emits payload_attributes.
// The event contains all necessary information: timestamp, randao, withdrawals, etc.
func (b *PayloadBuilder) BuildPayloadFromAttributes(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
) (*PayloadReadyEvent, error) {
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

	// Get finality info (still need safe/finalized block hashes)
	finalityInfo, err := b.clClient.GetFinalityInfo(buildCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to get finality info: %w", err)
	}


	// Convert hashes for engine API
	// parent_block_hash from payload_attributes is the execution layer parent
	headBlockHash := common.BytesToHash(attrs.ParentBlockHash[:])
	safeBlockHash := common.BytesToHash(finalityInfo.SafeExecutionBlockHash[:])
	finalizedBlockHash := common.BytesToHash(finalityInfo.FinalizedExecutionBlockHash[:])
	parentBeaconRoot := common.BytesToHash(attrs.ParentBeaconBlockRoot[:])

	// Convert withdrawals from payload_attributes to engine format
	engineWithdrawals := convertWithdrawalsToEngineFormat(attrs.Withdrawals)

	b.log.WithFields(logrus.Fields{
		"slot":             attrs.ProposalSlot,
		"timestamp":        attrs.Timestamp,
		"withdrawal_count": len(engineWithdrawals),
		"parent_hash":      fmt.Sprintf("%x", attrs.ParentBlockHash[:8]),
	}).Debug("Building payload from attributes")

	// Request payload build from the EL
	payloadID, err := b.engineClient.RequestPayloadBuild(
		buildCtx,
		headBlockHash,
		safeBlockHash,
		finalizedBlockHash,
		&engine.PayloadAttributes{
			Timestamp:             attrs.Timestamp,
			PrevRandao:            common.BytesToHash(attrs.PrevRandao[:]),
			SuggestedFeeRecipient: b.feeRecipient, // Use builder's fee recipient
			Withdrawals:           engineWithdrawals,
			ParentBeaconBlockRoot: &parentBeaconRoot,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to request payload build: %w", err)
	}

	b.mu.Lock()
	if b.activeBuild != nil && b.activeBuild.slot == attrs.ProposalSlot {
		b.activeBuild.payloadID = payloadID
	}
	b.mu.Unlock()

	b.log.WithFields(logrus.Fields{
		"slot":       attrs.ProposalSlot,
		"payload_id": fmt.Sprintf("%x", payloadID[:]),
	}).Debug("Payload build requested from attributes")

	b.log.Infof("Allowing payload to build for: %dms", b.payloadBuildTime)
	time.Sleep(time.Duration(b.payloadBuildTime) * time.Millisecond)

	// Get the built payload with all components (blobs, execution requests) as typed values
	payloadResult, err := b.engineClient.GetPayloadRaw(buildCtx, payloadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get payload: %w", err)
	}

	payload := payloadResult.ExecutionPayload
	var blockHash phase0.Hash32
	copy(blockHash[:], payload.BlockHash[:])

	var blockValueGwei uint64
	if payloadResult.BlockValue != nil {
		blockValueGwei = payloadResult.BlockValue.Uint64()
	}

	txCount := len(payload.Transactions)

	event := &PayloadReadyEvent{
		Slot:              attrs.ProposalSlot,
		ParentBlockRoot:   attrs.ParentBlockRoot,
		ParentBlockHash:   attrs.ParentBlockHash,
		BlockHash:         blockHash,
		Payload:           payload,
		BlobsBundle:       payloadResult.BlobsBundle,
		ExecutionRequests: payloadResult.ExecutionRequests,
		Timestamp:         attrs.Timestamp,
		GasLimit:          payload.GasLimit,
		PrevRandao:        attrs.PrevRandao,
		FeeRecipient:      b.feeRecipient,
		BlockValue:        blockValueGwei,
		BuildSource:       BuildSourceBlock,
		ReadyAt:           time.Now(),
	}

	b.log.WithFields(logrus.Fields{
		"slot":              attrs.ProposalSlot,
		"block_hash":        fmt.Sprintf("%x", blockHash[:8]),
		"parent_hash":       fmt.Sprintf("%x", attrs.ParentBlockHash[:8]),
		"block_value":       blockValueGwei,
		"has_blobs":         payloadResult.BlobsBundle != nil,
		"has_exec_requests": len(payloadResult.ExecutionRequests) > 0,
		"txs_in_payload":    txCount,
	}).Debug("Payload built from attributes")

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

// SetFeeRecipient updates the fee recipient address.
func (b *PayloadBuilder) SetFeeRecipient(feeRecipient common.Address) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.feeRecipient = feeRecipient
}

// convertWithdrawalsToEngineFormat converts CL withdrawals to engine API format.
// Always returns a non-nil slice (empty if input is nil).
func convertWithdrawalsToEngineFormat(clWithdrawals []*capella.Withdrawal) []*types.Withdrawal {
	if clWithdrawals == nil {
		return make([]*types.Withdrawal, 0)
	}

	result := make([]*types.Withdrawal, len(clWithdrawals))

	for i, w := range clWithdrawals {
		result[i] = &types.Withdrawal{
			Index:     uint64(w.Index),
			Validator: uint64(w.ValidatorIndex),
			Address:   common.Address(w.Address),
			Amount:    uint64(w.Amount),
		}
	}

	return result
}

// BuildContext contains contextual information for building a payload.
type BuildContext struct {
	Slot             phase0.Slot
	HeadBlockHash    common.Hash
	SafeBlockHash    common.Hash
	FinalBlockHash   common.Hash
	ParentBeaconRoot common.Hash
	Timestamp        uint64
	PrevRandao       common.Hash
	Withdrawals      []*types.Withdrawal
}

// BuildPayloadWithContext builds a payload using explicit context values.
// This provides more control over the build parameters.
func (b *PayloadBuilder) BuildPayloadWithContext(
	ctx context.Context,
	buildCtx *BuildContext,
) (*engine.ExecutionPayload, error) {
	payloadID, err := b.engineClient.RequestPayloadBuild(
		ctx,
		buildCtx.HeadBlockHash,
		buildCtx.SafeBlockHash,
		buildCtx.FinalBlockHash,
		&engine.PayloadAttributes{
			Timestamp:             buildCtx.Timestamp,
			PrevRandao:            buildCtx.PrevRandao,
			SuggestedFeeRecipient: b.feeRecipient,
			Withdrawals:           buildCtx.Withdrawals,
			ParentBeaconBlockRoot: &buildCtx.ParentBeaconRoot,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to request payload build: %w", err)
	}

	result, err := b.engineClient.GetPayloadRaw(ctx, payloadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get payload: %w", err)
	}

	return result.ExecutionPayload, nil
}
