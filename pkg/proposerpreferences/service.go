package proposerpreferences

import (
	"context"
	"fmt"
	"sync"

	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// Service listens to proposer preferences from the beacon node's SSE event stream
// and caches the latest preference per slot.
type Service struct {
	clClient *beacon.Client
	cache    *Cache
	log      logrus.FieldLogger

	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
}

// NewService creates a new proposer preferences service.
func NewService(clClient *beacon.Client, log logrus.FieldLogger) *Service {
	return &Service{
		clClient: clClient,
		cache:    NewCache(),
		log:      log.WithField("component", "proposer-preferences"),
	}
}

// Start subscribes to the proposer_preferences SSE topic and begins processing events.
func (s *Service) Start(ctx context.Context) error {
	sub := s.clClient.Events().SubscribeProposerPreferences()

	svcCtx, cancel := context.WithCancel(ctx)
	s.cancelFunc = cancel

	s.wg.Add(1)

	go s.processEvents(svcCtx, sub)

	s.log.Info("Proposer preferences service started")

	return nil
}

// Stop stops the service.
func (s *Service) Stop() {
	if s.cancelFunc != nil {
		s.cancelFunc()
	}

	s.wg.Wait()
	s.log.Info("Proposer preferences service stopped")
}

// GetPreferences returns the cached proposer preferences for a given slot.
func (s *Service) GetPreferences(slot phase0.Slot) (*gloas.SignedProposerPreferences, bool) {
	return s.cache.Get(slot)
}

// GetCache returns the underlying cache for direct access.
func (s *Service) GetCache() *Cache {
	return s.cache
}

// processEvents reads SSE proposer preference events and caches them.
func (s *Service) processEvents(ctx context.Context, sub interface {
	Channel() <-chan *gloas.SignedProposerPreferences
	Unsubscribe()
}) {
	defer s.wg.Done()
	defer sub.Unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return
		case signed, ok := <-sub.Channel():
			if !ok {
				return
			}

			s.handleEvent(signed)
		}
	}
}

// handleEvent caches a received proposer preferences event.
func (s *Service) handleEvent(signed *gloas.SignedProposerPreferences) {
	if signed == nil || signed.Message == nil {
		return
	}

	slot := signed.Message.ProposalSlot
	validatorIndex := signed.Message.ValidatorIndex

	log := s.log.WithFields(logrus.Fields{
		"slot":            slot,
		"validator_index": validatorIndex,
		"fee_recipient":   fmt.Sprintf("0x%x", signed.Message.FeeRecipient[:]),
		"gas_limit":       signed.Message.GasLimit,
	})

	log.Info("Received proposer preferences from SSE")

	if s.cache.Has(slot) {
		log.Debug("Already have proposer preferences for this slot, ignoring")
		return
	}

	if s.cache.Add(slot, signed) {
		log.Info("Cached proposer preferences")
	}
}
