package chain

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/proposerpreferences"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// Service interface defines the chain service operations.
type Service interface {
	Start(ctx context.Context) error
	Stop() error

	// Chain state accessors
	GetChainSpec() *ChainSpec
	GetGenesis() *beacon.Genesis
	SlotToTime(slot phase0.Slot) time.Time
	TimeToSlot(t time.Time) phase0.Slot
	GetCurrentEpoch() phase0.Epoch
	GetCurrentSlot() phase0.Slot
	GetCurrentFork() version.DataVersion
	ActiveForkAtEpoch(epoch phase0.Epoch) version.DataVersion
	GetForkVersion() (phase0.Version, error)
	GetEpochOfSlot(slot phase0.Slot) phase0.Epoch

	// Epoch stats access
	GetCurrentEpochStats() *EpochStats
	GetEpochStats(epoch phase0.Epoch) *EpochStats

	// Subscriptions
	SubscribeEpochStats() *utils.Subscription[*EpochStats]

	// Head vote tracking
	GetHeadVoteTracker() *HeadVoteTracker

	// Finality
	GetFinalizedEpoch() phase0.Epoch

	// Builder access
	GetBuilderByIndex(index uint64) *BuilderInfo
	GetBuilderByPubkey(pubkey phase0.BLSPubKey) *BuilderInfo
	GetBuilders() []*BuilderInfo

	// Validator access
	GetValidatorPubkeyByIndex(index phase0.ValidatorIndex) *phase0.BLSPubKey

	// RefreshBuilders re-fetches the beacon state to pick up new builder registrations.
	RefreshBuilders(ctx context.Context) error

	// SetProposerPreferencesCache registers the proposer preferences cache so
	// it can be pruned on each epoch transition. Pass nil to disable pruning.
	SetProposerPreferencesCache(cache *proposerpreferences.Cache)
}

// Ensure implementation satisfies interface.
var _ Service = (*service)(nil)

// service is the implementation of Service.
type service struct {
	clClient  *beacon.Client
	chainSpec *ChainSpec
	genesis   *beacon.Genesis
	log       logrus.FieldLogger

	// State cache (keeps latest 2 epochs)
	stateCache          map[phase0.Epoch]*EpochStats
	validatorIndexCache []phase0.BLSPubKey
	currentEpoch        phase0.Epoch
	cacheMu             sync.RWMutex

	// Head vote tracking
	headVoteTracker *HeadVoteTracker

	// Event dispatching
	epochStatsDispatcher *utils.Dispatcher[*EpochStats]

	// Proposer preferences cache, pruned on epoch transitions. Optional.
	propPrefCache *proposerpreferences.Cache

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewService creates a new chain service.
func NewService(
	clClient *beacon.Client,
	chainSpec *ChainSpec,
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

	// Start head vote tracker
	s.headVoteTracker = NewHeadVoteTracker(s, s.clClient, s.log)
	s.headVoteTracker.Start(s.ctx)

	// Subscribe to head events to detect epoch transitions
	s.wg.Add(1)
	go s.runEpochMonitor()

	s.log.Info("Chain service started")

	return nil
}

// Stop stops the chain service.
func (s *service) Stop() error {
	s.log.Info("Stopping chain service")

	if s.headVoteTracker != nil {
		s.headVoteTracker.Stop()
	}

	if s.cancel != nil {
		s.cancel()
	}

	s.wg.Wait()

	s.log.Info("Chain service stopped")

	return nil
}

// GetFinalizedEpoch returns the finalized epoch from the current epoch's cached state.
func (s *service) GetFinalizedEpoch() phase0.Epoch {
	stats := s.GetCurrentEpochStats()
	if stats == nil {
		return 0
	}

	return stats.FinalizedEpoch
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

// GetCurrentFork returns the current fork version.
func (s *service) GetCurrentFork() version.DataVersion {
	return s.ActiveForkAtEpoch(s.GetCurrentEpoch())
}

// SubscribeEpochStats returns a subscription for epoch stats updates.
func (s *service) SubscribeEpochStats() *utils.Subscription[*EpochStats] {
	return s.epochStatsDispatcher.Subscribe(4, false)
}

// GetBuilderByIndex returns builder info by index from the current epoch stats.
func (s *service) GetBuilderByIndex(index uint64) *BuilderInfo {
	stats := s.GetCurrentEpochStats()
	if stats == nil {
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
	if stats == nil {
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

// GetValidatorPubkeyByIndex returns the public key for a validator by index from the current epoch stats.
func (s *service) GetValidatorPubkeyByIndex(index phase0.ValidatorIndex) *phase0.BLSPubKey {
	if index >= phase0.ValidatorIndex(len(s.validatorIndexCache)) {
		return nil
	}

	return &s.validatorIndexCache[index]
}

// GetChainSpec returns the chain specification.
func (s *service) GetChainSpec() *ChainSpec {
	return s.chainSpec
}

// GetGenesis returns the genesis information.
func (s *service) GetGenesis() *beacon.Genesis {
	return s.genesis
}

// SlotToTime converts a slot number to a timestamp.
func (s *service) SlotToTime(slot phase0.Slot) time.Time {
	slotDuration := time.Duration(uint64(slot)) * s.chainSpec.SecondsPerSlot
	return s.genesis.GenesisTime.Add(slotDuration)
}

// TimeToSlot converts a timestamp to a slot number.
func (s *service) TimeToSlot(t time.Time) phase0.Slot {
	if t.Before(s.genesis.GenesisTime) {
		return 0
	}

	elapsed := t.Sub(s.genesis.GenesisTime)

	return phase0.Slot(elapsed / s.chainSpec.SecondsPerSlot)
}

// GetCurrentEpoch calculates the current epoch from the head slot.
func (s *service) GetCurrentEpoch() phase0.Epoch {
	currentSlot := s.TimeToSlot(time.Now())
	return phase0.Epoch(uint64(currentSlot) / s.chainSpec.SlotsPerEpoch)
}

// GetCurrentSlot calculates the current slot from the current time.
func (s *service) GetCurrentSlot() phase0.Slot {
	currentSlot := s.TimeToSlot(time.Now())
	return currentSlot
}

// GetEpochOfSlot calculates the epoch of a given slot.
func (s *service) GetEpochOfSlot(slot phase0.Slot) phase0.Epoch {
	return phase0.Epoch(uint64(slot) / s.chainSpec.SlotsPerEpoch)
}

// GetForkVersion returns the current fork version from the beacon node.
func (s *service) GetForkVersion() (phase0.Version, error) {
	return s.chainSpec.GetForkVersion(s.GetCurrentFork())
}

// GetHeadVoteTracker returns the head vote tracker.
func (s *service) GetHeadVoteTracker() *HeadVoteTracker {
	return s.headVoteTracker
}

// SetProposerPreferencesCache registers the proposer preferences cache so it
// can be pruned on each epoch transition.
func (s *service) SetProposerPreferencesCache(cache *proposerpreferences.Cache) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	s.propPrefCache = cache
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

	propPrefCache := s.propPrefCache
	s.cacheMu.Unlock()

	// Drop any proposer preferences for slots that are now in the past.
	if propPrefCache != nil {
		newEpochStartSlot := phase0.Slot(uint64(newEpoch) * s.chainSpec.SlotsPerEpoch)
		propPrefCache.PruneBefore(newEpochStartSlot)
	}

	// Fire epoch stats event
	stats := s.GetEpochStats(newEpoch)
	if stats != nil {
		s.epochStatsDispatcher.Fire(stats)
	}
}

// fetchCurrentEpochState fetches the state for the current epoch at startup.
func (s *service) fetchCurrentEpochState(ctx context.Context) error {
	// Get current slot to determine epoch
	currentSlot := s.GetCurrentSlot()
	epoch := phase0.Epoch(uint64(currentSlot) / s.chainSpec.SlotsPerEpoch)

	s.log.WithFields(logrus.Fields{
		"slot":  currentSlot,
		"epoch": epoch,
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
	s.cacheMu.Unlock()

	s.log.WithFields(logrus.Fields{
		"epoch":             epoch,
		"validators":        stats.ValidatorCount,
		"active_validators": stats.ActiveValidators,
		"builders":          len(stats.Builders),
	}).Info("Epoch state cached")

	return nil
}
