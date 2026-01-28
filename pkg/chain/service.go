package chain

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// Service interface defines the chain service operations.
type Service interface {
	Start(ctx context.Context) error
	Stop() error

	// Chain state accessors
	GetChainSpec() *beacon.ChainSpec
	GetGenesis() *beacon.Genesis
	SlotToTime(slot phase0.Slot) time.Time
	TimeToSlot(t time.Time) phase0.Slot
	GetCurrentEpoch(ctx context.Context) (phase0.Epoch, error)
	GetForkVersion(ctx context.Context) (phase0.Version, error)
	IsGloas() bool

	// Epoch stats access
	GetCurrentEpochStats() *EpochStats
	GetEpochStats(epoch phase0.Epoch) *EpochStats

	// Subscriptions
	SubscribeEpochStats() *utils.Subscription[*EpochStats]

	// Builder access
	GetBuilderByIndex(index uint64) *BuilderInfo
	GetBuilderByPubkey(pubkey phase0.BLSPubKey) *BuilderInfo
	GetBuilders() []*BuilderInfo
	HasBuildersLoaded() bool

	// RefreshBuilders re-fetches the beacon state to pick up new builder registrations.
	RefreshBuilders(ctx context.Context) error
}

// Ensure implementation satisfies interface.
var _ Service = (*service)(nil)

// service is the implementation of Service.
type service struct {
	clClient  *beacon.Client
	chainSpec *beacon.ChainSpec
	genesis   *beacon.Genesis
	log       logrus.FieldLogger

	// State cache (keeps latest 2 epochs)
	stateCache     map[phase0.Epoch]*EpochStats
	currentEpoch   phase0.Epoch
	buildersLoaded bool
	cacheMu        sync.RWMutex

	// Event dispatching
	epochStatsDispatcher *utils.Dispatcher[*EpochStats]

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewService creates a new chain service.
func NewService(
	clClient *beacon.Client,
	chainSpec *beacon.ChainSpec,
	genesis *beacon.Genesis,
	log logrus.FieldLogger,
) Service {
	return &service{
		clClient:             clClient,
		chainSpec:            chainSpec,
		genesis:              genesis,
		log:                  log.WithField("component", "chain-service"),
		stateCache:           make(map[phase0.Epoch]*EpochStats, 2),
		epochStatsDispatcher: &utils.Dispatcher[*EpochStats]{},
	}
}

// Start starts the chain service and begins listening to head events for epoch transitions.
func (s *service) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	// Fetch initial state
	if err := s.fetchCurrentEpochState(ctx); err != nil {
		return fmt.Errorf("failed to fetch initial state: %w", err)
	}

	// Subscribe to head events to detect epoch transitions
	s.wg.Add(1)
	go s.runEpochMonitor()

	s.log.Info("Chain service started")

	return nil
}

// Stop stops the chain service.
func (s *service) Stop() error {
	s.log.Info("Stopping chain service")

	if s.cancel != nil {
		s.cancel()
	}

	s.wg.Wait()

	s.log.Info("Chain service stopped")

	return nil
}

// GetCurrentEpochStats returns the stats for the current epoch.
func (s *service) GetCurrentEpochStats() *EpochStats {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	return s.stateCache[s.currentEpoch]
}

// GetEpochStats returns the stats for a specific epoch if cached.
func (s *service) GetEpochStats(epoch phase0.Epoch) *EpochStats {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	return s.stateCache[epoch]
}

// SubscribeEpochStats returns a subscription for epoch stats updates.
func (s *service) SubscribeEpochStats() *utils.Subscription[*EpochStats] {
	return s.epochStatsDispatcher.Subscribe(4, false)
}

// GetBuilderByIndex returns builder info by index from the current epoch stats.
func (s *service) GetBuilderByIndex(index uint64) *BuilderInfo {
	stats := s.GetCurrentEpochStats()
	if stats == nil || !stats.BuildersLoaded {
		return nil
	}

	if index >= uint64(len(stats.Builders)) {
		return nil
	}

	return stats.Builders[index]
}

// GetBuilderByPubkey returns builder info by public key from the current epoch stats.
func (s *service) GetBuilderByPubkey(pubkey phase0.BLSPubKey) *BuilderInfo {
	stats := s.GetCurrentEpochStats()
	if stats == nil || !stats.BuildersLoaded {
		return nil
	}

	for _, builder := range stats.Builders {
		if builder.Pubkey == pubkey {
			return builder
		}
	}

	return nil
}

// GetBuilders returns all builders from the current epoch stats.
func (s *service) GetBuilders() []*BuilderInfo {
	stats := s.GetCurrentEpochStats()
	if stats == nil {
		return nil
	}

	return stats.Builders
}

// HasBuildersLoaded returns whether builders have been loaded.
func (s *service) HasBuildersLoaded() bool {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	return s.buildersLoaded
}

// GetChainSpec returns the chain specification.
func (s *service) GetChainSpec() *beacon.ChainSpec {
	return s.chainSpec
}

// GetGenesis returns the genesis information.
func (s *service) GetGenesis() *beacon.Genesis {
	return s.genesis
}

// SlotToTime converts a slot number to a timestamp.
func (s *service) SlotToTime(slot phase0.Slot) time.Time {
	return SlotToTime(s.genesis, s.chainSpec, slot)
}

// TimeToSlot converts a timestamp to a slot number.
func (s *service) TimeToSlot(t time.Time) phase0.Slot {
	return TimeToSlot(s.genesis, s.chainSpec, t)
}

// GetCurrentEpoch calculates the current epoch from the head slot.
func (s *service) GetCurrentEpoch(ctx context.Context) (phase0.Epoch, error) {
	slot, err := s.clClient.GetHeadSlot(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get head slot: %w", err)
	}

	return phase0.Epoch(uint64(slot) / s.chainSpec.SlotsPerEpoch), nil
}

// GetForkVersion returns the current fork version from the beacon node.
func (s *service) GetForkVersion(ctx context.Context) (phase0.Version, error) {
	return s.clClient.GetForkVersion(ctx)
}

// IsGloas returns whether the chain is running the Gloas fork.
func (s *service) IsGloas() bool {
	stats := s.GetCurrentEpochStats()
	if stats == nil {
		return false
	}

	return stats.IsGloas
}

// RefreshBuilders re-fetches the head state to pick up new builder registrations.
func (s *service) RefreshBuilders(ctx context.Context) error {
	s.log.Debug("Refreshing builders from head state")

	stats, err := s.fetchEpochStats(ctx, "head", s.currentEpoch)
	if err != nil {
		return fmt.Errorf("failed to refresh builders: %w", err)
	}

	s.cacheMu.Lock()
	s.stateCache[s.currentEpoch] = stats
	if stats.BuildersLoaded {
		s.buildersLoaded = true
	}
	s.cacheMu.Unlock()

	return nil
}

// runEpochMonitor monitors head events to detect epoch transitions.
// It fetches new state when the first block of a new epoch is received.
func (s *service) runEpochMonitor() {
	defer s.wg.Done()

	headSub := s.clClient.Events().SubscribeHead()
	defer headSub.Unsubscribe()

	for {
		select {
		case <-s.ctx.Done():
			return

		case event := <-headSub.Channel():
			s.handleHeadEvent(event)
		}
	}
}

// handleHeadEvent checks if a head event represents a new epoch and fetches state.
func (s *service) handleHeadEvent(event *beacon.HeadEvent) {
	newEpoch := phase0.Epoch(uint64(event.Slot) / s.chainSpec.SlotsPerEpoch)

	s.cacheMu.RLock()
	currentEpoch := s.currentEpoch
	_, alreadyCached := s.stateCache[newEpoch]
	s.cacheMu.RUnlock()

	// Only fetch state when we enter a new epoch that hasn't been cached yet
	if newEpoch <= currentEpoch || alreadyCached {
		return
	}

	s.log.WithFields(logrus.Fields{
		"slot":  event.Slot,
		"epoch": newEpoch,
	}).Info("New epoch detected, fetching state from head block")

	// Use the head event's slot as state ID - this block is guaranteed to exist
	stateID := fmt.Sprintf("%d", event.Slot)
	if err := s.fetchAndCacheEpochState(s.ctx, stateID, newEpoch); err != nil {
		s.log.WithError(err).Error("Failed to fetch epoch state")
		return
	}

	// Update current epoch and evict old states
	s.cacheMu.Lock()
	s.currentEpoch = newEpoch

	// Keep only last 2 epochs
	for epoch := range s.stateCache {
		if epoch < newEpoch-1 {
			delete(s.stateCache, epoch)
		}
	}
	s.cacheMu.Unlock()

	// Fire epoch stats event
	stats := s.GetEpochStats(newEpoch)
	if stats != nil {
		s.epochStatsDispatcher.Fire(stats)
	}
}

// fetchCurrentEpochState fetches the state for the current epoch at startup.
func (s *service) fetchCurrentEpochState(ctx context.Context) error {
	// Get current slot to determine epoch
	headSlot, err := s.clClient.GetHeadSlot(ctx)
	if err != nil {
		return fmt.Errorf("failed to get head slot: %w", err)
	}

	epoch := phase0.Epoch(uint64(headSlot) / s.chainSpec.SlotsPerEpoch)

	s.log.WithFields(logrus.Fields{
		"head_slot": headSlot,
		"epoch":     epoch,
	}).Info("Fetching initial epoch state")

	// Use "head" as state ID at startup - the head block always exists
	if err := s.fetchAndCacheEpochState(ctx, "head", epoch); err != nil {
		return err
	}

	s.cacheMu.Lock()
	s.currentEpoch = epoch
	s.cacheMu.Unlock()

	return nil
}

// fetchAndCacheEpochState fetches the beacon state and caches it for the given epoch.
// stateID should refer to an existing block (e.g., "head" or a slot number of a proposed block).
func (s *service) fetchAndCacheEpochState(ctx context.Context, stateID string, epoch phase0.Epoch) error {
	s.log.WithFields(logrus.Fields{
		"epoch":    epoch,
		"state_id": stateID,
	}).Debug("Fetching beacon state")

	stats, err := s.fetchEpochStats(ctx, stateID, epoch)
	if err != nil {
		return fmt.Errorf("failed to fetch epoch stats for epoch %d: %w", epoch, err)
	}

	s.cacheMu.Lock()
	s.stateCache[epoch] = stats
	if stats.BuildersLoaded {
		s.buildersLoaded = true
	}
	s.cacheMu.Unlock()

	s.log.WithFields(logrus.Fields{
		"epoch":             epoch,
		"validators":        stats.ValidatorCount,
		"active_validators": stats.ActiveValidators,
		"builders":          len(stats.Builders),
	}).Info("Epoch state cached")

	return nil
}
