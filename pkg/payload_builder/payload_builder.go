package payload_builder

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth-engine-client/spec/paris"
	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/txpool"
)

// PayloadBuilder handles execution payload building via the Engine API and,
// when requested per build, the additional local build via the EL's
// testing_buildBlockV1.
type PayloadBuilder struct {
	clClient     *beacon.Client
	engineClient EngineClient
	localClient  LocalBuildClient // nil = local build unconfigured
	txPool       *txpool.Pool     // nil = txpool source unavailable
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

// activeBuildKey identifies one in-progress build by the full parent tuple:
// candidates can share an execution parent while differing in the beacon
// parent (parent_empty and grandparent_full both extend the grandparent's
// payload), so the beacon root must be part of the key or they collide and
// cancel each other.
type activeBuildKey struct {
	slot       phase0.Slot
	parentRoot phase0.Root
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
// limit; the first match wins. localClient and txPool may be nil (local build
// unconfigured / no pool).
func NewPayloadBuilder(
	clClient *beacon.Client,
	engineClient EngineClient,
	localClient LocalBuildClient,
	txPool *txpool.Pool,
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
		localClient:       localClient,
		txPool:            txPool,
		feeRecipient:      feeRecipient,
		settingsResolvers: settingsResolvers,
		cfg:               cfg,
		activeBuilds:      make(map[activeBuildKey]*activeBuild, 4),
		log:               log.WithField("component", "payload-builder"),
	}
}

// buildPrelude is everything both build paths share for one target: the fork,
// the resolved proposer settings, the engine attributes and forkchoice state.
type buildPrelude struct {
	beaconFork     version.DataVersion
	engineVersion  enginev.DataVersion
	feeRecipient   common.Address
	targetGasLimit uint64
	payloadAttrs   *engineall.PayloadAttributes
	fcState        *paris.ForkchoiceState
}

// localOutcome is the local build goroutine's result.
type localOutcome struct {
	payload    *Payload
	info       *LocalBuildInfo
	skipReason string
	err        error
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
//
// local, when non-nil, additionally runs the local build (testing_buildBlockV1)
// for the same target; it starts once the engine forkchoiceUpdated pinned the
// EL head to the parent and runs during the engine build's wait. The request's
// payload source decides which payload the result exposes to consumers. An
// error is returned when the target ends without a consumable payload; the
// result still carries both builds' outcomes for inspection.
func (b *PayloadBuilder) BuildPayloadFromAttributes(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
	buildTimeMs uint64,
	local *LocalBuildRequest,
) (*BuildResult, error) {
	buildKey := activeBuildKey{
		slot:       attrs.ProposalSlot,
		parentRoot: attrs.ParentBlockRoot,
		parentHash: attrs.ParentBlockHash,
	}

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

	build := &activeBuild{cancelFn: cancel}
	b.activeBuilds[buildKey] = build
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		// Only retire our own build: a newer build of the same tuple has
		// already replaced (and cancelled) this one.
		if current, ok := b.activeBuilds[buildKey]; ok && current == build {
			delete(b.activeBuilds, buildKey)
		}
		b.mu.Unlock()

		cancel()
	}()

	prelude, err := b.prepareBuild(buildCtx, attrs)
	if err != nil {
		return nil, err
	}

	result := &BuildResult{}
	runEL := local.RunEL()

	// The forkchoice update pins the EL head to the build parent. With the
	// engine build it also starts the EL's own payload construction; without
	// it (local-only) the head pin alone is what head-pinned ELs need for the
	// testing call.
	var payloadAttrs *engineall.PayloadAttributes
	if runEL {
		payloadAttrs = prelude.payloadAttrs
	}

	payloadID, err := b.forkchoiceUpdated(buildCtx, attrs, prelude, payloadAttrs)
	if err != nil {
		return nil, err
	}

	if runEL {
		b.mu.Lock()
		build.payloadID = payloadID
		b.mu.Unlock()
	}

	var localCh chan localOutcome

	if local != nil {
		localCh = make(chan localOutcome, 1)

		go func() {
			localCh <- b.buildLocal(buildCtx, attrs, prelude, local)
		}()
	}

	if runEL {
		result.EL, result.ELErr = b.buildEngine(buildCtx, attrs, prelude, payloadID, buildTimeMs)
	}

	if localCh != nil {
		select {
		case <-buildCtx.Done():
			result.LocalErr = fmt.Errorf("local build aborted: %w", buildCtx.Err())
		case outcome := <-localCh:
			result.Local = outcome.payload
			result.LocalInfo = outcome.info
			result.LocalSkipReason = outcome.skipReason
			result.LocalErr = outcome.err
		}
	}

	result, err = b.selectPayload(result, local)

	// ReadyAt marks the hand-over to the consumers. A selected local payload
	// was built during the engine build's wait, so its build time would
	// otherwise show up as ready long before bids could use it; the local
	// build's own completion stays on Local.BuiltAt.
	if err == nil && result.Source == SourceLocal && result.Payload != nil {
		result.Payload.ReadyAt = time.Now()
	}

	return result, err
}

// selectPayload picks the payload feeding the consumers per the request's
// payload source and returns the error when the target ends without one.
func (b *PayloadBuilder) selectPayload(result *BuildResult, local *LocalBuildRequest) (*BuildResult, error) {
	source := config.PayloadSourceEL
	if local != nil {
		source = config.NormalizedPayloadSource(local.PayloadSource, config.PayloadSourceEL)
	}

	useEL := func() (*BuildResult, error) {
		if result.EL == nil {
			if result.ELErr != nil {
				return result, result.ELErr
			}

			return result, errors.New("no engine payload built")
		}

		result.Payload = result.EL
		result.Source = SourceEL

		return result, nil
	}

	useLocal := func() (*BuildResult, error) {
		result.Payload = result.Local
		result.Source = SourceLocal

		return result, nil
	}

	localErr := func() error {
		if result.LocalErr != nil {
			return fmt.Errorf("local build failed: %w", result.LocalErr)
		}

		if result.LocalSkipReason != "" {
			return fmt.Errorf("local build skipped: %s", result.LocalSkipReason)
		}

		return errors.New("no local payload built")
	}

	switch source {
	case config.PayloadSourceLocal:
		if result.Local != nil {
			return useLocal()
		}

		return result, localErr()

	case config.PayloadSourceLocalOrEL:
		if result.Local != nil {
			return useLocal()
		}

		result.Fallback = true

		return useEL()

	default:
		return useEL()
	}
}

// prepareBuild resolves everything both build paths share for one target.
func (b *PayloadBuilder) prepareBuild(
	ctx context.Context, attrs *beacon.PayloadAttributesEvent,
) (*buildPrelude, error) {
	// Resolve the fork active at the build epoch and the engine method version it implies.
	buildEpoch := b.chainSvc.GetEpochOfSlot(attrs.ProposalSlot)
	beaconFork := b.chainSvc.ActiveForkAtEpoch(buildEpoch)

	engineVersion, err := chain.EngineVersion(beaconFork)
	if err != nil {
		return nil, fmt.Errorf("cannot build payload for fork %s: %w", beaconFork, err)
	}

	// Safe/finalized hashes for the forkchoice state. The head tracker keeps
	// them fresh per head change, so concurrent candidate builds share one
	// snapshot instead of each paying the beacon-API round trips; the direct
	// fetch only covers the window before the first refresh.
	var finalityInfo *beacon.FinalityInfo
	if headTracker := b.chainSvc.GetHeadTracker(); headTracker != nil {
		finalityInfo = headTracker.FinalityInfo()
	}

	if finalityInfo == nil {
		fetched, err := b.clClient.GetFinalityInfo(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get finality info: %w", err)
		}

		finalityInfo = fetched
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

	// Build the fork-agnostic payload attributes and forkchoice state. The
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

	return &buildPrelude{
		beaconFork:     beaconFork,
		engineVersion:  engineVersion,
		feeRecipient:   proposerFeeRecipient,
		targetGasLimit: targetGasLimit,
		payloadAttrs:   payloadAttrs,
		fcState: &paris.ForkchoiceState{
			HeadBlockHash:      paris.Hash32(attrs.ParentBlockHash),
			SafeBlockHash:      paris.Hash32(finalityInfo.SafeExecutionBlockHash),
			FinalizedBlockHash: paris.Hash32(finalityInfo.FinalizedExecutionBlockHash),
		},
	}, nil
}

// forkchoiceUpdated pins the EL head to the build parent; with attributes it
// also starts the EL's payload construction and returns the payload id.
func (b *PayloadBuilder) forkchoiceUpdated(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
	prelude *buildPrelude,
	payloadAttrs *engineall.PayloadAttributes,
) (paris.PayloadID, error) {
	fcuReq := &engineall.ForkchoiceUpdatedRequest{
		Version:           prelude.engineVersion,
		ForkchoiceState:   prelude.fcState,
		PayloadAttributes: payloadAttrs,
	}

	b.log.WithFields(logrus.Fields{
		"slot":             attrs.ProposalSlot,
		"timestamp":        attrs.Timestamp,
		"withdrawal_count": len(prelude.payloadAttrs.Withdrawals),
		"parent_hash":      fmt.Sprintf("%x", attrs.ParentBlockHash[:8]),
		"engine_version":   prelude.engineVersion,
		"target_gas_limit": prelude.targetGasLimit,
		"engine_build":     payloadAttrs != nil,
	}).Debug("Updating forkchoice for build")

	fcuResp, err := b.engineClient.ForkchoiceUpdatedAgnostic(ctx, fcuReq)
	if err != nil {
		return paris.PayloadID{}, fmt.Errorf("forkchoiceUpdated failed: %w", err)
	}

	status := fcuResp.PayloadStatus.Status
	if status != paris.PayloadValidationStatusValid && status != paris.PayloadValidationStatusSyncing {
		return paris.PayloadID{}, fmt.Errorf("forkchoice status: %s", status)
	}

	if payloadAttrs == nil {
		return paris.PayloadID{}, nil
	}

	if fcuResp.PayloadID == nil {
		return paris.PayloadID{}, fmt.Errorf("no payload ID returned")
	}

	b.log.WithFields(logrus.Fields{
		"slot":       attrs.ProposalSlot,
		"payload_id": fmt.Sprintf("%x", fcuResp.PayloadID[:]),
	}).Debug("Payload build requested from attributes")

	return *fcuResp.PayloadID, nil
}

// buildEngine waits the build time and retrieves the EL's payload.
func (b *PayloadBuilder) buildEngine(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
	prelude *buildPrelude,
	payloadID paris.PayloadID,
	buildTimeMs uint64,
) (*Payload, error) {
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
	case <-ctx.Done():
		return nil, fmt.Errorf("build aborted while waiting for payload: %w", ctx.Err())
	case <-buildTimer.C:
	}

	// Retrieve the built payload as the fork-agnostic union.
	resp, err := b.engineClient.GetPayloadAgnostic(ctx, prelude.engineVersion, payloadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get payload: %w", err)
	}

	payload, err := b.finalizePayload(ctx, attrs, prelude, resp)
	if err != nil {
		return nil, err
	}

	payload.Source = SourceEL

	b.logPayloadBuilt(attrs, prelude, payload, resp)

	return payload, nil
}

// buildLocal assembles the transaction list per the request's tx source and
// builds the payload through testing_buildBlockV1.
func (b *PayloadBuilder) buildLocal(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
	prelude *buildPrelude,
	req *LocalBuildRequest,
) localOutcome {
	if b.localClient == nil {
		return localOutcome{skipReason: LocalSkipUnavailable}
	}

	info := &LocalBuildInfo{TxSource: req.TxSource, SubmittedTxs: -1}

	var (
		txs [][]byte
		// poolSelected are the pool entries behind txs (txpool source only);
		// submitted is the list error attribution works on — the pool entries
		// once txs holds nothing but them (no inclusion-list prefix).
		poolSelected, submitted []*txpool.PooledTx
	)

	switch req.TxSource {
	case config.TxSourceExplicit:
		txs = req.Transactions
		if txs == nil {
			txs = [][]byte{}
		}

		info.ExplicitTxs = len(txs)

	case config.TxSourceQueued:
		if b.txPool == nil {
			return localOutcome{skipReason: LocalSkipTxPoolUnavailable}
		}

		selection, err := b.txPool.SelectQueued(ctx, req.Queued, &txpool.SelectParams{
			ParentHash:     common.Hash(attrs.ParentBlockHash),
			GasLimit:       b.localGasLimit(ctx, attrs, prelude),
			MaxBlobs:       b.chainSvc.GetChainSpec().MaxBlobsPerBlockAt(b.chainSvc.GetEpochOfSlot(attrs.ProposalSlot)),
			InclusionList:  attrs.InclusionListTransactions,
			IncludeBlobTxs: req.IncludeBlobTxs,
			BlobEncoding:   req.BlobEncoding,
		})
		if err != nil {
			return localOutcome{info: info, err: fmt.Errorf("queued list: %w", err)}
		}

		info.Selection = selection.Summary()
		info.InclusionListTxs = selection.InclusionListTxs
		info.ExplicitTxs = len(selection.Selected)
		txs = selection.Txs

	case config.TxSourceEmpty:
		txs = [][]byte{}

	case config.TxSourceELMempool:
		txs = nil

	default: // txpool
		if b.txPool == nil {
			return localOutcome{skipReason: LocalSkipTxPoolUnavailable}
		}

		if !b.txPool.Enabled() {
			return localOutcome{skipReason: LocalSkipTxPoolDisabled}
		}

		selection, err := b.txPool.Select(ctx, &txpool.SelectParams{
			ParentHash:     common.Hash(attrs.ParentBlockHash),
			GasLimit:       b.localGasLimit(ctx, attrs, prelude),
			MaxBlobs:       b.chainSvc.GetChainSpec().MaxBlobsPerBlockAt(b.chainSvc.GetEpochOfSlot(attrs.ProposalSlot)),
			MaxTxs:         req.MaxTxs,
			GasFillPct:     req.GasFillPct,
			Ordering:       req.Ordering,
			Seed:           uint64(attrs.ProposalSlot),
			InclusionList:  attrs.InclusionListTransactions,
			IncludeBlobTxs: req.IncludeBlobTxs,
			BlobEncoding:   req.BlobEncoding,
		})
		if err != nil {
			return localOutcome{info: info, err: fmt.Errorf("pool selection failed: %w", err)}
		}

		info.Selection = selection.Summary()
		info.InclusionListTxs = selection.InclusionListTxs
		txs = selection.Txs
		poolSelected = selection.Selected

		if selection.InclusionListTxs == 0 {
			submitted = poolSelected
		}
	}

	// Inclusion-list transactions (FOCIL) are mandatory for validity; the
	// pool selection already prepends them, the other explicit sources get
	// them here. The EL-mempool source leaves the list to the EL.
	if req.TxSource == config.TxSourceExplicit || req.TxSource == config.TxSourceEmpty {
		if il := attrs.InclusionListTransactions; len(il) > 0 {
			withIL := make([][]byte, 0, len(il)+len(txs))
			withIL = append(withIL, il...)
			withIL = append(withIL, txs...)
			txs = withIL
			info.InclusionListTxs = len(il)
		}
	}

	if txs != nil {
		info.SubmittedTxs = len(txs)
		info.ExpectedHashes = txHashes(txs)
	}

	parentHash := common.Hash(attrs.ParentBlockHash)

	// The EL refuses the whole call on one bad transaction (geth, ethrex) or
	// fails its post-check (nethermind, besu). For the txpool source the
	// refusal is attributed to the offending sender or position, those
	// transactions are dropped from the attempt (striked in the pool) and
	// the call retried within MaxAttempts. Exact sources never retry: a
	// refusal fails them, since dropping would build a block that is not the
	// plan.
	maxAttempts := max(req.MaxAttempts, 1)
	if req.TxSource != config.TxSourceTxPool {
		maxAttempts = 1
	}

	var (
		resp *engineall.GetPayloadResponse
		err  error
	)

	for attempt := 1; ; attempt++ {
		info.Attempts = attempt

		resp, err = b.localClient.BuildBlockV1(ctx, prelude.engineVersion, parentHash, prelude.payloadAttrs, txs, nil)
		if err == nil || ctx.Err() != nil {
			break
		}

		if info.InclusionListTxs > 0 && !info.InclusionListDropped && txs != nil {
			// A failing inclusion-list transaction takes the whole call down
			// on most ELs; retry without the list (a spec-violating payload,
			// flagged).
			b.log.WithError(err).WithField("slot", attrs.ProposalSlot).
				Warn("Local build failed with the inclusion list, retrying without it")

			txs = txs[info.InclusionListTxs:]
			info.InclusionListDropped = true
			info.SubmittedTxs = len(txs)
			info.ExpectedHashes = txHashes(txs)
			submitted = poolSelected

			continue
		}

		if attempt >= int(maxAttempts) || len(submitted) == 0 {
			break
		}

		attribution := txpool.Attribute(err, submitted)
		kept, dropped := attribution.Apply(submitted)

		if len(dropped) == 0 {
			break
		}

		for _, tx := range dropped {
			info.Dropped = append(info.Dropped, DroppedTx{Hash: tx.Hash.Hex(), Reason: attribution.Reason})
			b.txPool.Strike(tx.Hash, req.MaxStrikes)
		}

		b.log.WithError(err).WithFields(logrus.Fields{
			"slot":    attrs.ProposalSlot,
			"attempt": attempt,
			"reason":  attribution.Reason,
			"dropped": len(dropped),
			"kept":    len(kept),
		}).Warn("EL refused the transaction list, retrying without the attributed transactions")

		submitted = kept
		txs = make([][]byte, 0, len(kept))

		for _, tx := range kept {
			raw, encErr := txpool.EncodeTx(tx.Tx, req.BlobEncoding)
			if encErr != nil {
				return localOutcome{info: info, err: fmt.Errorf("encode %s: %w", tx.Hash.Hex(), encErr)}
			}

			txs = append(txs, raw)
		}

		info.SubmittedTxs = len(txs)
		info.ExpectedHashes = txHashes(txs)
	}

	if err != nil {
		return localOutcome{info: info, err: err}
	}

	payload, err := b.finalizePayload(ctx, attrs, prelude, resp)
	if err != nil {
		return localOutcome{info: info, err: fmt.Errorf("local payload: %w", err)}
	}

	payload.Source = SourceLocal
	payload.Local = info
	info.BuiltAt = payload.ReadyAt

	// The submitted list is a contract: the payload must hold exactly these
	// transactions in this order. ELs that silently filter (erigon) or
	// reorder would otherwise bid a block that is not the plan; the bid is
	// refused instead and the deviation recorded.
	if txs != nil {
		if err := verifyBuiltPayload(payload, info.ExpectedHashes); err != nil {
			info.DroppedByEL = max(0, len(txs)-len(payload.ExecutionPayload.Transactions))

			return localOutcome{info: info, err: err}
		}
	}

	b.logPayloadBuilt(attrs, prelude, payload, resp)

	return localOutcome{payload: payload, info: info}
}

// txHashes decodes network-encoded transactions into their hashes, in order.
// Undecodable entries map to the zero hash (the EL refuses them anyway).
func txHashes(txs [][]byte) []string {
	out := make([]string, len(txs))

	for i, raw := range txs {
		tx := new(types.Transaction)
		if err := tx.UnmarshalBinary(raw); err != nil {
			out[i] = (common.Hash{}).Hex()

			continue
		}

		out[i] = tx.Hash().Hex()
	}

	return out
}

// verifyBuiltPayload checks that the payload holds exactly the expected
// transactions, in order.
func verifyBuiltPayload(payload *Payload, expected []string) error {
	if payload.ExecutionPayload == nil {
		return errors.New("local payload deviates from the plan: no execution payload")
	}

	actual := payload.ExecutionPayload.Transactions
	if len(actual) != len(expected) {
		return fmt.Errorf("local payload deviates from the plan: %d transactions built, %d submitted",
			len(actual), len(expected))
	}

	for i, raw := range actual {
		tx := new(types.Transaction)
		if err := tx.UnmarshalBinary(raw); err != nil {
			return fmt.Errorf("local payload deviates from the plan: tx %d does not decode: %w", i, err)
		}

		if got := tx.Hash().Hex(); got != expected[i] {
			return fmt.Errorf("local payload deviates from the plan: tx %d is %s, submitted %s", i, got, expected[i])
		}
	}

	return nil
}

// localGasLimit predicts the gas limit the local payload will carry so the
// pool selection fills against it: the bid-gossip-required value when the
// parent's EL gas limit is known, else 0 (the parent's limit).
func (b *PayloadBuilder) localGasLimit(
	ctx context.Context, attrs *beacon.PayloadAttributesEvent, prelude *buildPrelude,
) uint64 {
	headTracker := b.chainSvc.GetHeadTracker()
	if headTracker == nil || prelude.targetGasLimit == 0 {
		return 0
	}

	_, parentGasLimit := headTracker.ResolveELParentMeta(ctx, attrs.ParentBlockRoot, attrs.ParentBlockHash)
	if parentGasLimit == 0 {
		return 0
	}

	return expectedBidGasLimit(parentGasLimit, prelude.targetGasLimit)
}

// finalizePayload turns an engine-shaped response into the built Payload:
// gas limit enforcement, extra-data marker (block hash recomputed), and the
// single conversions to beacon types.
func (b *PayloadBuilder) finalizePayload(
	ctx context.Context,
	attrs *beacon.PayloadAttributesEvent,
	prelude *buildPrelude,
	resp *engineall.GetPayloadResponse,
) (*Payload, error) {
	enginePayload := resp.ExecutionPayload
	if enginePayload == nil {
		return nil, fmt.Errorf("build response carries no execution payload")
	}

	gasLimitOverride := b.resolveGasLimitOverride(ctx, attrs, prelude.beaconFork,
		prelude.targetGasLimit, enginePayload.GasLimit, enginePayload.GasUsed)

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
	beaconPayload := beaconPayloadFromEngine(enginePayload, prelude.beaconFork)

	execRequests, err := ParseExecutionRequests(resp.ExecutionRequests, prelude.beaconFork)
	if err != nil {
		return nil, fmt.Errorf("failed to parse execution requests: %w", err)
	}

	blockValue := new(big.Int)
	if resp.BlockValue != nil {
		blockValue = resp.BlockValue.ToBig()
	}

	return &Payload{
		Attributes:        attrs,
		ExecutionPayload:  beaconPayload,
		BlobsBundle:       beaconBlobsBundleFromEngine(resp.BlobsBundle),
		ExecutionRequests: execRequests,
		BlockHash:         phase0.Hash32(newHash),
		FeeRecipient:      prelude.feeRecipient,
		BlockValue:        blockValue,
		ReadyAt:           time.Now(),
	}, nil
}

func (b *PayloadBuilder) logPayloadBuilt(
	attrs *beacon.PayloadAttributesEvent,
	prelude *buildPrelude,
	payload *Payload,
	resp *engineall.GetPayloadResponse,
) {
	b.log.WithFields(logrus.Fields{
		"slot":              attrs.ProposalSlot,
		"source":            payload.Source,
		"block_hash":        fmt.Sprintf("%x", payload.BlockHash[:8]),
		"parent_hash":       fmt.Sprintf("%x", attrs.ParentBlockHash[:8]),
		"block_value":       payload.BlockValue.String(),
		"has_blobs":         resp.BlobsBundle != nil,
		"has_exec_requests": len(resp.ExecutionRequests) > 0,
		"txs_in_payload":    len(payload.ExecutionPayload.Transactions),
		"target_gas_limit":  prelude.targetGasLimit,
		"payload_gas_limit": payload.ExecutionPayload.GasLimit,
	}).Info("Payload built from attributes")
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
