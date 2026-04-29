package builder

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builderapi/validators"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/proposerpreferences"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

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
	cfg                    *Config
	clClient               *beacon.Client
	chainSvc               chain.Service
	engineClient           *engine.Client
	feeRecipient           common.Address
	validatorStore         *validators.Store          // optional: use fee recipient from validator registrations
	validatorIndexCache    *chain.ValidatorIndexCache // optional: index→pubkey cache so we don't query beacon every build
	propPrefCache          *proposerpreferences.Cache // optional: proposer preferences (Gloas+)
	payloadBuilder         *PayloadBuilder
	payloadCache           *PayloadCache
	payloadReadyDispatcher *utils.Dispatcher[*PayloadReadyEvent]
	slotManager            *SlotManager
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

	// Payload inclusion tracking (deduplication between detection methods)
	wonPayloadsMu sync.Mutex
	wonPayloads   map[phase0.Hash32]phase0.Slot
}

// NewService creates a new builder service.
// validatorStore is optional; when set, fee recipient is taken from the proposer's validator registration.
// If no registration exists for a proposer, the build is skipped for that slot.
// validatorIndexCache is optional; when set, proposer index→pubkey is read from cache instead of querying beacon state every build.
func NewService(
	cfg *Config,
	clClient *beacon.Client,
	chainSvc chain.Service,
	engineClient *engine.Client,
	feeRecipient common.Address,
	validatorStore *validators.Store,
	validatorIndexCache *chain.ValidatorIndexCache,
	log logrus.FieldLogger,
) (*Service, error) {
	serviceLog := log.WithField("component", "builder-service")

	s := &Service{
		cfg:                    cfg,
		clClient:               clClient,
		chainSvc:               chainSvc,
		engineClient:           engineClient,
		feeRecipient:           feeRecipient,
		validatorStore:         validatorStore,
		validatorIndexCache:    validatorIndexCache,
		payloadCache:           NewPayloadCache(DefaultCacheSize),
		payloadReadyDispatcher: &utils.Dispatcher[*PayloadReadyEvent]{},
		stats:                  &BuilderStats{},
		log:                    serviceLog,
		buildStartedSlots:      make(map[phase0.Slot]bool),
		wonPayloads:            make(map[phase0.Hash32]phase0.Slot, 16),
	}

	return s, nil
}

// Start initializes and starts the builder service.
func (s *Service) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	// Initialize managers
	s.slotManager = NewSlotManager(s.cfg)

	// Create payload builder
	s.payloadBuilder = NewPayloadBuilder(
		s.clClient,
		s.engineClient,
		s.feeRecipient,
		s.cfg.PayloadBuildTime,
		s.log,
		s.validatorStore,
		s.validatorIndexCache,
		s.propPrefCache,
		s.chainSvc.IsGloas,
	)

	// Start event stream
	if err := s.clClient.Events().Start(s.ctx); err != nil {
		return fmt.Errorf("failed to start event stream: %w", err)
	}

	// Start main loop
	s.wg.Add(1)

	go s.run()

	s.log.Info("Builder service started")

	return nil
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
func (s *Service) GetConfig() *Config {
	return s.cfg
}

// UpdateConfig updates the service configuration at runtime.
func (s *Service) UpdateConfig(cfg *Config) error {
	s.cfg = cfg
	s.slotManager.UpdateConfig(cfg)

	s.log.Info("Configuration updated")

	return nil
}

// GetCurrentSlot returns the current slot.
func (s *Service) GetCurrentSlot() phase0.Slot {
	return s.slotManager.GetCurrentSlot()
}

// GetChainSpec returns the chain specification.
func (s *Service) GetChainSpec() *beacon.ChainSpec {
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

// IsGloas returns whether we're on the Gloas fork.
func (s *Service) IsGloas() bool {
	return s.chainSvc.IsGloas()
}

// GetProposerPreferencesCache returns the proposer preferences cache.
func (s *Service) GetProposerPreferencesCache() *proposerpreferences.Cache {
	return s.propPrefCache
}

// SetProposerPreferencesCache sets the proposer preferences cache used for Gloas+ builds.
// Must be called before Start().
func (s *Service) SetProposerPreferencesCache(cache *proposerpreferences.Cache) {
	s.propPrefCache = cache
}

// SubscribePayloadReady subscribes to payload ready events.
// Consumers (like the ePBS service) use this to receive built payloads.
func (s *Service) SubscribePayloadReady(capacity int) *utils.Subscription[*PayloadReadyEvent] {
	return s.payloadReadyDispatcher.Subscribe(capacity, false)
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
		"is_gloas":  s.chainSvc.IsGloas(),
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

	s.markPayloadWon(blockInfo.ExecutionBlockHash, payload.Slot)
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
		s.markPayloadWon(event.ParentBlockHash, payload.Slot)
	}

	// Check if we should build for this slot
	if !s.slotManager.ShouldBuildForSlot(event.ProposalSlot) {
		s.log.WithField("slot", event.ProposalSlot).Debug("Skipping slot per schedule")
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

	s.scheduleBuildForSlot(event.ProposalSlot)
}

// scheduleBuildForSlot schedules payload building for the given slot.
// BuildStartTime is milliseconds relative to the proposal slot start:
//   - Negative values (e.g. -3000) mean before the slot starts.
//   - Positive values mean after the slot starts.
//   - Zero means build immediately when the event is received.
func (s *Service) scheduleBuildForSlot(slot phase0.Slot) {
	buildStartMs := s.cfg.EPBS.BuildStartTime
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

// gloasFullBuildLeadSlots is how many slots after the Gloas fork-epoch boundary
// we skip building entirely. This avoids ambiguous parent-block-hash semantics
// during the transition where attrs.ParentBlockRoot may point to a Fulu block
// that has no embedded bid for us to read.
const gloasFullBuildLeadSlots = phase0.Slot(3)

// executeBuildForSlot fetches the latest cached payload_attributes for the
// given slot and performs payload building.
//
// Build mode is fork-dependent:
//   - Pre-Gloas (Electra/Fulu): single FULL build using attrs.ParentBlockHash as the FCU head.
//     No beacon-block-by-root fetch is needed because there is no bid to resolve.
//   - Gloas fork boundary [GLOAS_FIRST_SLOT, GLOAS_FIRST_SLOT+gloasFullBuildLeadSlots]:
//     skipped entirely. The parent beacon block is Fulu (no embedded bid) so EMPTY
//     can't be derived; rather than special-case both variants we duck the few slots.
//   - Post-Gloas (after the boundary window): fetch the chosen bid embedded in the
//     beacon block at attrs.ParentBlockRoot and run two sequential builds:
//       1. EMPTY with head = bid.parent_block_hash (assumes prior slot was missed)
//       2. FULL  with head = bid.block_hash        (assumes prior slot was published)
//     Each completed build emits its PayloadReadyEvent independently so ePBS can
//     start bidding on whichever lands first.
func (s *Service) executeBuildForSlot(slot phase0.Slot) {
	event := s.clClient.Events().GetLatestPayloadAttributes(slot)
	if event == nil {
		s.log.WithField("slot", slot).Warn(
			"No cached payload attributes for slot, skipping build",
		)
		return
	}

	chainSpec := s.chainSvc.GetChainSpec()
	isGloas := s.chainSvc.IsGloas()

	if isGloas && chainSpec != nil && chainSpec.GloasForkEpoch != nil {
		gloasFirstSlot := phase0.Slot(*chainSpec.GloasForkEpoch * chainSpec.SlotsPerEpoch)
		if slot >= gloasFirstSlot && slot <= gloasFirstSlot+gloasFullBuildLeadSlots {
			s.log.WithFields(logrus.Fields{
				"slot":             slot,
				"gloas_first_slot": gloasFirstSlot,
				"skip_until":       gloasFirstSlot + gloasFullBuildLeadSlots,
			}).Info("Skipping build at Gloas fork boundary")
			return
		}
	}

	s.log.WithFields(logrus.Fields{
		"slot":        event.ProposalSlot,
		"parent_hash": fmt.Sprintf("%x", event.ParentBlockHash[:8]),
		"is_gloas":    isGloas,
	}).Info("Starting payload build")

	if !isGloas {
		s.runSingleBuild(slot, event, PayloadVariantFull, phase0.Hash32{}, true)
		return
	}

	bid, err := s.fetchBidForParent(event.ParentBlockRoot)
	if err != nil {
		s.log.WithError(err).WithFields(logrus.Fields{
			"slot":              slot,
			"parent_block_root": fmt.Sprintf("%x", event.ParentBlockRoot[:8]),
		}).Warn("Failed to fetch bid for parent block, falling back to single FULL build")

		s.runSingleBuild(slot, event, PayloadVariantFull, phase0.Hash32{}, true)
		return
	}

	// Sequential: EMPTY first, then FULL. Each emits its event when it completes.
	// Use a single statsCounted flag so SlotsBuilt / OnSlotBuilt fires once per slot,
	// not once per variant.
	emptyOK := s.runSingleBuild(slot, event, PayloadVariantEmpty, bid.ParentBlockHash, true)
	s.runSingleBuild(slot, event, PayloadVariantFull, bid.BlockHash, !emptyOK)
}

// runSingleBuild executes one variant of BuildPayloadFromAttributes and emits
// its PayloadReadyEvent on success. countSlotStat controls whether
// SlotsBuilt/OnSlotBuilt fires for this build — we only want to count one per
// slot, not one per variant.
func (s *Service) runSingleBuild(
	slot phase0.Slot,
	event *beacon.PayloadAttributesEvent,
	variant PayloadVariant,
	headOverride phase0.Hash32,
	countSlotStat bool,
) bool {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	payloadEvent, err := s.payloadBuilder.BuildPayloadFromAttributes(ctx, event, variant, headOverride)
	if err != nil {
		s.log.WithError(err).WithFields(logrus.Fields{
			"slot":    slot,
			"variant": variant.String(),
		}).Error("Failed to build payload from attributes")
		return false
	}

	s.emitPayloadReady(slot, payloadEvent, countSlotStat)
	return true
}

// fetchBidForParent fetches the chosen execution payload bid from the beacon
// block at the given root. Used post-Gloas to derive the FCU head for the
// FULL and EMPTY variants.
func (s *Service) fetchBidForParent(parentRoot phase0.Root) (*beacon.BidInfo, error) {
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()

	return s.clClient.GetExecutionPayloadBid(ctx, fmt.Sprintf("0x%x", parentRoot[:]))
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
// countSlotStat should be true for at most one variant per slot so SlotsBuilt
// and OnSlotBuilt fire once per slot, not once per variant.
func (s *Service) emitPayloadReady(slot phase0.Slot, payloadEvent *PayloadReadyEvent, countSlotStat bool) {
	// Store in cache
	s.payloadCache.Store(payloadEvent)

	// Emit the payload ready event to subscribers
	s.payloadReadyDispatcher.Fire(payloadEvent)

	s.log.WithFields(logrus.Fields{
		"slot":              slot,
		"variant":           payloadEvent.Variant.String(),
		"block_hash":        fmt.Sprintf("%x", payloadEvent.BlockHash[:8]),
		"block_value":       payloadEvent.BlockValue,
		"source":            payloadEvent.BuildSource.String(),
		"parent_block_hash": fmt.Sprintf("%x", payloadEvent.ParentBlockHash[:8]),
	}).Info("Payload built and dispatched")

	if !countSlotStat {
		return
	}

	// Mark slot as built
	s.slotManager.OnSlotBuilt(slot)

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

// incrementStat safely increments statistics.
func (s *Service) incrementStat(fn func(*BuilderStats)) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()

	fn(s.stats)
}

// IncrementBidsSubmitted increments the bids submitted counter.
// Called by the ePBS service when a bid is submitted.
func (s *Service) IncrementBidsSubmitted() {
	s.incrementStat(func(stats *BuilderStats) {
		stats.BidsSubmitted++
	})
}

// IncrementBlocksIncluded increments the blocks included and bids won counters.
// Called by the ePBS service when our payload is included in a beacon block.
func (s *Service) IncrementBlocksIncluded() {
	s.incrementStat(func(stats *BuilderStats) {
		stats.BlocksIncluded++
		stats.BidsWon++
	})
}

// IncrementRevealsSuccess increments the successful reveals counter.
func (s *Service) IncrementRevealsSuccess() {
	s.incrementStat(func(stats *BuilderStats) {
		stats.RevealsSuccess++
	})
}

// IncrementRevealsFailed increments the failed reveals counter.
func (s *Service) IncrementRevealsFailed() {
	s.incrementStat(func(stats *BuilderStats) {
		stats.RevealsFailed++
	})
}

// IncrementRevealsSkipped increments the skipped reveals counter.
func (s *Service) IncrementRevealsSkipped() {
	s.incrementStat(func(stats *BuilderStats) {
		stats.RevealsSkipped++
	})
}
