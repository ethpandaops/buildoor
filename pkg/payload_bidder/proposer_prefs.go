package payload_bidder

import (
	"context"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	gloasspec "github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// ProposerPreferencesService listens to the beacon node's proposer_preferences
// SSE topic, caches the first valid preference per slot, prunes old slots on
// epoch transitions, and resolves proposer settings for Gloas+ payload builds
// (it implements payload_builder.ProposerSettingsResolver).
type ProposerPreferencesService struct {
	clClient *beacon.Client
	chainSvc chain.Service
	store    *memstore.Store[phase0.Slot, *gloasspec.SignedProposerPreferences]

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	log    logrus.FieldLogger
}

var _ payload_builder.ProposerSettingsResolver = (*ProposerPreferencesService)(nil)

// NewProposerPreferencesService creates a new proposer preferences service.
// The backing store keeps the first valid preference per slot (subsequent
// preferences for the same slot are ignored).
func NewProposerPreferencesService(clClient *beacon.Client, chainSvc chain.Service,
	log logrus.FieldLogger) *ProposerPreferencesService {
	return &ProposerPreferencesService{
		clClient: clClient,
		chainSvc: chainSvc,
		store:    memstore.NewKeepExisting[phase0.Slot, *gloasspec.SignedProposerPreferences](),
		log:      log.WithField("component", "proposer-preferences"),
	}
}

// GetStore returns the underlying per-slot preference store for direct access
// (bid gating, bid construction, WebUI listing).
func (s *ProposerPreferencesService) GetStore() *memstore.Store[phase0.Slot, *gloasspec.SignedProposerPreferences] {
	return s.store
}

// Start subscribes to the proposer_preferences SSE topic and to epoch stats
// (for pruning) and begins processing events in the service's own loop.
func (s *ProposerPreferencesService) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	prefsSub := s.clClient.Events().SubscribeProposerPreferences()
	epochSub := s.chainSvc.SubscribeEpochStats()

	s.wg.Add(1)

	go s.run(prefsSub, epochSub)

	s.log.Info("Proposer preferences service started")

	return nil
}

// Stop stops the service and waits for the main loop to exit.
func (s *ProposerPreferencesService) Stop() {
	if s.cancel != nil {
		s.cancel()
	}

	s.wg.Wait()

	s.log.Info("Proposer preferences service stopped")
}

// run is the main loop: cache incoming preferences and prune past slots on
// epoch transitions.
func (s *ProposerPreferencesService) run(
	prefsSub *utils.Subscription[*gloasspec.SignedProposerPreferences],
	epochSub *utils.Subscription[*chain.EpochStats],
) {
	defer s.wg.Done()
	defer prefsSub.Unsubscribe()
	defer epochSub.Unsubscribe()

	for {
		select {
		case <-s.ctx.Done():
			return
		case signed, ok := <-prefsSub.Channel():
			if !ok {
				return
			}

			s.handleEvent(signed)
		case epochStats, ok := <-epochSub.Channel():
			if !ok {
				return
			}

			s.pruneForEpoch(epochStats.Epoch)
		}
	}
}

// handleEvent caches a received proposer preferences event. Only the first
// preference per slot is kept (the store's keep-existing policy).
func (s *ProposerPreferencesService) handleEvent(signed *gloasspec.SignedProposerPreferences) {
	if signed == nil || signed.Message == nil {
		return
	}

	log := s.log.WithFields(logrus.Fields{
		"slot":             signed.Message.ProposalSlot,
		"validator_index":  signed.Message.ValidatorIndex,
		"fee_recipient":    fmt.Sprintf("0x%x", signed.Message.FeeRecipient[:]),
		"target_gas_limit": signed.Message.TargetGasLimit,
	})

	log.Info("Received proposer preferences from SSE")

	if !s.store.Put(signed.Message.ProposalSlot, signed) {
		log.Debug("Already have proposer preferences for this slot, ignoring")
		return
	}

	log.Info("Cached proposer preferences")
}

// pruneForEpoch drops any proposer preferences for slots before the start of
// the given (new current) epoch — those slots are now in the past.
func (s *ProposerPreferencesService) pruneForEpoch(epoch phase0.Epoch) {
	epochStartSlot := phase0.Slot(uint64(epoch) * s.chainSvc.GetChainSpec().SlotsPerEpoch)

	pruned := s.store.Prune(func(slot phase0.Slot) bool {
		return slot < epochStartSlot
	})
	if pruned > 0 {
		s.log.WithFields(logrus.Fields{
			"epoch":  epoch,
			"pruned": pruned,
		}).Debug("Pruned past-slot proposer preferences")
	}
}

// ResolveProposerSettings resolves the proposer's announced settings for a
// build from the cached gossip preference. Self-scoped: it only applies from
// the Gloas fork onwards and returns false when no preference is cached for
// the slot.
func (s *ProposerPreferencesService) ResolveProposerSettings(slot phase0.Slot,
	_ phase0.ValidatorIndex) (payload_builder.ProposerSettings, bool) {
	if s.chainSvc.GetCurrentFork() < version.DataVersionGloas {
		return payload_builder.ProposerSettings{}, false
	}

	signed, ok := s.store.Get(slot)
	if !ok || signed == nil || signed.Message == nil {
		return payload_builder.ProposerSettings{}, false
	}

	return payload_builder.ProposerSettings{
		FeeRecipient:   common.Address(signed.Message.FeeRecipient),
		TargetGasLimit: signed.Message.TargetGasLimit,
	}, true
}
