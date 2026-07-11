package payload_builder

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth-engine-client/spec/identification"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// buildCallTimeout is the margin added on top of the configured PayloadBuildTime
// for the engine getPayload and finality lookups that run after the build wait.
const buildCallTimeout = 10 * time.Second

// Service is the standalone builder service that handles payload building.
// It does NOT handle ePBS bidding or revealing - those are handled by the epbs package.
//
// Fork-aware building:
// - Electra/Fulu: Build on parent block (payload is in the block)
// - Gloas: Build on last known payload (payload is separate from block)
//
// Building is triggered by payload_attributes events from the beacon node,
// which contain all the information needed to build a payload.
type Service struct {
	cfg                    *config.Config
	clClient               *beacon.Client
	chainSvc               chain.Service
	planSvc                *action_plan.PlanService // per-slot scheduling authority
	engineClient           EngineClient
	feeRecipient           common.Address
	settingsResolvers      []ProposerSettingsResolver // ordered proposer-settings sources (register before Start)
	payloadBuilder         *PayloadBuilder
	payloadCache           *PayloadCache
	payloadReadyDispatcher *utils.Dispatcher[*Payload]
	buildStartedDispatcher *utils.Dispatcher[*PayloadBuildStartedEvent]
	buildFailedDispatcher  *utils.Dispatcher[*PayloadBuildFailedEvent]
	buildSkippedDispatcher *utils.Dispatcher[*BuildSkippedEvent]
	stats                  *BuilderStats
	statsMu                sync.RWMutex
	ctx                    context.Context
	cancel                 context.CancelFunc
	log                    logrus.FieldLogger
	wg                     sync.WaitGroup

	// Last known execution payload tracking (for Gloas)
	// In Gloas, blocks don't contain execution payloads - they come separately.
	// We need to track the last block that has a known payload.
	lastKnownPayloadMu        sync.RWMutex
	lastKnownPayloadBlockRoot phase0.Root // Beacon block root with known payload
	lastKnownPayloadSlot      phase0.Slot // Slot of the block with known payload

	// Build tracking
	scheduledBuildMu  sync.Mutex
	buildStartedSlots map[phase0.Slot]bool // Slots where building has started (to prevent re-building)
	skipFiredSlots    map[phase0.Slot]bool // Slots a BuildSkippedEvent was fired for (dedup per slot)

	// Payload inclusion tracking (deduplication between detection methods)
	wonPayloadsMu sync.Mutex
	wonPayloads   map[phase0.Hash32]phase0.Slot

	// lastBuiltSlot tracks the most recently built slot (WebUI status).
	lastBuiltSlot atomic.Uint64

	// EL client identification (engine_getClientVersionV1) — refreshed periodically.
	elClientVersionMu sync.RWMutex
	elClientVersion   *ELClientVersion
}

// ELClientVersion is the execution client's identification, as returned by
// engine_getClientVersionV1, in display-friendly string form.
type ELClientVersion struct {
	Code    string
	Name    string
	Version string
	Commit  string
}

// NewService creates a new builder service.
// Proposer settings (fee recipient, target gas limit) are resolved through the
// resolvers registered via AddProposerSettingsResolver before Start.
// planSvc is the per-slot scheduling authority: every build decision polls it
// for the slot's frozen plan (schedule, force/suppress, build timing).
func NewService(
	cfg *config.Config,
	clClient *beacon.Client,
	chainSvc chain.Service,
	planSvc *action_plan.PlanService,
	engineClient EngineClient,
	feeRecipient common.Address,
	log logrus.FieldLogger,
) (*Service, error) {
	serviceLog := log.WithField("component", "builder-service")

	s := &Service{
		cfg:                    cfg,
		clClient:               clClient,
		chainSvc:               chainSvc,
		planSvc:                planSvc,
		engineClient:           engineClient,
		feeRecipient:           feeRecipient,
		payloadCache:           NewPayloadCache(DefaultCacheSize),
		payloadReadyDispatcher: &utils.Dispatcher[*Payload]{},
		buildStartedDispatcher: &utils.Dispatcher[*PayloadBuildStartedEvent]{},
		buildFailedDispatcher:  &utils.Dispatcher[*PayloadBuildFailedEvent]{},
		buildSkippedDispatcher: &utils.Dispatcher[*BuildSkippedEvent]{},
		stats:                  &BuilderStats{},
		log:                    serviceLog,
		buildStartedSlots:      make(map[phase0.Slot]bool),
		skipFiredSlots:         make(map[phase0.Slot]bool, 16),
		wonPayloads:            make(map[phase0.Hash32]phase0.Slot, 16),
	}

	return s, nil
}

// Start initializes and starts the builder service.
func (s *Service) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	// Create payload builder
	s.payloadBuilder = NewPayloadBuilder(
		s.clClient,
		s.engineClient,
		s.chainSvc,
		s.feeRecipient,
		s.cfg,
		s.log,
		s.settingsResolvers,
	)

	// Start event stream
	if err := s.clClient.Events().Start(s.ctx); err != nil {
		return fmt.Errorf("failed to start event stream: %w", err)
	}

	// Start main loop
	s.wg.Add(1)

	go s.run()

	// Refresh EL client identification in the background.
	s.wg.Add(1)

	go s.refreshELClientVersionLoop()

	s.log.Info("Builder service started")

	return nil
}

// refreshELClientVersionLoop polls the EL for its client identification.
// Refreshes immediately on start and then every 5 minutes; tolerates errors
// silently since this is informational only.
func (s *Service) refreshELClientVersionLoop() {
	defer s.wg.Done()

	refresh := func() {
		ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
		defer cancel()

		versions, err := s.engineClient.ClientVersion(ctx, &identification.ClientVersion{
			Code:    []byte("BO"),
			Name:    []byte("buildoor"),
			Version: []byte("0"),
		})
		if err != nil || len(versions) == 0 {
			if err != nil {
				s.log.WithError(err).Debug("Failed to fetch EL client version")
			}
			return
		}

		v := &ELClientVersion{
			Code:    string(versions[0].Code),
			Name:    string(versions[0].Name),
			Version: string(versions[0].Version),
			Commit:  fmt.Sprintf("%x", versions[0].Commit),
		}

		s.elClientVersionMu.Lock()
		s.elClientVersion = v
		s.elClientVersionMu.Unlock()

		s.log.WithFields(logrus.Fields{
			"name":    v.Name,
			"version": v.Version,
			"code":    v.Code,
		}).Info("EL client identified")
	}

	refresh()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

// GetELClientVersion returns the cached EL client identification.
// Returns nil if the EL has not yet responded or does not support engine_getClientVersionV1.
func (s *Service) GetELClientVersion() *ELClientVersion {
	s.elClientVersionMu.RLock()
	defer s.elClientVersionMu.RUnlock()

	if s.elClientVersion == nil {
		return nil
	}

	v := *s.elClientVersion

	return &v
}

// Stop stops the builder service.
func (s *Service) Stop() {
	s.log.Info("Stopping builder service")

	if s.cancel != nil {
		s.cancel()
	}

	s.wg.Wait()

	s.log.Info("Builder service stopped")
}

// GetStats returns the current builder statistics.
func (s *Service) GetStats() BuilderStats {
	s.statsMu.RLock()
	defer s.statsMu.RUnlock()

	return *s.stats
}

// GetConfig returns the current configuration.
func (s *Service) GetConfig() *config.Config {
	return s.cfg
}

// UpdateConfig updates the service configuration at runtime. Schedule
// changes are handled by the plan service (the scheduling authority).
func (s *Service) UpdateConfig(cfg *config.Config) error {
	s.cfg = cfg

	s.log.Info("Configuration updated")

	return nil
}

// GetCurrentSlot returns the most recently built slot.
func (s *Service) GetCurrentSlot() phase0.Slot {
	return phase0.Slot(s.lastBuiltSlot.Load())
}

// GetChainSpec returns the chain specification.
func (s *Service) GetChainSpec() *chain.ChainSpec {
	return s.chainSvc.GetChainSpec()
}

// GetGenesis returns the genesis information.
func (s *Service) GetGenesis() *beacon.Genesis {
	return s.chainSvc.GetGenesis()
}

// GetCLClient returns the consensus layer client.
func (s *Service) GetCLClient() *beacon.Client {
	return s.clClient
}

// SubscribePayloadReady subscribes to payload ready events.
// Consumers (like the ePBS service) use this to receive built payloads.
func (s *Service) SubscribePayloadReady(capacity int) *utils.Subscription[*Payload] {
	return s.payloadReadyDispatcher.Subscribe(capacity, false)
}

// SubscribePayloadBuildStarted subscribes to payload build started events.
// Consumers (like the WebUI) use this to render builds as in-progress.
func (s *Service) SubscribePayloadBuildStarted(
	capacity int,
) *utils.Subscription[*PayloadBuildStartedEvent] {
	return s.buildStartedDispatcher.Subscribe(capacity, false)
}

// SubscribePayloadBuildFailed subscribes to payload build failed events.
// Consumers (like the WebUI) use this to mark in-progress builds as failed.
func (s *Service) SubscribePayloadBuildFailed(
	capacity int,
) *utils.Subscription[*PayloadBuildFailedEvent] {
	return s.buildFailedDispatcher.Subscribe(capacity, false)
}

// SubscribeBuildSkipped subscribes to build skipped events. Authoritative
// consumers (the slot results tracker) should pass blocking=true so no skip
// is lost; informational consumers should pass blocking=false.
func (s *Service) SubscribeBuildSkipped(
	capacity int,
	blocking bool,
) *utils.Subscription[*BuildSkippedEvent] {
	return s.buildSkippedDispatcher.Subscribe(capacity, blocking)
}

// GetPayloadCache returns the payload cache for direct access.
func (s *Service) GetPayloadCache() *PayloadCache {
	return s.payloadCache
}

// run is the main event loop.
func (s *Service) run() {
	defer s.wg.Done()

	headSub := s.clClient.Events().SubscribeHead()
	payloadAttrSub := s.clClient.Events().SubscribePayloadAttributes()
	payloadSub := s.clClient.Events().SubscribePayloadAvailable()

	defer headSub.Unsubscribe()
	defer payloadAttrSub.Unsubscribe()
	defer payloadSub.Unsubscribe()

	for {
		select {
		case <-s.ctx.Done():
			return

		case event := <-headSub.Channel():
			s.handleHeadEvent(event)

		case event := <-payloadAttrSub.Channel():
			s.handlePayloadAttributesEvent(event)

		case event := <-payloadSub.Channel():
			s.handlePayloadAvailableEvent(event)
		}
	}
}

// handleHeadEvent processes a head event (new block received).
// Checks if the block's execution payload matches one of our built payloads.
func (s *Service) handleHeadEvent(event *beacon.HeadEvent) {
	s.log.WithFields(logrus.Fields{
		"head_slot": event.Slot,
		"fork":      s.chainSvc.GetCurrentFork().String(),
	}).Debug("Head event received")

	go s.checkPayloadInclusion(event)
}

// checkPayloadInclusion fetches the block info and checks if its execution
// payload hash matches any of our built payloads.
func (s *Service) checkPayloadInclusion(event *beacon.HeadEvent) {
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()

	blockInfo, err := s.clClient.GetBlockInfo(ctx, fmt.Sprintf("0x%x", event.Block[:]))
	if err != nil {
		s.log.WithError(err).WithField("slot", event.Slot).Debug(
			"Failed to get block info for payload inclusion check",
		)

		return
	}

	payload := s.payloadCache.GetByBlockHash(blockInfo.ExecutionBlockHash)
	if payload == nil {
		return
	}

	s.markPayloadWon(blockInfo.ExecutionBlockHash, payload.Attributes.ProposalSlot)
}

// handlePayloadAttributesEvent processes a payload_attributes event.
// This is the primary trigger for building payloads.
// The event is cached by the EventStream; this method schedules the build
// at the configured BuildStartTime rather than building immediately.
func (s *Service) handlePayloadAttributesEvent(event *beacon.PayloadAttributesEvent) {
	s.log.WithFields(logrus.Fields{
		"proposal_slot": event.ProposalSlot,
		"parent_hash":   fmt.Sprintf("%x", event.ParentBlockHash[:8]),
		"timestamp":     event.Timestamp,
		"withdrawals":   len(event.Withdrawals),
	}).Info("Payload attributes event received")

	// Check if the parent block hash matches one of our built payloads
	// (this means our payload was included as the parent of this new block).
	if payload := s.payloadCache.GetByBlockHash(event.ParentBlockHash); payload != nil {
		s.markPayloadWon(event.ParentBlockHash, payload.Attributes.ProposalSlot)
	}

	// Resolve the slot's immutable action plan snapshot once. The plan
	// service is the scheduling authority: the frozen snapshot carries the
	// complete build decision (schedule + plan force/suppress + timing).
	frozen := s.planSvc.Freeze(event.ProposalSlot)

	if !frozen.Build.Build {
		s.log.WithFields(logrus.Fields{
			"slot":   event.ProposalSlot,
			"reason": frozen.Build.SkipReason,
		}).Debug("Skipping build for slot")

		s.fireBuildSkipped(event.ProposalSlot, frozen.Build)

		return
	}

	// Check if already scheduled/building/built for this slot
	s.scheduledBuildMu.Lock()
	if s.buildStartedSlots[event.ProposalSlot] {
		s.scheduledBuildMu.Unlock()
		return
	}
	s.buildStartedSlots[event.ProposalSlot] = true
	s.scheduledBuildMu.Unlock()

	s.scheduleBuildForSlot(event.ProposalSlot, frozen.Build.BuildStartTimeMs)
}

// fireBuildSkipped emits a BuildSkippedEvent (once per slot) when the skip is
// worth recording: a plan exists for the slot or a consumer is effectively
// active (build.PlanInvolved), so a results tracker can explain why no
// payload was produced. Other skips stay silent.
func (s *Service) fireBuildSkipped(slot phase0.Slot, build *action_plan.ResolvedBuildSettings) {
	if !build.PlanInvolved {
		return
	}

	reason := build.SkipReason

	s.scheduledBuildMu.Lock()
	if s.skipFiredSlots[slot] {
		s.scheduledBuildMu.Unlock()

		return
	}

	s.skipFiredSlots[slot] = true

	// Prune inline: the emitPayloadReady cleanup only runs on successful
	// builds, which never happen when every slot is skipped.
	if slot > 64 {
		cutoff := slot - 64
		for oldSlot := range s.skipFiredSlots {
			if oldSlot < cutoff {
				delete(s.skipFiredSlots, oldSlot)
			}
		}
	}
	s.scheduledBuildMu.Unlock()

	s.buildSkippedDispatcher.Fire(&BuildSkippedEvent{
		Slot:   slot,
		Reason: reason,
	})
}

// scheduleBuildForSlot schedules payload building for the given slot.
// buildStartMs is the slot's frozen build start time, milliseconds relative
// to the proposal slot start:
//   - Negative values (e.g. -3000) mean before the slot starts.
//   - Positive values mean after the slot starts.
//   - Zero means build immediately when the event is received.
func (s *Service) scheduleBuildForSlot(slot phase0.Slot, buildStartMs int64) {
	slotStart := s.chainSvc.SlotToTime(slot)
	buildTime := slotStart.Add(time.Duration(buildStartMs) * time.Millisecond)
	delay := time.Until(buildTime)

	if delay <= 0 {
		// Build time already passed – build immediately.
		s.log.WithFields(logrus.Fields{
			"slot":     slot,
			"delay_ms": delay.Milliseconds(),
		}).Debug("Build start time already passed, building immediately")

		go s.executeBuildForSlot(slot)

		return
	}

	s.log.WithFields(logrus.Fields{
		"slot":     slot,
		"delay_ms": delay.Milliseconds(),
	}).Info("Scheduling build for slot")

	time.AfterFunc(delay, func() {
		s.executeBuildForSlot(slot)
	})
}

// executeBuildForSlot fetches the latest cached payload_attributes for the
// given slot and performs payload building.
func (s *Service) executeBuildForSlot(slot phase0.Slot) {
	event := s.clClient.Events().GetLatestPayloadAttributes(slot)
	if event == nil {
		s.log.WithField("slot", slot).Warn(
			"No cached payload attributes for slot, skipping build",
		)
		return
	}

	s.log.WithFields(logrus.Fields{
		"slot":        event.ProposalSlot,
		"parent_hash": fmt.Sprintf("%x", event.ParentBlockHash[:8]),
	}).Info("Starting payload build")

	// Notify subscribers that building has started so the build can be rendered
	// as in-progress before the payload is ready.
	s.buildStartedDispatcher.Fire(&PayloadBuildStartedEvent{
		Slot:      slot,
		StartedAt: time.Now(),
	})

	// Size the build deadline to the configured build time plus a margin for the
	// engine getPayload and finality lookups, so a long PayloadBuildTime doesn't
	// make the getPayload call time out spuriously.
	buildTimeout := time.Duration(s.cfg.PayloadBuildTime)*time.Millisecond + buildCallTimeout
	ctx, cancel := context.WithTimeout(s.ctx, buildTimeout)
	defer cancel()

	payloadEvent, err := s.payloadBuilder.BuildPayloadFromAttributes(ctx, event)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Error(
			"Failed to build payload from attributes",
		)

		// Notify subscribers so the in-progress build is marked failed rather than
		// left rendered as perpetually building.
		s.buildFailedDispatcher.Fire(&PayloadBuildFailedEvent{
			Slot:     slot,
			Error:    err.Error(),
			FailedAt: time.Now(),
		})

		return
	}

	s.emitPayloadReady(slot, payloadEvent)
}

// handlePayloadAvailableEvent processes an execution_payload_available event (Gloas only).
// This is called when the node verifies that a payload and blobs are available.
// With payload_attributes-based building, we only track the last known payload here.
func (s *Service) handlePayloadAvailableEvent(event *beacon.PayloadAvailableEvent) {
	s.log.WithFields(logrus.Fields{
		"slot":       event.Slot,
		"block_root": fmt.Sprintf("%x", event.BlockRoot[:8]),
	}).Debug("Payload available event received")

	// Update last known payload (for Gloas tracking)
	s.lastKnownPayloadMu.Lock()
	s.lastKnownPayloadBlockRoot = event.BlockRoot
	s.lastKnownPayloadSlot = event.Slot
	s.lastKnownPayloadMu.Unlock()

	// NOTE: We no longer build from payload available events.
	// Building is now triggered by payload_attributes events.
}

// emitPayloadReady stores the payload and emits the ready event.
func (s *Service) emitPayloadReady(slot phase0.Slot, payloadEvent *Payload) {
	// Store in cache
	s.payloadCache.Store(payloadEvent)

	// Emit the payload ready event to subscribers
	s.payloadReadyDispatcher.Fire(payloadEvent)

	s.log.WithFields(logrus.Fields{
		"slot":              slot,
		"block_hash":        fmt.Sprintf("%x", payloadEvent.BlockHash[:8]),
		"block_value":       payloadEvent.BlockValue,
		"parent_block_hash": fmt.Sprintf("%x", payloadEvent.Attributes.ParentBlockHash[:8]),
	}).Info("Payload built and dispatched")

	// Mark slot as built (next_n schedule accounting + WebUI status).
	s.planSvc.OnSlotBuilt(slot)
	s.lastBuiltSlot.Store(uint64(slot))

	s.incrementStat(func(stats *BuilderStats) {
		stats.SlotsBuilt++
	})

	// Cleanup old data
	if slot > 64 {
		cleanupSlot := slot - 64
		s.payloadCache.Cleanup(cleanupSlot)
		s.clClient.Events().CleanupPayloadAttributesCache(cleanupSlot)

		// Cleanup old build tracking
		s.scheduledBuildMu.Lock()
		for oldSlot := range s.buildStartedSlots {
			if oldSlot < cleanupSlot {
				delete(s.buildStartedSlots, oldSlot)
			}
		}
		s.scheduledBuildMu.Unlock()

		// Cleanup old won payload tracking, keeping the 10 most recent.
		const keepWonPayloads = 10
		s.wonPayloadsMu.Lock()
		for len(s.wonPayloads) > keepWonPayloads {
			var oldestHash phase0.Hash32
			var oldestSlot phase0.Slot
			first := true

			for hash, wonSlot := range s.wonPayloads {
				if first || wonSlot < oldestSlot {
					oldestHash = hash
					oldestSlot = wonSlot
					first = false
				}
			}

			delete(s.wonPayloads, oldestHash)
		}
		s.wonPayloadsMu.Unlock()
	}
}

// markPayloadWon records a payload as won (included on-chain), deduplicating
// between the two detection methods (payload_attributes parent hash and head event).
func (s *Service) markPayloadWon(blockHash phase0.Hash32, slot phase0.Slot) {
	s.wonPayloadsMu.Lock()
	if _, ok := s.wonPayloads[blockHash]; ok {
		s.wonPayloadsMu.Unlock()
		return
	}

	s.wonPayloads[blockHash] = slot
	s.wonPayloadsMu.Unlock()

	s.log.WithFields(logrus.Fields{
		"slot":       slot,
		"block_hash": fmt.Sprintf("%x", blockHash[:8]),
	}).Info("Our payload was included on-chain")

	s.incrementStat(func(stats *BuilderStats) {
		stats.BlocksIncluded++
	})
}
