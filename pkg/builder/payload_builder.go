package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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
	clClient     *beacon.Client
	engineClient *engine.Client
	chainSpec    *beacon.ChainSpec
	genesis      *beacon.Genesis
	feeRecipient common.Address
	log          logrus.FieldLogger

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
	chainSpec *beacon.ChainSpec,
	genesis *beacon.Genesis,
	feeRecipient common.Address,
	log logrus.FieldLogger,
) *PayloadBuilder {
	return &PayloadBuilder{
		clClient:     clClient,
		engineClient: engineClient,
		chainSpec:    chainSpec,
		genesis:      genesis,
		feeRecipient: feeRecipient,
		log:          log.WithField("component", "payload-builder"),
	}
}

// BuildPayload builds a payload for the given slot based on a head event.
// This is the primary build path for Electra/Fulu, triggered when a parent block is received.
// In these forks, the execution payload is in the beacon block.
func (b *PayloadBuilder) BuildPayload(
	ctx context.Context,
	slot phase0.Slot,
	headEvent *beacon.HeadEvent,
) (*PayloadReadyEvent, error) {
	b.mu.Lock()

	// Cancel any existing build for a different slot
	if b.activeBuild != nil && b.activeBuild.slot != slot {
		b.activeBuild.cancelFn()
		b.activeBuild = nil
	}

	// Create a cancellable context for this build
	buildCtx, cancel := context.WithCancel(ctx)

	b.activeBuild = &activeBuild{
		slot:     slot,
		cancelFn: cancel,
	}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		if b.activeBuild != nil && b.activeBuild.slot == slot {
			b.activeBuild = nil
		}
		b.mu.Unlock()
	}()

	// Get finality info from beacon node
	finalityInfo, err := b.clClient.GetFinalityInfo(buildCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to get finality info: %w", err)
	}

	// Get RANDAO from beacon state
	randao, err := b.clClient.GetRandao(buildCtx, "head")
	if err != nil {
		return nil, fmt.Errorf("failed to get randao: %w", err)
	}

	// Calculate timestamp for the target slot
	slotTime := b.clClient.SlotToTime(b.genesis, b.chainSpec, slot)
	timestamp := uint64(slotTime.Unix())

	// Convert hashes for engine API
	// For pre-Gloas: head execution block hash comes from finality info
	headBlockHash := common.BytesToHash(finalityInfo.HeadExecutionBlockHash[:])
	safeBlockHash := common.BytesToHash(finalityInfo.SafeExecutionBlockHash[:])
	finalizedBlockHash := common.BytesToHash(finalityInfo.FinalizedExecutionBlockHash[:])
	parentBeaconRoot := common.BytesToHash(headEvent.Block[:])

	// Get expected withdrawals from cached state
	expectedWithdrawals, err := b.clClient.GetExpectedWithdrawals(
		headEvent.Block,
		b.chainSpec.SlotsPerEpoch,
	)
	if err != nil {
		b.log.WithError(err).Warn("Failed to get expected withdrawals, using empty list")
	}

	engineWithdrawals := convertWithdrawalsToEngineFormat(expectedWithdrawals)

	b.log.WithField("withdrawal_count", len(engineWithdrawals)).Debug("Including withdrawals in payload")

	// Request payload build from the EL
	payloadID, err := b.engineClient.RequestPayloadBuild(
		buildCtx,
		headBlockHash,
		safeBlockHash,
		finalizedBlockHash,
		&engine.PayloadAttributes{
			Timestamp:             timestamp,
			PrevRandao:            common.BytesToHash(randao[:]),
			SuggestedFeeRecipient: b.feeRecipient,
			Withdrawals:           engineWithdrawals,
			ParentBeaconBlockRoot: &parentBeaconRoot,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to request payload build: %w", err)
	}

	b.mu.Lock()
	if b.activeBuild != nil && b.activeBuild.slot == slot {
		b.activeBuild.payloadID = payloadID
	}
	b.mu.Unlock()

	b.log.WithFields(logrus.Fields{
		"slot":       slot,
		"payload_id": fmt.Sprintf("%x", payloadID[:]),
	}).Debug("Payload build requested")

	// Get the built payload
	payloadJSON, blockValue, err := b.engineClient.GetPayloadRaw(buildCtx, payloadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get payload: %w", err)
	}

	// Parse block hash and fields from the payload
	blockHashCommon, err := engine.ParseBlockHashFromPayload(payloadJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to parse block hash from payload: %w", err)
	}

	var blockHash phase0.Hash32

	copy(blockHash[:], blockHashCommon[:])

	payloadFields, err := engine.ParsePayloadFields(payloadJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to parse payload fields: %w", err)
	}

	gasLimit, _ := strconv.ParseUint(strings.TrimPrefix(payloadFields.GasLimit, "0x"), 16, 64)

	var parentBlockHash phase0.Hash32

	parentHashBytes := common.HexToHash(payloadFields.ParentHash)

	copy(parentBlockHash[:], parentHashBytes[:])

	// Calculate block value in gwei
	var blockValueGwei uint64
	if blockValue != nil {
		blockValueGwei = blockValue.Uint64()
	}

	event := &PayloadReadyEvent{
		Slot:            slot,
		ParentBlockRoot: headEvent.Block,
		ParentBlockHash: parentBlockHash,
		BlockHash:       blockHash,
		Payload:         payloadJSON,
		Timestamp:       timestamp,
		GasLimit:        gasLimit,
		PrevRandao:      randao,
		FeeRecipient:    b.feeRecipient,
		BlockValue:      blockValueGwei,
		BuildSource:     BuildSourceBlock,
		ReadyAt:         time.Now(),
	}

	b.log.WithFields(logrus.Fields{
		"slot":        slot,
		"block_hash":  fmt.Sprintf("%x", blockHash[:8]),
		"parent_hash": fmt.Sprintf("%x", parentBlockHash[:8]),
		"gas_limit":   gasLimit,
		"block_value": blockValueGwei,
	}).Debug("Payload built via engine API (pre-Gloas)")

	return event, nil
}

// BuildPayloadGloas builds a payload for Gloas fork.
// In Gloas, execution payloads are separate from beacon blocks.
// We use the last known execution block hash as the parent EL block.
func (b *PayloadBuilder) BuildPayloadGloas(
	ctx context.Context,
	slot phase0.Slot,
	headEvent *beacon.HeadEvent,
	lastKnownPayloadBlockRoot phase0.Root,
	lastKnownPayloadBlockHash phase0.Hash32,
) (*PayloadReadyEvent, error) {
	b.mu.Lock()

	// Cancel any existing build for a different slot
	if b.activeBuild != nil && b.activeBuild.slot != slot {
		b.activeBuild.cancelFn()
		b.activeBuild = nil
	}

	buildCtx, cancel := context.WithCancel(ctx)

	b.activeBuild = &activeBuild{
		slot:     slot,
		cancelFn: cancel,
	}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		if b.activeBuild != nil && b.activeBuild.slot == slot {
			b.activeBuild = nil
		}
		b.mu.Unlock()
	}()

	// Get RANDAO from beacon state
	randao, err := b.clClient.GetRandao(buildCtx, "head")
	if err != nil {
		return nil, fmt.Errorf("failed to get randao: %w", err)
	}

	// Calculate timestamp for the target slot
	slotTime := b.clClient.SlotToTime(b.genesis, b.chainSpec, slot)
	timestamp := uint64(slotTime.Unix())

	// For Gloas: use the last known execution block hash as head
	// The head beacon block doesn't have its payload yet
	headBlockHash := common.BytesToHash(lastKnownPayloadBlockHash[:])
	parentBeaconRoot := common.BytesToHash(headEvent.Block[:])

	// For safe and finalized, we need to query the beacon node
	finalityInfo, err := b.clClient.GetFinalityInfo(buildCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to get finality info: %w", err)
	}

	safeBlockHash := common.BytesToHash(finalityInfo.SafeExecutionBlockHash[:])
	finalizedBlockHash := common.BytesToHash(finalityInfo.FinalizedExecutionBlockHash[:])

	// Get expected withdrawals from the current head's state
	expectedWithdrawals, err := b.clClient.GetExpectedWithdrawals(
		headEvent.Block,
		b.chainSpec.SlotsPerEpoch,
	)
	if err != nil {
		b.log.WithError(err).Warn("Failed to get expected withdrawals, using empty list")
	}

	engineWithdrawals := convertWithdrawalsToEngineFormat(expectedWithdrawals)

	// Request payload build
	payloadID, err := b.engineClient.RequestPayloadBuild(
		buildCtx,
		headBlockHash,
		safeBlockHash,
		finalizedBlockHash,
		&engine.PayloadAttributes{
			Timestamp:             timestamp,
			PrevRandao:            common.BytesToHash(randao[:]),
			SuggestedFeeRecipient: b.feeRecipient,
			Withdrawals:           engineWithdrawals,
			ParentBeaconBlockRoot: &parentBeaconRoot,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to request payload build: %w", err)
	}

	b.mu.Lock()
	if b.activeBuild != nil && b.activeBuild.slot == slot {
		b.activeBuild.payloadID = payloadID
	}
	b.mu.Unlock()

	// Get the built payload
	payloadJSON, blockValue, err := b.engineClient.GetPayloadRaw(buildCtx, payloadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get payload: %w", err)
	}

	// Parse block hash and fields
	blockHashCommon, err := engine.ParseBlockHashFromPayload(payloadJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to parse block hash from payload: %w", err)
	}

	var blockHash phase0.Hash32

	copy(blockHash[:], blockHashCommon[:])

	payloadFields, err := engine.ParsePayloadFields(payloadJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to parse payload fields: %w", err)
	}

	gasLimit, _ := strconv.ParseUint(strings.TrimPrefix(payloadFields.GasLimit, "0x"), 16, 64)

	var blockValueGwei uint64
	if blockValue != nil {
		blockValueGwei = blockValue.Uint64()
	}

	event := &PayloadReadyEvent{
		Slot:            slot,
		ParentBlockRoot: headEvent.Block,
		ParentBlockHash: lastKnownPayloadBlockHash, // The EL parent is the last known payload
		BlockHash:       blockHash,
		Payload:         payloadJSON,
		Timestamp:       timestamp,
		GasLimit:        gasLimit,
		PrevRandao:      randao,
		FeeRecipient:    b.feeRecipient,
		BlockValue:      blockValueGwei,
		BuildSource:     BuildSourceBlock,
		ReadyAt:         time.Now(),
	}

	b.log.WithFields(logrus.Fields{
		"slot":               slot,
		"block_hash":         fmt.Sprintf("%x", blockHash[:8]),
		"parent_el_hash":     fmt.Sprintf("%x", lastKnownPayloadBlockHash[:8]),
		"parent_beacon_root": fmt.Sprintf("%x", headEvent.Block[:8]),
	}).Debug("Payload built via engine API (Gloas)")

	return event, nil
}

// BuildPayloadOnEnvelope builds a payload when a payload envelope is received.
// This is called when we receive a payload envelope event (Gloas only).
// The envelope completes the parent block, so we can now build on it.
func (b *PayloadBuilder) BuildPayloadOnEnvelope(
	ctx context.Context,
	slot phase0.Slot,
	envelope *beacon.PayloadEnvelopeEvent,
) (*PayloadReadyEvent, error) {
	b.mu.Lock()

	// Cancel any existing build for a different slot
	if b.activeBuild != nil && b.activeBuild.slot != slot {
		b.activeBuild.cancelFn()
		b.activeBuild = nil
	}

	buildCtx, cancel := context.WithCancel(ctx)

	b.activeBuild = &activeBuild{
		slot:     slot,
		cancelFn: cancel,
	}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		if b.activeBuild != nil && b.activeBuild.slot == slot {
			b.activeBuild = nil
		}
		b.mu.Unlock()
	}()

	// Get RANDAO from beacon state
	randao, err := b.clClient.GetRandao(buildCtx, "head")
	if err != nil {
		return nil, fmt.Errorf("failed to get randao: %w", err)
	}

	// Calculate timestamp for the target slot
	slotTime := b.clClient.SlotToTime(b.genesis, b.chainSpec, slot)
	timestamp := uint64(slotTime.Unix())

	// Use the envelope's block hash as the head
	headBlockHash := common.BytesToHash(envelope.BlockHash[:])
	parentBeaconRoot := common.BytesToHash(envelope.BlockRoot[:])

	// For safe and finalized, query the beacon node
	finalityInfo, err := b.clClient.GetFinalityInfo(buildCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to get finality info: %w", err)
	}

	safeBlockHash := common.BytesToHash(finalityInfo.SafeExecutionBlockHash[:])
	finalizedBlockHash := common.BytesToHash(finalityInfo.FinalizedExecutionBlockHash[:])

	// Get expected withdrawals
	expectedWithdrawals, err := b.clClient.GetExpectedWithdrawals(
		envelope.BlockRoot,
		b.chainSpec.SlotsPerEpoch,
	)
	if err != nil {
		b.log.WithError(err).Warn("Failed to get expected withdrawals, using empty list")
	}

	engineWithdrawals := convertWithdrawalsToEngineFormat(expectedWithdrawals)

	// Request payload build
	payloadID, err := b.engineClient.RequestPayloadBuild(
		buildCtx,
		headBlockHash,
		safeBlockHash,
		finalizedBlockHash,
		&engine.PayloadAttributes{
			Timestamp:             timestamp,
			PrevRandao:            common.BytesToHash(randao[:]),
			SuggestedFeeRecipient: b.feeRecipient,
			Withdrawals:           engineWithdrawals,
			ParentBeaconBlockRoot: &parentBeaconRoot,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to request payload build: %w", err)
	}

	b.mu.Lock()
	if b.activeBuild != nil && b.activeBuild.slot == slot {
		b.activeBuild.payloadID = payloadID
	}
	b.mu.Unlock()

	// Get the built payload
	payloadJSON, blockValue, err := b.engineClient.GetPayloadRaw(buildCtx, payloadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get payload: %w", err)
	}

	// Parse block hash and fields
	blockHashCommon, err := engine.ParseBlockHashFromPayload(payloadJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to parse block hash from payload: %w", err)
	}

	var blockHash phase0.Hash32

	copy(blockHash[:], blockHashCommon[:])

	payloadFields, err := engine.ParsePayloadFields(payloadJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to parse payload fields: %w", err)
	}

	gasLimit, _ := strconv.ParseUint(strings.TrimPrefix(payloadFields.GasLimit, "0x"), 16, 64)

	var blockValueGwei uint64
	if blockValue != nil {
		blockValueGwei = blockValue.Uint64()
	}

	event := &PayloadReadyEvent{
		Slot:            slot,
		ParentBlockRoot: envelope.BlockRoot,
		ParentBlockHash: envelope.BlockHash,
		BlockHash:       blockHash,
		Payload:         payloadJSON,
		Timestamp:       timestamp,
		GasLimit:        gasLimit,
		PrevRandao:      randao,
		FeeRecipient:    b.feeRecipient,
		BlockValue:      blockValueGwei,
		BuildSource:     BuildSourcePayload,
		ReadyAt:         time.Now(),
	}

	b.log.WithFields(logrus.Fields{
		"slot":        slot,
		"block_hash":  fmt.Sprintf("%x", blockHash[:8]),
		"parent_hash": fmt.Sprintf("%x", envelope.BlockHash[:8]),
		"build_on":    "payload_envelope",
	}).Debug("Payload built on payload envelope (Gloas)")

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
) (json.RawMessage, error) {
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

	payloadJSON, _, err := b.engineClient.GetPayloadRaw(ctx, payloadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get payload: %w", err)
	}

	return payloadJSON, nil
}
