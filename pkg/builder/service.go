package builder

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/attestantio/go-eth2-client/spec/capella"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/ethereum/go-ethereum/common"
	"github.com/sirupsen/logrus"

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
type Service struct {
	cfg                    *Config
	clClient               *beacon.Client
	engineClient           *engine.Client
	feeRecipient           common.Address
	payloadBuilder         *PayloadBuilder
	payloadCache           *PayloadCache
	payloadReadyDispatcher *utils.Dispatcher[*PayloadReadyEvent]
	slotManager            *SlotManager
	chainSpec              *beacon.ChainSpec
	genesis                *beacon.Genesis
	stats                  *BuilderStats
	statsMu                sync.RWMutex
	ctx                    context.Context
	cancel                 context.CancelFunc
	log                    logrus.FieldLogger
	wg                     sync.WaitGroup

	// Fork tracking
	isGloas bool

	// Last known execution payload tracking (for Gloas)
	// In Gloas, blocks don't contain execution payloads - they come separately.
	// We need to track the last block that has a known payload.
	lastKnownPayloadMu        sync.RWMutex
	lastKnownPayloadBlockRoot phase0.Root   // Beacon block root with known payload
	lastKnownPayloadBlockHash phase0.Hash32 // Execution block hash from that payload
	lastKnownPayloadSlot      phase0.Slot   // Slot of the block with known payload

	// Scheduled build tracking
	// We delay building until BuildStartTime (75% into slot by default)
	scheduledBuildMu    sync.Mutex
	scheduledBuildSlot  phase0.Slot          // The slot we're scheduled to build for
	scheduledBuildTimer *time.Timer          // Timer for scheduled build
	scheduledBuildEvent *beacon.HeadEvent    // Head event to use for building
	buildStartedSlots   map[phase0.Slot]bool // Slots where building has started (to prevent re-building)
}

// NewService creates a new builder service.
func NewService(
	cfg *Config,
	clClient *beacon.Client,
	engineClient *engine.Client,
	feeRecipient common.Address,
	log logrus.FieldLogger,
) (*Service, error) {
	serviceLog := log.WithField("component", "builder-service")

	s := &Service{
		cfg:                    cfg,
		clClient:               clClient,
		engineClient:           engineClient,
		feeRecipient:           feeRecipient,
		payloadCache:           NewPayloadCache(DefaultCacheSize),
		payloadReadyDispatcher: &utils.Dispatcher[*PayloadReadyEvent]{},
		stats:                  &BuilderStats{},
		log:                    serviceLog,
		buildStartedSlots:      make(map[phase0.Slot]bool),
	}

	return s, nil
}

// Start initializes and starts the builder service.
func (s *Service) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	// Fetch chain spec
	chainSpec, err := s.clClient.GetChainSpec(s.ctx)
	if err != nil {
		return fmt.Errorf("failed to get chain spec: %w", err)
	}

	s.chainSpec = chainSpec

	// Fetch genesis
	genesis, err := s.clClient.GetGenesis(s.ctx)
	if err != nil {
		return fmt.Errorf("failed to get genesis: %w", err)
	}

	s.genesis = genesis

	// Check if we're on Gloas fork
	isGloas, err := s.clClient.IsGloas(s.ctx)
	if err != nil {
		s.log.WithError(err).Warn("Failed to check Gloas fork, assuming non-Gloas")
		isGloas = false
	}

	s.isGloas = isGloas
	s.log.WithField("is_gloas", isGloas).Info("Fork detected")

	// Initialize managers
	s.slotManager = NewSlotManager(s.cfg)

	// Create payload builder
	s.payloadBuilder = NewPayloadBuilder(
		s.clClient,
		s.engineClient,
		s.chainSpec,
		s.genesis,
		s.feeRecipient,
		s.log,
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
	return s.chainSpec
}

// GetGenesis returns the genesis information.
func (s *Service) GetGenesis() *beacon.Genesis {
	return s.genesis
}

// GetCLClient returns the consensus layer client.
func (s *Service) GetCLClient() *beacon.Client {
	return s.clClient
}

// IsGloas returns whether we're on the Gloas fork.
func (s *Service) IsGloas() bool {
	return s.isGloas
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
	payloadSub := s.clClient.Events().SubscribePayloadEnvelope()

	defer headSub.Unsubscribe()
	defer payloadSub.Unsubscribe()

	for {
		select {
		case <-s.ctx.Done():
			return

		case event := <-headSub.Channel():
			s.handleHeadEvent(event)

		case event := <-payloadSub.Channel():
			s.handlePayloadEnvelopeEvent(event)
		}
	}
}

// handleHeadEvent processes a head event (new block received).
func (s *Service) handleHeadEvent(event *beacon.HeadEvent) {
	s.log.WithFields(logrus.Fields{
		"head_slot": event.Slot,
		"next_slot": event.Slot + 1,
		"is_gloas":  s.isGloas,
	}).Debug("Head event received")

	// Build for the next slot
	nextSlot := event.Slot + 1

	// Check if we should build for this slot
	if !s.slotManager.ShouldBuildForSlot(nextSlot) {
		s.log.WithField("slot", nextSlot).Debug("Skipping slot per schedule")
		return
	}

	// Fetch and cache the beacon state for this block (do this immediately)
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer cancel()

		if err := s.clClient.FetchStateForBlock(ctx, event.Block, event.State, event.Slot); err != nil {
			s.log.WithError(err).WithField("slot", event.Slot).Warn("Failed to fetch beacon state")
		}

		// For pre-Gloas, validate the withdrawals in this block against expected
		if !s.isGloas && s.cfg.ValidateWithdrawals {
			s.runBlockWithdrawalValidation(ctx, event.Block, event.Slot)
		}
	}()

	// Schedule the payload build for BuildStartTime into the current slot
	s.scheduleBuild(nextSlot, event)
}

// scheduleBuild schedules a payload build for the specified slot.
// Building is delayed until BuildStartTime into the current slot (default 75%).
func (s *Service) scheduleBuild(slot phase0.Slot, headEvent *beacon.HeadEvent) {
	s.scheduledBuildMu.Lock()
	defer s.scheduledBuildMu.Unlock()

	// Check if we've already started building for this slot
	if s.buildStartedSlots[slot] {
		s.log.WithField("slot", slot).Debug("Already built for this slot, not scheduling")
		return
	}

	// Cancel any existing timer for a different slot
	if s.scheduledBuildTimer != nil && s.scheduledBuildSlot != slot {
		s.scheduledBuildTimer.Stop()
		s.scheduledBuildTimer = nil
	}

	// Calculate when to build
	// BuildStartTime is relative to the current slot start (the slot whose head we received)
	currentSlotStart := s.genesis.GenesisTime.Add(time.Duration(headEvent.Slot) * s.chainSpec.SecondsPerSlot)
	buildTime := currentSlotStart.Add(time.Duration(s.cfg.EPBS.BuildStartTime) * time.Millisecond)
	delay := time.Until(buildTime)

	// If BuildStartTime is 0 or we're past the build time, build immediately
	if s.cfg.EPBS.BuildStartTime == 0 || delay <= 0 {
		s.log.WithFields(logrus.Fields{
			"slot":             slot,
			"build_start_time": s.cfg.EPBS.BuildStartTime,
		}).Debug("Building immediately (no delay or past build time)")

		s.buildStartedSlots[slot] = true
		go s.executeBuild(slot, headEvent)

		return
	}

	// Schedule the build
	s.scheduledBuildSlot = slot
	s.scheduledBuildEvent = headEvent

	s.log.WithFields(logrus.Fields{
		"slot":             slot,
		"build_start_time": s.cfg.EPBS.BuildStartTime,
		"delay_ms":         delay.Milliseconds(),
	}).Info("Scheduled payload build")

	s.scheduledBuildTimer = time.AfterFunc(delay, func() {
		s.scheduledBuildMu.Lock()
		defer s.scheduledBuildMu.Unlock()

		// Check if this is still the scheduled slot
		if s.scheduledBuildSlot != slot {
			return
		}

		// Check if already built (by payload envelope)
		if s.buildStartedSlots[slot] {
			s.log.WithField("slot", slot).Debug("Slot already built, skipping scheduled build")
			return
		}

		s.buildStartedSlots[slot] = true
		go s.executeBuild(slot, s.scheduledBuildEvent)
	})
}

// executeBuild performs the actual payload building.
func (s *Service) executeBuild(slot phase0.Slot, headEvent *beacon.HeadEvent) {
	s.log.WithFields(logrus.Fields{
		"slot":     slot,
		"is_gloas": s.isGloas,
	}).Info("Starting payload build")

	if s.isGloas {
		// Gloas: Build based on last known payload, not this block
		if err := s.processSlotGloas(slot, headEvent); err != nil {
			s.log.WithError(err).WithField("slot", slot).Error("Failed to process slot (Gloas)")
		}
	} else {
		// Electra/Fulu: Build based on this block (payload is in the block)
		if err := s.processSlotPreGloas(slot, headEvent); err != nil {
			s.log.WithError(err).WithField("slot", slot).Error("Failed to process slot (pre-Gloas)")
		}
	}
}

// handlePayloadEnvelopeEvent processes a payload envelope event (Gloas only).
// This is called when a payload is revealed for a block.
func (s *Service) handlePayloadEnvelopeEvent(event *beacon.PayloadEnvelopeEvent) {
	s.log.WithFields(logrus.Fields{
		"slot":       event.Slot,
		"block_root": fmt.Sprintf("%x", event.BlockRoot[:8]),
		"block_hash": fmt.Sprintf("%x", event.BlockHash[:8]),
	}).Debug("Payload envelope event received")

	// Update last known payload
	s.lastKnownPayloadMu.Lock()
	s.lastKnownPayloadBlockRoot = event.BlockRoot
	s.lastKnownPayloadBlockHash = event.BlockHash
	s.lastKnownPayloadSlot = event.Slot
	s.lastKnownPayloadMu.Unlock()

	// Validate payload envelope withdrawals if enabled
	if s.cfg.ValidateWithdrawals {
		go func() {
			ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
			defer cancel()

			s.runPayloadEnvelopeWithdrawalValidation(ctx, event.BlockRoot, event.Slot)
		}()
	}

	// If this is Gloas, check if we should build on this envelope
	// We only build if:
	// 1. We're past the BuildStartTime (75% into slot by default)
	// 2. We haven't already started building for the next slot
	if s.isGloas {
		nextSlot := event.Slot + 1

		// Check if we should build for this slot
		if !s.slotManager.ShouldBuildForSlot(nextSlot) {
			return
		}

		// Check if we're past the build start time
		currentSlotStart := s.genesis.GenesisTime.Add(time.Duration(event.Slot) * s.chainSpec.SecondsPerSlot)
		buildTime := currentSlotStart.Add(time.Duration(s.cfg.EPBS.BuildStartTime) * time.Millisecond)

		if time.Now().Before(buildTime) {
			// We're before BuildStartTime - the scheduled build will handle this
			// Just update the last known payload (already done above)
			s.log.WithFields(logrus.Fields{
				"slot":             nextSlot,
				"build_start_time": s.cfg.EPBS.BuildStartTime,
			}).Debug("Payload envelope received before build time, scheduled build will use it")
			return
		}

		// We're past BuildStartTime - check if we've already started building
		s.scheduledBuildMu.Lock()
		alreadyBuilding := s.buildStartedSlots[nextSlot]
		if !alreadyBuilding {
			s.buildStartedSlots[nextSlot] = true
		}
		s.scheduledBuildMu.Unlock()

		if alreadyBuilding {
			s.log.WithField("slot", nextSlot).Debug("Already building for this slot, ignoring late payload envelope")
			return
		}

		// Check if we already have a payload in cache
		if s.payloadCache.Get(nextSlot) != nil {
			s.log.WithField("slot", nextSlot).Debug("Already built for this slot, skipping")
			return
		}

		s.log.WithField("slot", nextSlot).Info("Building on payload envelope (past build time, not yet built)")

		go func() {
			ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
			defer cancel()

			if err := s.buildOnPayloadEnvelope(ctx, nextSlot, event); err != nil {
				s.log.WithError(err).WithField("slot", nextSlot).Error("Failed to build on payload envelope")
			}
		}()
	}
}

// processSlotPreGloas handles building a payload for pre-Gloas forks (Electra/Fulu).
// In these forks, the execution payload is included in the beacon block.
func (s *Service) processSlotPreGloas(slot phase0.Slot, headEvent *beacon.HeadEvent) error {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	s.log.WithFields(logrus.Fields{
		"slot":        slot,
		"parent_root": fmt.Sprintf("%x", headEvent.Block[:8]),
	}).Info("Building payload for slot (pre-Gloas)")

	// Build the payload - use head block as parent
	payloadEvent, err := s.payloadBuilder.BuildPayload(ctx, slot, headEvent)
	if err != nil {
		return fmt.Errorf("failed to build payload: %w", err)
	}

	s.emitPayloadReady(slot, payloadEvent)

	return nil
}

// processSlotGloas handles building a payload for Gloas fork.
// In Gloas, execution payloads are separate from beacon blocks.
// We build based on the last block that has a known payload.
func (s *Service) processSlotGloas(slot phase0.Slot, headEvent *beacon.HeadEvent) error {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	s.lastKnownPayloadMu.RLock()
	lastPayloadBlockRoot := s.lastKnownPayloadBlockRoot
	lastPayloadBlockHash := s.lastKnownPayloadBlockHash
	lastPayloadSlot := s.lastKnownPayloadSlot
	s.lastKnownPayloadMu.RUnlock()

	// Check if we have a known payload to build on
	if lastPayloadBlockHash == (phase0.Hash32{}) {
		s.log.WithField("slot", slot).Warn("No known execution payload yet, cannot build (Gloas)")
		return nil
	}

	s.log.WithFields(logrus.Fields{
		"slot":                    slot,
		"parent_root":             fmt.Sprintf("%x", headEvent.Block[:8]),
		"last_payload_slot":       lastPayloadSlot,
		"last_payload_block_hash": fmt.Sprintf("%x", lastPayloadBlockHash[:8]),
	}).Info("Building payload for slot (Gloas - on last known payload)")

	// Build the payload using last known execution block hash
	payloadEvent, err := s.payloadBuilder.BuildPayloadGloas(
		ctx,
		slot,
		headEvent,
		lastPayloadBlockRoot,
		lastPayloadBlockHash,
	)
	if err != nil {
		return fmt.Errorf("failed to build payload (Gloas): %w", err)
	}

	s.emitPayloadReady(slot, payloadEvent)

	return nil
}

// buildOnPayloadEnvelope builds a payload when a payload envelope is received.
// This allows building on the now-complete parent block.
func (s *Service) buildOnPayloadEnvelope(
	ctx context.Context,
	slot phase0.Slot,
	envelope *beacon.PayloadEnvelopeEvent,
) error {
	s.log.WithFields(logrus.Fields{
		"slot":        slot,
		"parent_root": fmt.Sprintf("%x", envelope.BlockRoot[:8]),
		"parent_hash": fmt.Sprintf("%x", envelope.BlockHash[:8]),
	}).Info("Building payload on received payload envelope (Gloas)")

	// Build the payload using the envelope's block hash as parent
	payloadEvent, err := s.payloadBuilder.BuildPayloadOnEnvelope(ctx, slot, envelope)
	if err != nil {
		return fmt.Errorf("failed to build payload on envelope: %w", err)
	}

	s.emitPayloadReady(slot, payloadEvent)

	return nil
}

// emitPayloadReady stores the payload and emits the ready event.
func (s *Service) emitPayloadReady(slot phase0.Slot, payloadEvent *PayloadReadyEvent) {
	// Store in cache
	s.payloadCache.Store(payloadEvent)

	// Emit the payload ready event to subscribers
	s.payloadReadyDispatcher.Fire(payloadEvent)

	s.log.WithFields(logrus.Fields{
		"slot":        slot,
		"block_hash":  fmt.Sprintf("%x", payloadEvent.BlockHash[:8]),
		"block_value": payloadEvent.BlockValue,
		"source":      payloadEvent.BuildSource.String(),
	}).Info("Payload built and dispatched")

	// Mark slot as built
	s.slotManager.OnSlotBuilt(slot)

	s.incrementStat(func(stats *BuilderStats) {
		stats.SlotsBuilt++
	})

	// Cleanup old data
	if slot > 64 {
		cleanupSlot := slot - 64
		s.payloadCache.Cleanup(cleanupSlot)

		// Cleanup old build tracking
		s.scheduledBuildMu.Lock()
		for oldSlot := range s.buildStartedSlots {
			if oldSlot < cleanupSlot {
				delete(s.buildStartedSlots, oldSlot)
			}
		}
		s.scheduledBuildMu.Unlock()
	}
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

// IncrementBlocksIncluded increments the blocks included counter.
// Called by the ePBS service when our payload is included.
func (s *Service) IncrementBlocksIncluded() {
	s.incrementStat(func(stats *BuilderStats) {
		stats.BlocksIncluded++
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

// runBlockWithdrawalValidation runs withdrawal validation for a pre-Gloas block.
// It fetches the block info to get the parent root, then validates.
func (s *Service) runBlockWithdrawalValidation(ctx context.Context, blockRoot phase0.Root, slot phase0.Slot) {
	// Get block info to find the parent root
	blockID := fmt.Sprintf("0x%x", blockRoot[:])

	blockInfo, err := s.clClient.GetBlockInfo(ctx, blockID)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Warn("Failed to get block info for withdrawal validation")
		return
	}

	// Validate withdrawals using parent block's state
	s.validateBlockWithdrawals(ctx, blockRoot, blockInfo.ParentRoot, slot)
}

// runPayloadEnvelopeWithdrawalValidation runs withdrawal validation for a Gloas payload envelope.
// It fetches the block info to get the parent root, then validates.
func (s *Service) runPayloadEnvelopeWithdrawalValidation(
	ctx context.Context,
	blockRoot phase0.Root,
	slot phase0.Slot,
) {
	// Get block info to find the parent root
	blockID := fmt.Sprintf("0x%x", blockRoot[:])

	blockInfo, err := s.clClient.GetBlockInfo(ctx, blockID)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Warn("Failed to get block info for payload envelope validation")
		return
	}

	// Validate withdrawals using parent block's state
	s.validatePayloadEnvelopeWithdrawals(ctx, blockRoot, blockInfo.ParentRoot, slot)
}

// validateBlockWithdrawals validates that the withdrawals in a block match expected withdrawals.
// This is called for pre-Gloas blocks where the execution payload is embedded in the block.
// parentBlockRoot is the root of the block whose state we use to calculate expected withdrawals.
func (s *Service) validateBlockWithdrawals(
	ctx context.Context,
	blockRoot phase0.Root,
	parentBlockRoot phase0.Root,
	slot phase0.Slot,
) {
	if !s.cfg.ValidateWithdrawals {
		return
	}

	// Fetch actual withdrawals from the block
	actualWithdrawals, err := s.clClient.GetBlockWithdrawals(ctx, blockRoot)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Warn("Failed to fetch block withdrawals for validation")
		return
	}

	// Calculate expected withdrawals from parent state
	expectedWithdrawals, err := s.clClient.GetExpectedWithdrawals(parentBlockRoot, s.chainSpec.SlotsPerEpoch)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Warn("Failed to calculate expected withdrawals")
		return
	}

	// Compare
	s.compareWithdrawals(slot, expectedWithdrawals, actualWithdrawals, "block")
}

// validatePayloadEnvelopeWithdrawals validates that the withdrawals in a payload envelope match expected.
// This is called for Gloas where the execution payload is in a separate envelope.
// parentBlockRoot is the root of the block whose state we use to calculate expected withdrawals.
func (s *Service) validatePayloadEnvelopeWithdrawals(
	ctx context.Context,
	blockRoot phase0.Root,
	parentBlockRoot phase0.Root,
	slot phase0.Slot,
) {
	if !s.cfg.ValidateWithdrawals {
		return
	}

	// Fetch actual withdrawals from the payload envelope
	actualWithdrawals, err := s.clClient.GetPayloadEnvelopeWithdrawals(ctx, blockRoot)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Warn("Failed to fetch payload envelope withdrawals for validation")
		return
	}

	// Calculate expected withdrawals from parent state
	expectedWithdrawals, err := s.clClient.GetExpectedWithdrawals(parentBlockRoot, s.chainSpec.SlotsPerEpoch)
	if err != nil {
		s.log.WithError(err).WithField("slot", slot).Warn("Failed to calculate expected withdrawals")
		return
	}

	// Compare
	s.compareWithdrawals(slot, expectedWithdrawals, actualWithdrawals, "payload_envelope")
}

// compareWithdrawals compares expected and actual withdrawals and logs errors on mismatch.
func (s *Service) compareWithdrawals(
	slot phase0.Slot,
	expected []*capella.Withdrawal,
	actual []*capella.Withdrawal,
	source string,
) {
	if len(expected) != len(actual) {
		s.log.WithFields(logrus.Fields{
			"slot":           slot,
			"source":         source,
			"expected_count": len(expected),
			"actual_count":   len(actual),
		}).Error("Withdrawal count mismatch!")
		s.logWithdrawalDetails(expected, actual)
		return
	}

	for i := range expected {
		if !withdrawalsEqual(expected[i], actual[i]) {
			s.log.WithFields(logrus.Fields{
				"slot":               slot,
				"source":             source,
				"withdrawal_index":   i,
				"expected_index":     expected[i].Index,
				"expected_validator": expected[i].ValidatorIndex,
				"expected_address":   fmt.Sprintf("%x", expected[i].Address),
				"expected_amount":    expected[i].Amount,
				"actual_index":       actual[i].Index,
				"actual_validator":   actual[i].ValidatorIndex,
				"actual_address":     fmt.Sprintf("%x", actual[i].Address),
				"actual_amount":      actual[i].Amount,
			}).Error("Withdrawal mismatch at index!")
			return
		}
	}

	s.log.WithFields(logrus.Fields{
		"slot":             slot,
		"source":           source,
		"withdrawal_count": len(actual),
	}).Debug("Withdrawal validation passed")
}

// logWithdrawalDetails logs detailed information about expected and actual withdrawals.
func (s *Service) logWithdrawalDetails(expected, actual []*capella.Withdrawal) {
	s.log.WithField("expected_withdrawals", formatWithdrawals(expected)).Debug("Expected withdrawals")
	s.log.WithField("actual_withdrawals", formatWithdrawals(actual)).Debug("Actual withdrawals")
}

// withdrawalsEqual checks if two withdrawals are equal.
func withdrawalsEqual(a, b *capella.Withdrawal) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Index == b.Index &&
		a.ValidatorIndex == b.ValidatorIndex &&
		a.Address == b.Address &&
		a.Amount == b.Amount
}

// formatWithdrawals formats a slice of withdrawals for logging.
func formatWithdrawals(withdrawals []*capella.Withdrawal) string {
	if len(withdrawals) == 0 {
		return "[]"
	}

	var sb strings.Builder

	sb.WriteString("[")

	for i, w := range withdrawals {
		if i > 0 {
			sb.WriteString(", ")
		}

		sb.WriteString(fmt.Sprintf("{idx:%d val:%d addr:%x amt:%d}",
			w.Index, w.ValidatorIndex, w.Address[:4], w.Amount))
	}

	sb.WriteString("]")

	return sb.String()
}
