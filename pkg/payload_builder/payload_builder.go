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
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/inclusionlist"
	"github.com/ethpandaops/buildoor/pkg/jqtransform"
	"github.com/ethpandaops/buildoor/pkg/metrics"
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
	// feeRecipient is the builder's own fee recipient (the wallet address,
	// zero without a wallet).
	feeRecipient common.Address

	settingsResolvers []ProposerSettingsResolver // asked in order for proposer settings; first match wins
	cfg               *config.Config             // shared config; mutable settings are read live, never cached
	log               logrus.FieldLogger

	// Active build tracking: multiple candidate builds may run for the same
	// slot (one per parent tuple); builds for older slots are cancelled when
	// a newer slot starts. The testing builds outlive the engine build (their
	// own window) and are tracked apart under the same rules.
	activeBuilds map[activeBuildKey]*activeBuild
	localLoops   map[activeBuildKey]*activeBuild
	mu           sync.Mutex

	// localTiming learns how long a testing build takes, to time the final
	// one against its window end.
	localTiming buildTimingModel

	// ilStore holds the gossiped inclusion lists (FOCIL, Heze); nil = the
	// payload attributes' list alone.
	ilStore *inclusionlist.Store
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
// feeRecipient is the builder's own fee recipient (the wallet address, zero
// without a wallet). settingsResolvers are asked in order for the proposer's
// announced fee recipient and gas limit; the first match wins. localClient and txPool may be nil (local build
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
		localLoops:        make(map[activeBuildKey]*activeBuild, 4),
		log:               log.WithField("component", "payload-builder"),
	}
}

// buildPrelude is everything both build paths share for one target: the fork,
// the resolved proposer settings, the engine attributes and forkchoice state.
type buildPrelude struct {
	beaconFork    version.DataVersion
	engineVersion enginev.DataVersion
	// feeRecipient is the payload's coinbase; proposerFeeRecipient the
	// proposer's resolved fee recipient (see payloadCoinbase).
	feeRecipient         common.Address
	proposerFeeRecipient common.Address
	targetGasLimit       uint64
	payloadAttrs         *engineall.PayloadAttributes
	fcState              *paris.ForkchoiceState
	// ilReq is the slot's inclusion list instruction; inclusionList the view
	// the engine build carries (nil before Heze).
	ilReq         *InclusionListRequest
	inclusionList *InclusionListBuild
}

// localOutcome is one testing run's result.
type localOutcome struct {
	payload    *Payload
	info       *LocalBuildInfo
	skipReason string
	err        error
}

// transformTimeout bounds how long an operator jq payload transform may run.
const transformTimeout = 2 * time.Second

// BuildRequest is one build target handed to the payload builder.
type BuildRequest struct {
	// Attrs are the attributes to build from. They may be an effective copy
	// with the parent fields redirected to another candidate parent (reorg /
	// payload-miss handling); the builder treats whatever parent it is given
	// as authoritative and stores it on the payloads, so a bid built from
	// one advertises the parent it was built on.
	Attrs *beacon.PayloadAttributesEvent
	// BuildTimeMs is the engine build's wait; 0 uses the live-configured
	// PayloadBuildTime.
	BuildTimeMs uint64
	// Seq and Candidate identify the target on every payload it builds.
	Seq       uint64
	Candidate chain.CandidateKey
	// Transform is the slot's jq payload transform ("" = none), applied to
	// every payload right after its build.
	Transform string
	// Local, when non-nil, runs the testing builds for the target; its
	// payload source decides which payload is selected.
	Local *LocalBuildRequest
	// InclusionList is the slot's inclusion list instruction.
	InclusionList *InclusionListRequest

	// reporter receives the engine build and testing run events as they
	// happen.
	reporter buildReporter
}

// BuildPayloadFromAttributes runs one build target: the engine build
// (forkchoiceUpdated, wait, getPayload) and, when requested, the testing
// builds in their own window, which outlive this call. Every build reports
// its own payload through the request's reporter the moment it ends, and the
// target's pick is re-evaluated right after (targetPicker): a pick change
// or, once every build ended without one, no payload. The returned error is
// a failure before any build ran (prelude, forkchoiceUpdated).
func (b *PayloadBuilder) BuildPayloadFromAttributes(ctx context.Context, req *BuildRequest) error {
	attrs := req.Attrs
	local := req.Local

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

	for key, loop := range b.localLoops {
		if key.slot != attrs.ProposalSlot || key == buildKey {
			loop.cancelFn()
			delete(b.localLoops, key)
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

	prelude, err := b.prepareBuild(buildCtx, attrs, req.InclusionList)
	if err != nil {
		return err
	}

	runEL := local.RunEL()
	picker := newTargetPicker(req)

	// The forkchoice update pins the EL head to the build parent. With the
	// engine build it also starts the EL's own payload construction; without
	// it (testing only) the head pin alone is what head-pinned ELs need for
	// the testing call.
	var payloadAttrs *engineall.PayloadAttributes
	if runEL {
		payloadAttrs = prelude.payloadAttrs
	}

	fcuStart := time.Now()

	if runEL {
		b.reportEngine(req, &EngineBuildEvent{Status: EngineStatusStarted, StartedAt: fcuStart})
	}

	payloadID, err := b.forkchoiceUpdated(buildCtx, attrs, prelude, payloadAttrs)
	if err != nil {
		if runEL {
			b.reportEngine(req, &EngineBuildEvent{Status: EngineStatusFailed, StartedAt: fcuStart, Error: err.Error()})
		}

		return err
	}

	calls := &EngineCallTimes{ForkchoiceStart: fcuStart, ForkchoiceEnd: time.Now()}

	if runEL {
		b.mu.Lock()
		build.payloadID = payloadID
		b.mu.Unlock()
	}

	// The engine build's getPayload: payload_build_time after the forkchoice
	// update.
	deadline := time.Now().Add(b.payloadBuildTime(req.BuildTimeMs))

	if local != nil {
		localDone := make(chan struct{})
		localCtx := b.startLocalLoop(ctx, buildKey, attrs, local, localDone)

		windowStart, windowEnd := b.localWindow(attrs, local)

		go func() {
			defer close(localDone)
			defer picker.testingEnded()

			b.runLocalBuilds(localCtx, req, prelude, windowStart, windowEnd, picker)
		}()
	}

	if runEL {
		payload, err := b.buildEngine(buildCtx, req, prelude, payloadID, deadline, calls)

		event := &EngineBuildEvent{Status: EngineStatusReady, StartedAt: fcuStart, Payload: payload}
		if payload != nil {
			event.Calls = payload.EngineCalls
		} else {
			event.Status = EngineStatusFailed
			event.Error = err.Error()
		}

		b.reportEngine(req, event)
		picker.engineEnded(payload, err)
	}

	return nil
}

// reportEngine stamps an engine build event with the target and hands it to
// the request's reporter.
func (b *PayloadBuilder) reportEngine(req *BuildRequest, event *EngineBuildEvent) {
	if req.reporter == nil {
		return
	}

	event.Slot, event.Candidate, event.Seq, event.At = req.Attrs.ProposalSlot, req.Candidate, req.Seq, time.Now()
	req.reporter.reportEngine(event)
}

// reportTesting stamps a testing run event with the target and hands it to
// the request's reporter.
func (b *PayloadBuilder) reportTesting(req *BuildRequest, event *TestingBuildEvent) {
	if req.reporter == nil {
		return
	}

	event.Slot, event.Candidate, event.Seq, event.At = req.Attrs.ProposalSlot, req.Candidate, req.Seq, time.Now()
	event.TxSource = req.Local.TxSource
	req.reporter.reportTesting(event)
}

// completePayload stamps a freshly built payload with its build and target
// and applies the slot's payload transform: the payload is final before its
// build event reports it, and never modified afterwards.
func (b *PayloadBuilder) completePayload(ctx context.Context, req *BuildRequest, payload *Payload, ref BuildRef) error {
	payload.Build = ref
	payload.BuildSeq = req.Seq
	payload.Candidate = req.Candidate

	return b.applyPayloadTransform(ctx, req.Transform, payload)
}

// applyPayloadTransform rewrites the built execution payload with the slot's
// jq payload transform, in place. Because the bid commits to the payload's
// block hash, Payload.BlockHash is re-synced to the (possibly modified)
// payload block hash so the bid and the revealed payload stay consistent.
// A no-op without a transform.
func (b *PayloadBuilder) applyPayloadTransform(ctx context.Context, expr string, p *Payload) error {
	if expr == "" {
		return nil
	}

	tctx, cancel := context.WithTimeout(ctx, transformTimeout)
	defer cancel()

	transformed := &eth2all.ExecutionPayload{Version: p.ExecutionPayload.Version}
	if err := jqtransform.ApplyTyped(tctx, expr, p.ExecutionPayload, transformed); err != nil {
		return fmt.Errorf("payload transform failed: %w", err)
	}

	if transformed.BlockHash == p.ExecutionPayload.BlockHash {
		newHash, err := RehashBeaconPayload(transformed, p.EngineVersion,
			p.EngineExecutionRequests, common.Hash(p.Attributes.ParentBeaconBlockRoot))
		if err != nil {
			return fmt.Errorf("failed to recompute block hash after transform: %w", err)
		}

		transformed.BlockHash = phase0.Hash32(newHash)
	}

	p.ExecutionPayload = transformed
	p.BlockHash = transformed.BlockHash

	b.log.WithFields(logrus.Fields{
		"slot":       p.Attributes.ProposalSlot,
		"build":      p.Build.Kind,
		"run":        p.Build.Run,
		"block_hash": fmt.Sprintf("%x", p.BlockHash[:8]),
	}).Warn("Applied payload jq transform")

	return nil
}

// localWindow returns the testing build window of the target in wall time.
func (b *PayloadBuilder) localWindow(attrs *beacon.PayloadAttributesEvent, req *LocalBuildRequest) (time.Time, time.Time) {
	slotStart := b.chainSvc.SlotToTime(attrs.ProposalSlot)

	return slotStart.Add(time.Duration(req.StartMs) * time.Millisecond),
		slotStart.Add(time.Duration(req.EndMs) * time.Millisecond)
}

// startLocalLoop gives the target's testing builds their own lifetime: until
// the window ends (plus a build that overruns it), unless a newer build of
// the tuple, a newer slot or AbortBuild cancels them.
func (b *PayloadBuilder) startLocalLoop(
	ctx context.Context,
	key activeBuildKey,
	attrs *beacon.PayloadAttributesEvent,
	req *LocalBuildRequest,
	done <-chan struct{},
) context.Context {
	_, windowEnd := b.localWindow(attrs, req)

	loopCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), windowEnd.Add(lateBuildTimeout))
	loop := &activeBuild{cancelFn: cancel}

	b.mu.Lock()
	b.localLoops[key] = loop
	b.mu.Unlock()

	go func() {
		select {
		case <-done:
		case <-loopCtx.Done():
		}

		cancel()

		b.mu.Lock()
		if b.localLoops[key] == loop {
			delete(b.localLoops, key)
		}
		b.mu.Unlock()
	}()

	return loopCtx
}

// payloadBuildTime is how long the engine build collects transactions before
// its getPayload: the live setting, or an explicit per-build time
// (speculative candidates).
func (b *PayloadBuilder) payloadBuildTime(buildTimeMs uint64) time.Duration {
	ms := b.cfg.PayloadBuildTime
	if buildTimeMs != 0 {
		ms = buildTimeMs
	}

	return time.Duration(ms) * time.Millisecond
}

// selectPayload applies the request's payload source to the target's builds
// as they stand and picks one of the build payloads itself, or returns why
// there is none to pick.
func selectPayload(result *buildState, local *LocalBuildRequest) (*selection, error) {
	source := config.PayloadSourceEL
	if local != nil {
		source = config.NormalizedPayloadSource(local.PayloadSource, config.PayloadSourceEL)
	}

	useEL := func(fallback bool) (*selection, error) {
		if result.EL == nil {
			if result.ELErr != nil {
				return nil, result.ELErr
			}

			return nil, errors.New("no engine payload built")
		}

		return &selection{Payload: result.EL, Fallback: fallback}, nil
	}

	useLocal := func() (*selection, error) {
		return &selection{Payload: result.Local}, nil
	}

	localErr := func() error {
		if result.LocalErr != nil {
			return fmt.Errorf("testing build failed: %w", result.LocalErr)
		}

		if result.LocalSkipReason != "" {
			return fmt.Errorf("testing build skipped: %s", result.LocalSkipReason)
		}

		return errors.New("no testing payload built")
	}

	switch source {
	case config.PayloadSourceLocal:
		if result.Local != nil {
			return useLocal()
		}

		return nil, localErr()

	case config.PayloadSourceHigherValue:
		switch {
		case result.Local == nil:
			return useEL(false)
		case result.EL == nil:
			return useLocal()
		case blockValue(result.Local).Cmp(blockValue(result.EL)) > 0:
			return useLocal()
		default:
			return useEL(false)
		}

	case config.PayloadSourceLocalOrEL:
		if result.Local != nil {
			return useLocal()
		}

		return useEL(true)

	default:
		return useEL(false)
	}
}

// blockValue returns the payload's EL-reported block value (0 when unknown).
func blockValue(payload *Payload) *big.Int {
	if payload.BlockValue == nil {
		return new(big.Int)
	}

	return payload.BlockValue
}

// prepareBuild resolves everything both build paths share for one target.
func (b *PayloadBuilder) prepareBuild(
	ctx context.Context, attrs *beacon.PayloadAttributesEvent, il *InclusionListRequest,
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

	proposerFeeRecipient, targetGasLimit := b.resolveProposerSettings(attrs, beaconFork)
	coinbase := b.payloadCoinbase(beaconFork, proposerFeeRecipient)

	// Build the fork-agnostic payload attributes and forkchoice state. The
	// engine client dispatches to the correct engine_forkchoiceUpdated version.
	payloadAttrs := &engineall.PayloadAttributes{
		Version:               engineVersion,
		Timestamp:             attrs.Timestamp,
		PrevRandao:            paris.Hash32(attrs.PrevRandao),
		SuggestedFeeRecipient: paris.Address(coinbase),
		Withdrawals:           convertWithdrawalsToEngineFormat(attrs.Withdrawals),
		ParentBeaconBlockRoot: paris.Hash32(attrs.ParentBeaconBlockRoot),
		SlotNumber:            uint64(attrs.ProposalSlot),
		TargetGasLimit:        targetGasLimit,
	}

	// From Heze the attributes always carry the inclusion list (V5 requires
	// the field, possibly empty): the builder's view, not just the beacon
	// node's at attributes time.
	inclusionList := b.resolveInclusionList(ctx, attrs, beaconFork, il)
	if txs := inclusionList.listTransactions(); txs != nil {
		payloadAttrs.InclusionListTransactions = make([]paris.Transaction, len(txs))
		for i, tx := range txs {
			payloadAttrs.InclusionListTransactions[i] = paris.Transaction(tx)
		}
	}

	return &buildPrelude{
		beaconFork:           beaconFork,
		engineVersion:        engineVersion,
		feeRecipient:         coinbase,
		proposerFeeRecipient: proposerFeeRecipient,
		targetGasLimit:       targetGasLimit,
		payloadAttrs:         payloadAttrs,
		ilReq:                il,
		inclusionList:        inclusionList,
		fcState: &paris.ForkchoiceState{
			HeadBlockHash:      paris.Hash32(attrs.ParentBlockHash),
			SafeBlockHash:      paris.Hash32(finalityInfo.SafeExecutionBlockHash),
			FinalizedBlockHash: paris.Hash32(finalityInfo.FinalizedExecutionBlockHash),
		},
	}, nil
}

// resolveProposerSettings resolves the proposer's fee recipient and target gas
// limit for a build. The registered resolvers are asked in order (each
// self-scoped to its fork / data source: gossip preferences post-Gloas,
// validator registrations pre-Gloas); the first match wins.
// Without a match the payload_attributes event fills in: its
// SuggestedFeeRecipient (any fork) and, post-Gloas, its TargetGasLimit, so
// bids match the proposer's expected fee recipient even when its preferences
// never reached us (a same-node p2p broadcast does not loop back).
// Final fallback: the builder's own fee recipient.
func (b *PayloadBuilder) resolveProposerSettings(
	attrs *beacon.PayloadAttributesEvent, beaconFork version.DataVersion,
) (common.Address, uint64) {
	for _, resolver := range b.settingsResolvers {
		settings, ok := resolver.ResolveProposerSettings(attrs.ProposalSlot, attrs.ProposerIndex)
		if !ok {
			continue
		}

		b.log.WithFields(logrus.Fields{
			"proposer_index":   attrs.ProposerIndex,
			"fee_recipient":    settings.FeeRecipient.Hex(),
			"target_gas_limit": settings.TargetGasLimit,
		}).Debug("Using proposer settings from resolver")

		targetGasLimit := settings.TargetGasLimit
		if targetGasLimit == 0 && beaconFork >= version.DataVersionGloas {
			targetGasLimit = attrs.TargetGasLimit
		}

		return settings.FeeRecipient, targetGasLimit
	}

	var targetGasLimit uint64
	if beaconFork >= version.DataVersionGloas {
		targetGasLimit = attrs.TargetGasLimit
	}

	if attrs.SuggestedFeeRecipient == (common.Address{}) {
		return b.feeRecipient, targetGasLimit
	}

	b.log.WithFields(logrus.Fields{
		"slot":           attrs.ProposalSlot,
		"proposer_index": attrs.ProposerIndex,
		"fee_recipient":  attrs.SuggestedFeeRecipient.Hex(),
	}).Debug("Using suggested fee recipient from payload_attributes")

	return attrs.SuggestedFeeRecipient, targetGasLimit
}

// payloadCoinbase returns the fee recipient the payload is built for. From
// Gloas on the proposer is paid the bid value through the CL, so the block
// value belongs to the builder: the payload pays its own wallet. Before Gloas
// the block value IS the proposer's payment, and without a wallet there is no
// own address to build for.
func (b *PayloadBuilder) payloadCoinbase(
	beaconFork version.DataVersion, proposerFeeRecipient common.Address,
) common.Address {
	if beaconFork >= version.DataVersionGloas && b.feeRecipient != (common.Address{}) {
		return b.feeRecipient
	}

	return proposerFeeRecipient
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

// buildEngine waits the build time and retrieves the EL's payload; calls
// carries the forkchoiceUpdated times and receives the getPayload ones.
func (b *PayloadBuilder) buildEngine(
	ctx context.Context,
	req *BuildRequest,
	prelude *buildPrelude,
	payloadID paris.PayloadID,
	deadline time.Time,
	calls *EngineCallTimes,
) (*Payload, error) {
	attrs := req.Attrs

	b.log.Infof("Allowing payload to build for: %dms", time.Until(deadline).Milliseconds())

	// Wait for the EL to accumulate transactions, but abort early (with an error)
	// if the build is cancelled by a newer slot or the context deadline is hit,
	// rather than sleeping into a doomed getPayload call.
	buildTimer := time.NewTimer(max(time.Until(deadline), 0))
	defer buildTimer.Stop()

	// A list arriving during the wait restarts the EL's build with it: the
	// payload ID changes, the deadline stays.
	var ilTicks <-chan time.Time

	ilVersion := uint64(0)

	if prelude.inclusionList != nil && b.ilStore != nil {
		ticker := time.NewTicker(inclusionListPollInterval)
		defer ticker.Stop()

		ilTicks = ticker.C
		ilVersion = prelude.inclusionList.StoreVersion
	}

wait:
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("build aborted while waiting for payload: %w", ctx.Err())
		case <-buildTimer.C:
			break wait
		case <-ilTicks:
			if version := b.ilStore.Version(); version != ilVersion {
				ilVersion = version

				if restarted, id := b.restartWithInclusionList(ctx, attrs, prelude); restarted != nil {
					prelude, payloadID = restarted, id
				}
			}
		}
	}

	// Retrieve the built payload as the fork-agnostic union.
	calls.GetPayloadStart = time.Now()

	resp, err := b.engineClient.GetPayloadAgnostic(ctx, prelude.engineVersion, payloadID)
	if err != nil {
		return nil, fmt.Errorf("failed to get payload: %w", err)
	}

	calls.GetPayloadEnd = time.Now()

	payload, err := b.finalizePayload(ctx, attrs, prelude, resp)
	if err != nil {
		return nil, err
	}

	payload.EngineCalls = calls

	if err := b.completePayload(ctx, req, payload, BuildRef{Kind: BuildKindEngine}); err != nil {
		return nil, err
	}

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

	started := time.Now()
	defer func() { metrics.LocalBuildSeconds.Observe(time.Since(started).Seconds()) }()

	info := &LocalBuildInfo{TxSource: req.TxSource, SubmittedTxs: -1}

	// Each testing build reads the inclusion list view afresh: lists that
	// arrived since the engine build started are carried too.
	inclusionList := b.resolveInclusionList(ctx, attrs, prelude.beaconFork, prelude.ilReq)
	info.InclusionList = inclusionList

	var (
		txs [][]byte
		// submitted is the pool selection's block order behind txs: the
		// inclusion list, the pool and the EL mempool fill (after the pool
		// for private_first, interleaved for most_profitable). fill marks
		// the EL mempool entries.
		submitted []*txpool.PooledTx
		fill      map[common.Hash]struct{}
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
			InclusionList:  inclusionList.listTransactions(),
			IncludeBlobTxs: req.IncludeBlobTxs,
			BlobEncoding:   req.BlobEncoding,
			CellProofs:     prelude.engineVersion >= enginev.DataVersionOsaka,
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

		// The EL builds from its own mempool and the testing call carries no
		// inclusion list: a slot with one gets a payload that ignores it.
		if il := inclusionList.listTransactions(); len(il) > 0 {
			info.InclusionListTxs = len(il)
			info.InclusionListDropped = true
		}

	default: // txpool, private_first, most_profitable
		if b.txPool == nil {
			return localOutcome{skipReason: LocalSkipTxPoolUnavailable}
		}

		if !b.txPool.Enabled() {
			return localOutcome{skipReason: LocalSkipTxPoolDisabled}
		}

		// The fill is best effort: without the EL mempool the block is built
		// from the pool alone and the reason recorded.
		var (
			pending      []*txpool.PooledTx
			fillSidecars txpool.SidecarSource
		)

		if config.TxSourceUsesELMempool(req.TxSource) {
			if source, ok := newEngineBlobSource(b.engineClient, prelude.engineVersion); ok {
				fillSidecars = source
			}

			mirrored, started, err := b.txPool.ELMempool(ctx)
			info.MirrorStarted = started

			if err != nil {
				info.FillError = err.Error()

				b.log.WithError(err).WithField("slot", attrs.ProposalSlot).
					Warn("Cannot read the EL mempool, building the custom block from the pool alone")
			}

			pending = mirrored
		}

		selection, err := b.txPool.Select(ctx, &txpool.SelectParams{
			ParentHash:     common.Hash(attrs.ParentBlockHash),
			GasLimit:       b.localGasLimit(ctx, attrs, prelude),
			MaxBlobs:       b.chainSvc.GetChainSpec().MaxBlobsPerBlockAt(b.chainSvc.GetEpochOfSlot(attrs.ProposalSlot)),
			MaxTxs:         req.MaxTxs,
			GasFillPct:     req.GasFillPct,
			BaseFeeCeiling: gweiToWei(req.BaseFeeCeilingGwei),
			Ordering:       req.Ordering,
			Seed:           uint64(attrs.ProposalSlot),
			InclusionList:  inclusionList.listTransactions(),
			IncludeBlobTxs: req.IncludeBlobTxs,
			BlobEncoding:   req.BlobEncoding,
			CellProofs:     prelude.engineVersion >= enginev.DataVersionOsaka,
			Fill:           pending,
			FillSidecars:   fillSidecars,
			MostProfitable: req.TxSource == config.TxSourceMostProfitable,
		})
		if err != nil {
			return localOutcome{info: info, err: fmt.Errorf("pool selection failed: %w", err)}
		}

		info.FillTxs = len(selection.Fill)
		fill = make(map[common.Hash]struct{}, len(selection.Fill))

		for _, tx := range selection.Fill {
			fill[tx.Hash] = struct{}{}
		}

		info.Selection = selection.Summary()
		info.InclusionListTxs = selection.InclusionListTxs
		txs = selection.Txs

		// A refusal drops the fill first; error attribution then works on
		// what is left: inclusion list and pool alike.
		submitted = selection.Ordered
	}

	// The pool selection places the inclusion list itself (checked against
	// the parent state); the exact sources carry it verbatim in front. The
	// EL-mempool source leaves the list to the EL.
	if req.TxSource == config.TxSourceExplicit || req.TxSource == config.TxSourceEmpty {
		if il := inclusionList.listTransactions(); len(il) > 0 {
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

	info.SelectedAt = time.Now()
	parentHash := common.Hash(attrs.ParentBlockHash)

	// The EL refuses the whole call on one bad transaction (geth, ethrex) or
	// fails its post-check (nethermind, besu). For the txpool source the
	// refusal is attributed to the offending sender or position, those
	// transactions are dropped from the attempt (striked in the pool) and
	// the call retried within MaxAttempts. Exact sources never retry: a
	// refusal fails them, since dropping would build a block that is not the
	// plan.
	maxAttempts := max(req.MaxAttempts, 1)
	if !config.TxSourceSelects(req.TxSource) {
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

		isFill := func(tx *txpool.PooledTx) bool {
			_, ok := fill[tx.Hash]
			return ok
		}

		if !info.FillDropped && !info.FillBlobsDropped && hasFillBlobTxs(submitted, fill) {
			// The fill's blob transactions carry sidecars assembled from the
			// EL blob pool: the likeliest part to be refused, so they go
			// first, together with their senders' later transactions.
			kept := withoutSenderTails(submitted, func(tx *txpool.PooledTx) bool {
				return isFill(tx) && tx.Tx.Type() == types.BlobTxType
			})

			b.log.WithError(err).WithFields(logrus.Fields{
				"slot":    attrs.ProposalSlot,
				"dropped": len(submitted) - len(kept),
			}).Warn("EL refused the transaction list, retrying without the EL mempool blob transactions")

			if txs, err = resubmit(info, kept, req.BlobEncoding, fill); err != nil {
				return localOutcome{info: info, err: err}
			}

			submitted = kept
			info.FillBlobsDropped = true

			continue
		}

		if info.FillTxs > 0 && !info.FillDropped {
			// A refusal is retried without the EL mempool fill (and the
			// senders' transactions after it) before anything of the pool's
			// is dropped.
			b.log.WithError(err).WithField("slot", attrs.ProposalSlot).
				Warn("EL refused the transaction list, retrying without the EL mempool fill")

			kept := withoutSenderTails(submitted, isFill)

			if txs, err = resubmit(info, kept, req.BlobEncoding, fill); err != nil {
				return localOutcome{info: info, err: err}
			}

			submitted = kept
			info.FillDropped = true

			continue
		}

		exactSource := req.TxSource == config.TxSourceExplicit || req.TxSource == config.TxSourceEmpty
		if exactSource && info.InclusionListTxs > 0 && !info.InclusionListDropped && txs != nil {
			// The exact sources carry the list verbatim, unchecked against
			// the parent state: a list transaction that cannot execute takes
			// the whole call down on most ELs. Retry with the plan alone (a
			// payload that may miss the list, flagged); the pool sources
			// place only executable list transactions and attribute instead.
			b.log.WithError(err).WithField("slot", attrs.ProposalSlot).
				Warn("Local build failed with the inclusion list, retrying without it")

			txs = txs[info.InclusionListTxs:]
			info.InclusionListDropped = true
			info.SubmittedTxs = len(txs)
			info.ExpectedHashes = txHashes(txs)

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

	payload.Build = BuildRef{Kind: BuildKindTesting}
	payload.Local = info
	payload.InclusionList = inclusionList
	info.BuiltAt = payload.ReadyAt

	// geth checks the parent against its head and then builds on whatever
	// the head is by then: a head that moved in between yields a block on
	// another parent, which no bid for this target may carry.
	if err := verifyBuiltParent(payload, attrs); err != nil {
		return localOutcome{info: info, err: err}
	}

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

// hasFillBlobTxs reports whether the list carries an EL mempool blob
// transaction.
func hasFillBlobTxs(txs []*txpool.PooledTx, fill map[common.Hash]struct{}) bool {
	for _, tx := range txs {
		if _, ok := fill[tx.Hash]; ok && tx.Tx.Type() == types.BlobTxType {
			return true
		}
	}

	return false
}

// withoutSenderTails drops the transactions drop picks and every later
// transaction of their senders, which would follow a nonce gap.
func withoutSenderTails(txs []*txpool.PooledTx, drop func(*txpool.PooledTx) bool) []*txpool.PooledTx {
	kept := make([]*txpool.PooledTx, 0, len(txs))
	dropped := make(map[common.Address]bool, 4)

	for _, tx := range txs {
		if dropped[tx.Sender] || drop(tx) {
			dropped[tx.Sender] = true

			continue
		}

		kept = append(kept, tx)
	}

	return kept
}

// resubmit encodes the list a retry submits and updates the build info: the
// submitted count, the expected hashes and the EL mempool share.
func resubmit(
	info *LocalBuildInfo,
	txs []*txpool.PooledTx,
	blobEncoding string,
	fill map[common.Hash]struct{},
) ([][]byte, error) {
	encoded, err := encodeTxs(txs, blobEncoding)
	if err != nil {
		return nil, err
	}

	info.FillTxs = 0

	for _, tx := range txs {
		if _, ok := fill[tx.Hash]; ok {
			info.FillTxs++
		}
	}

	info.SubmittedTxs = len(encoded)
	info.ExpectedHashes = txHashes(encoded)

	return encoded, nil
}

// encodeTxs encodes pool entries for the testing call.
func encodeTxs(txs []*txpool.PooledTx, blobEncoding string) ([][]byte, error) {
	out := make([][]byte, 0, len(txs))

	for _, tx := range txs {
		raw, err := txpool.EncodeTx(tx.Tx, blobEncoding)
		if err != nil {
			return nil, fmt.Errorf("encode %s: %w", tx.Hash.Hex(), err)
		}

		out = append(out, raw)
	}

	return out, nil
}

// gweiToWei converts a gwei amount to wei (nil for 0).
func gweiToWei(gwei uint64) *big.Int {
	if gwei == 0 {
		return nil
	}

	return new(big.Int).Mul(new(big.Int).SetUint64(gwei), big.NewInt(1_000_000_000))
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

// verifyBuiltParent checks that the payload extends the requested parent.
func verifyBuiltParent(payload *Payload, attrs *beacon.PayloadAttributesEvent) error {
	if payload.ExecutionPayload == nil {
		return errors.New("local payload carries no execution payload")
	}

	if payload.ExecutionPayload.ParentHash != attrs.ParentBlockHash {
		return fmt.Errorf("local payload built on parent %#x, requested %#x",
			payload.ExecutionPayload.ParentHash, attrs.ParentBlockHash)
	}

	if attrs.ParentBlockNumber > 0 && payload.ExecutionPayload.BlockNumber != attrs.ParentBlockNumber+1 {
		return fmt.Errorf("local payload has block number %d, the parent is %d",
			payload.ExecutionPayload.BlockNumber, attrs.ParentBlockNumber)
	}

	return nil
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

		EngineExecutionRequests: resp.ExecutionRequests,
		EngineVersion:           enginePayload.Version,
		InclusionList:           prelude.inclusionList,
		FeeRecipient:            prelude.feeRecipient,
		ProposerFeeRecipient:    prelude.proposerFeeRecipient,
		BlockValue:              blockValue,
		ReadyAt:                 time.Now(),
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
		"build":             payload.Build.Kind,
		"run":               payload.Build.Run,
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

	for key, loop := range b.localLoops {
		if key.slot == slot {
			loop.cancelFn()
			delete(b.localLoops, key)
		}
	}
}
