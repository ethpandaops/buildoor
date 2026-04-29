package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethpandaops/go-eth2-client/spec/capella"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builderapi/validators"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/proposerpreferences"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
)

// PayloadBuilder handles execution payload building via the Engine API.
type PayloadBuilder struct {
	clClient     *beacon.Client
	engineClient *engine.Client
	feeRecipient common.Address

	validatorStore      *validators.Store          // optional: use fee recipient from validator registrations (pre-Gloas)
	validatorIndexCache *chain.ValidatorIndexCache // optional: index→pubkey so we don't query beacon state every build
	propPrefCache       *proposerpreferences.Cache // optional: proposer preferences cache (Gloas+)
	isGloas             func() bool                // returns true when on the Gloas fork
	payloadBuildTime    uint64
	log                 logrus.FieldLogger

	// Active build tracking, keyed by (slot, variant) so the FULL and EMPTY
	// builds for the same slot don't trample each other.
	activeBuilds map[buildKey]*activeBuild
	mu           sync.Mutex
}

// buildKey identifies an in-flight build by slot and variant.
type buildKey struct {
	slot    phase0.Slot
	variant PayloadVariant
}

// activeBuild tracks an in-progress payload build.
type activeBuild struct {
	slot      phase0.Slot
	variant   PayloadVariant
	payloadID engine.PayloadID
	cancelFn  context.CancelFunc
}

// NewPayloadBuilder creates a new payload builder.
// When validatorStore is set (pre-Gloas), fee recipient is taken from the proposer's validator registration.
// When propPrefCache is set and isGloas returns true, fee recipient and gas limit come from proposer preferences instead.
// validatorIndexCache is optional; when set we use it to resolve proposer index→pubkey instead of querying beacon state every build.
func NewPayloadBuilder(
	clClient *beacon.Client,
	engineClient *engine.Client,
	feeRecipient common.Address,
	payloadBuildTime uint64,
	log logrus.FieldLogger,
	validatorStore *validators.Store,
	validatorIndexCache *chain.ValidatorIndexCache,
	propPrefCache *proposerpreferences.Cache,
	isGloas func() bool,
) *PayloadBuilder {
	return &PayloadBuilder{
		clClient:            clClient,
		engineClient:        engineClient,
		feeRecipient:        feeRecipient,
		validatorStore:      validatorStore,
		validatorIndexCache: validatorIndexCache,
		propPrefCache:       propPrefCache,
		isGloas:             isGloas,
		payloadBuildTime:    payloadBuildTime,
		log:                 log.WithField("component", "payload-builder"),
		activeBuilds:        make(map[buildKey]*activeBuild),
	}
}

// BuildPayloadFromAttributes builds a payload using data from a payload_attributes event.
// This is the primary build path, triggered when the beacon node emits payload_attributes.
//
// variant indicates whether to build assuming the parent slot was published (FULL) or
// missed (EMPTY). headBlockHashOverride, when non-zero, is used as the FCU head — this
// is how the caller passes the bid-derived head:
//   - FULL: caller passes bid.block_hash (== attrs.ParentBlockHash in normal operation).
//   - EMPTY: caller passes bid.parent_block_hash (the grandparent EL block).
//
// When headBlockHashOverride is the zero hash, attrs.ParentBlockHash is used (legacy
// pre-Gloas single-build path).
func (b *PayloadBuilder) BuildPayloadFromAttributes(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
	variant PayloadVariant,
	headBlockHashOverride phase0.Hash32,
) (*PayloadReadyEvent, error) {
	key := buildKey{slot: attrs.ProposalSlot, variant: variant}

	b.mu.Lock()

	// Cancel any existing builds for older slots; same-slot/same-variant rebuild
	// cancels the prior; same-slot/other-variant is left alone.
	for k, ab := range b.activeBuilds {
		if k.slot != attrs.ProposalSlot {
			ab.cancelFn()
			delete(b.activeBuilds, k)
		}
	}
	if existing, ok := b.activeBuilds[key]; ok {
		existing.cancelFn()
		delete(b.activeBuilds, key)
	}

	buildCtx, cancel := context.WithCancel(ctx)

	b.activeBuilds[key] = &activeBuild{
		slot:     attrs.ProposalSlot,
		variant:  variant,
		cancelFn: cancel,
	}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		if ab, ok := b.activeBuilds[key]; ok && ab.cancelFn != nil {
			delete(b.activeBuilds, key)
		}
		b.mu.Unlock()
	}()

	// Get finality info (still need safe/finalized block hashes)
	finalityInfo, err := b.clClient.GetFinalityInfo(buildCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to get finality info: %w", err)
	}

	// Choose the FCU head block hash. The caller-supplied variant-specific override
	// wins; otherwise fall back to attrs.ParentBlockHash (pre-Gloas single-build).
	var chosenHead phase0.Hash32
	if headBlockHashOverride != (phase0.Hash32{}) {
		chosenHead = headBlockHashOverride
	} else {
		chosenHead = attrs.ParentBlockHash
	}

	headBlockHash := common.BytesToHash(chosenHead[:])
	safeBlockHash := common.BytesToHash(finalityInfo.SafeExecutionBlockHash[:])
	finalizedBlockHash := common.BytesToHash(finalityInfo.FinalizedExecutionBlockHash[:])
	parentBeaconRoot := common.BytesToHash(attrs.ParentBeaconBlockRoot[:])

	// Convert withdrawals from payload_attributes to engine format
	engineWithdrawals := convertWithdrawalsToEngineFormat(attrs.Withdrawals)

	// Get fee recipient for build.
	// Post-Gloas: use proposer preferences (fee_recipient + gas_limit from the proposer's signed preferences).
	//             Fall back to SuggestedFeeRecipient from payload_attributes (always available from BN).
	// Pre-Gloas:  use validator registrations (fee_recipient from the proposer's registerValidator message).
	// Fallback:   use the builder's configured fee recipient.
	proposerFeeRecipient := b.feeRecipient

	if b.isGloas != nil && b.isGloas() {
		// Gloas: prefer proposer preferences from cache, fall back to payload_attributes suggested fee recipient.
		if b.propPrefCache != nil {
			if prefs, ok := b.propPrefCache.Get(attrs.ProposalSlot); ok && prefs.Message != nil {
				proposerFeeRecipient = common.Address(prefs.Message.FeeRecipient)
				b.log.WithFields(logrus.Fields{
					"proposer_index": attrs.ProposerIndex,
					"fee_recipient":  proposerFeeRecipient.Hex(),
					"gas_limit":      prefs.Message.GasLimit,
				}).Debug("Using fee recipient and gas limit from proposer preferences")
			}
		}

		// If we still have the default fee recipient, use SuggestedFeeRecipient from payload_attributes.
		// This ensures bids match the proposer's expected fee recipient even when preferences
		// aren't received via SSE (e.g. same-node P2P broadcast doesn't loop back).
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
		var pubkey phase0.BLSPubKey
		var ok bool
		if b.validatorIndexCache != nil {
			pubkey, ok = b.validatorIndexCache.Get(attrs.ProposerIndex)
		} else {
			var err error
			pubkey, err = b.clClient.GetValidatorPubkeyByIndex(buildCtx, "head", attrs.ProposerIndex)
			ok = (err == nil)
		}
		if ok {
			reg := b.validatorStore.Get(pubkey)
			if reg != nil && reg.Message != nil {
				proposerFeeRecipient = common.Address(reg.Message.FeeRecipient)
				b.log.WithFields(logrus.Fields{
					"proposer_index": attrs.ProposerIndex,
					"pubkey":         fmt.Sprintf("%x", pubkey[:8]),
					"fee_recipient":  proposerFeeRecipient.Hex(),
				}).Debug("Using fee recipient from validator registration")
			}
		}
	}

	b.log.WithFields(logrus.Fields{
		"slot":             attrs.ProposalSlot,
		"variant":          variant.String(),
		"timestamp":        attrs.Timestamp,
		"withdrawal_count": len(engineWithdrawals),
		"head_hash":        fmt.Sprintf("%x", chosenHead[:8]),
		"attrs_parent":     fmt.Sprintf("%x", attrs.ParentBlockHash[:8]),
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
			SuggestedFeeRecipient: b.feeRecipient,
			Withdrawals:           engineWithdrawals,
			ParentBeaconBlockRoot: &parentBeaconRoot,
			SlotNumber:            uint64(attrs.ProposalSlot),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to request payload build: %w", err)
	}

	b.mu.Lock()
	if ab, ok := b.activeBuilds[key]; ok {
		ab.payloadID = payloadID
	}
	b.mu.Unlock()

	b.log.WithFields(logrus.Fields{
		"slot":       attrs.ProposalSlot,
		"variant":    variant.String(),
		"payload_id": fmt.Sprintf("%x", payloadID[:]),
	}).Debug("Payload build requested from attributes")

	b.log.Infof("Allowing payload to build for: %dms", b.payloadBuildTime)
	time.Sleep(time.Duration(b.payloadBuildTime) * time.Millisecond)

	// Get the built payload with all components (blobs, execution requests) as typed values
	payloadResult, err := b.engineClient.GetPayloadRaw(buildCtx, payloadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get payload: %w", err)
	}

	modifiedPayloadJSON, _, err := ModifyPayloadExtraData(
		payloadResult.ExecutionPayloadJSON,
		[]byte("buildoor/"),
		parentBeaconRoot,
		payloadResult.ExecutionRequests,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to modify payload extra data: %w", err)
	}

	var modifiedPayload engine.ExecutionPayload
	if err := json.Unmarshal(modifiedPayloadJSON, &modifiedPayload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal modified payload: %w", err)
	}

	payload := &modifiedPayload
	payloadResult.ExecutionPayload = payload
	var blockHash phase0.Hash32
	copy(blockHash[:], payload.BlockHash[:])

	var blockValueGwei uint64
	if payloadResult.BlockValue != nil {
		// BlockValue from engine API is in wei; convert to gwei for bid values.
		gweiValue := new(big.Int).Div(payloadResult.BlockValue, big.NewInt(1_000_000_000))
		blockValueGwei = gweiValue.Uint64()
	}

	txCount := len(payload.Transactions)

	event := &PayloadReadyEvent{
		Slot:              attrs.ProposalSlot,
		ParentBlockRoot:   attrs.ParentBlockRoot,
		ParentBlockHash:   chosenHead,
		BlockHash:         blockHash,
		Payload:           payload,
		BlobsBundle:       payloadResult.BlobsBundle,
		ExecutionRequests: payloadResult.ExecutionRequests,
		Timestamp:         attrs.Timestamp,
		GasLimit:          payload.GasLimit,
		PrevRandao:        attrs.PrevRandao,
		FeeRecipient:      proposerFeeRecipient,
		BlockValue:        blockValueGwei,
		BuildSource:       BuildSourceBlock,
		Variant:           variant,
		ReadyAt:           time.Now(),
	}

	b.log.WithFields(logrus.Fields{
		"slot":              attrs.ProposalSlot,
		"variant":           variant.String(),
		"block_hash":        fmt.Sprintf("%x", blockHash[:8]),
		"parent_hash":       fmt.Sprintf("%x", chosenHead[:8]),
		"block_value":       blockValueGwei,
		"has_blobs":         payloadResult.BlobsBundle != nil,
		"has_exec_requests": len(payloadResult.ExecutionRequests) > 0,
		"txs_in_payload":    txCount,
	}).Info("Payload built from attributes")

	return event, nil
}

// AbortBuild aborts any active builds for the given slot (across all variants).
func (b *PayloadBuilder) AbortBuild(slot phase0.Slot) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for k, ab := range b.activeBuilds {
		if k.slot != slot {
			continue
		}
		ab.cancelFn()
		delete(b.activeBuilds, k)

		b.log.WithFields(logrus.Fields{
			"slot":    slot,
			"variant": k.variant.String(),
		}).Debug("Build aborted")
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
			SlotNumber:            uint64(buildCtx.Slot),
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
