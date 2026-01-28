package builder

import (
	"context"
	"fmt"
	"sync"
	"time"

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
//
// Building is triggered by payload_attributes events from the beacon node,
// which contain all the information needed to build a payload.
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

	// Build tracking
	scheduledBuildMu  sync.Mutex
	buildStartedSlots map[phase0.Slot]bool // Slots where building has started (to prevent re-building)
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
	payloadAttrSub := s.clClient.Events().SubscribePayloadAttributes()
	payloadSub := s.clClient.Events().SubscribePayloadEnvelope()

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
			s.handlePayloadEnvelopeEvent(event)
		}
	}
}

// handleHeadEvent processes a head event (new block received).
// With payload_attributes-based building, this is only used for logging.
// Building is triggered by payload_attributes events, not head events.
func (s *Service) handleHeadEvent(event *beacon.HeadEvent) {
	s.log.WithFields(logrus.Fields{
		"head_slot": event.Slot,
		"is_gloas":  s.isGloas,
	}).Debug("Head event received")

	// NOTE: We no longer trigger builds from head events.
	// Building is now triggered by payload_attributes events from the beacon node.
}

// handlePayloadAttributesEvent processes a payload_attributes event.
// This is the primary trigger for building payloads.
func (s *Service) handlePayloadAttributesEvent(event *beacon.PayloadAttributesEvent) {
	s.log.WithFields(logrus.Fields{
		"proposal_slot": event.ProposalSlot,
		"parent_hash":   fmt.Sprintf("%x", event.ParentBlockHash[:8]),
		"timestamp":     event.Timestamp,
		"withdrawals":   len(event.Withdrawals),
	}).Info("Payload attributes event received")

	// Check if we should build for this slot
	if !s.slotManager.ShouldBuildForSlot(event.ProposalSlot) {
		s.log.WithField("slot", event.ProposalSlot).Debug("Skipping slot per schedule")
		return
	}

	// Check if already building/built for this slot
	s.scheduledBuildMu.Lock()
	if s.buildStartedSlots[event.ProposalSlot] {
		s.scheduledBuildMu.Unlock()
		s.log.WithField("slot", event.ProposalSlot).Debug("Already building/built for this slot")
		return
	}
	s.buildStartedSlots[event.ProposalSlot] = true
	s.scheduledBuildMu.Unlock()

	// Build immediately (BuildStartTime is not used with payload_attributes)
	go s.executeBuildFromAttributes(event)
}

// executeBuildFromAttributes performs payload building using data from payload_attributes event.
func (s *Service) executeBuildFromAttributes(event *beacon.PayloadAttributesEvent) {
	s.log.WithFields(logrus.Fields{
		"slot":        event.ProposalSlot,
		"parent_hash": fmt.Sprintf("%x", event.ParentBlockHash[:8]),
	}).Info("Starting payload build from attributes")

	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()

	var payloadEvent *PayloadReadyEvent
	var err error

	if s.isGloas {
		// For Gloas, we still use the last known payload approach
		// TODO: May need special handling for Gloas with payload_attributes
		payloadEvent, err = s.payloadBuilder.BuildPayloadFromAttributes(ctx, event)
	} else {
		// Pre-Gloas (Electra/Fulu)
		payloadEvent, err = s.payloadBuilder.BuildPayloadFromAttributes(ctx, event)
	}

	if err != nil {
		s.log.WithError(err).WithField("slot", event.ProposalSlot).Error("Failed to build payload from attributes")
		return
	}

	s.emitPayloadReady(event.ProposalSlot, payloadEvent)
}

// handlePayloadEnvelopeEvent processes a payload envelope event (Gloas only).
// This is called when a payload is revealed for a block.
// With payload_attributes-based building, we only track the last known payload here.
func (s *Service) handlePayloadEnvelopeEvent(event *beacon.PayloadEnvelopeEvent) {
	s.log.WithFields(logrus.Fields{
		"slot":       event.Slot,
		"block_root": fmt.Sprintf("%x", event.BlockRoot[:8]),
		"block_hash": fmt.Sprintf("%x", event.BlockHash[:8]),
	}).Debug("Payload envelope event received")

	// Update last known payload (for Gloas tracking)
	s.lastKnownPayloadMu.Lock()
	s.lastKnownPayloadBlockRoot = event.BlockRoot
	s.lastKnownPayloadBlockHash = event.BlockHash
	s.lastKnownPayloadSlot = event.Slot
	s.lastKnownPayloadMu.Unlock()

	// NOTE: We no longer build from payload envelope events.
	// Building is now triggered by payload_attributes events.
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
