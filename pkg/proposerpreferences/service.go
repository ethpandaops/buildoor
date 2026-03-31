package proposerpreferences

import (
	"context"
	"fmt"
	"sync"

	"github.com/attestantio/go-eth2-client/spec/gloas"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/p2p"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// DomainProposerPreferences is the domain type for proposer preferences signatures.
// See: https://github.com/ethereum/consensus-specs/tree/master/specs/gloas
var DomainProposerPreferences = phase0.DomainType{0x0d, 0x00, 0x00, 0x00}

// GossipTopicName is the gossip message name for proposer preferences.
const GossipTopicName = "proposer_preferences"

// Service listens to proposer preferences from the P2P gossip network,
// validates signatures, and caches the latest preference per slot.
type Service struct {
	p2pHost        *p2p.Host
	chainSvc       chain.Service
	validatorCache *chain.ValidatorIndexCache
	clClient       *beacon.Client
	cache          *Cache
	log            logrus.FieldLogger

	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
}

// NewService creates a new proposer preferences service.
func NewService(
	p2pHost *p2p.Host,
	chainSvc chain.Service,
	validatorCache *chain.ValidatorIndexCache,
	clClient *beacon.Client,
	log logrus.FieldLogger,
) *Service {
	return &Service{
		p2pHost:        p2pHost,
		chainSvc:       chainSvc,
		validatorCache: validatorCache,
		clClient:       clClient,
		cache:          NewCache(),
		log:            log.WithField("component", "proposer-preferences"),
	}
}

// Start subscribes to the proposer_preferences gossip topic and begins processing messages.
func (s *Service) Start(ctx context.Context) error {
	genesis := s.chainSvc.GetGenesis()
	if genesis == nil {
		return fmt.Errorf("genesis not available")
	}

	chainSpec := s.chainSvc.GetChainSpec()
	if chainSpec == nil {
		return fmt.Errorf("chain spec not available")
	}

	// Fetch the current active fork version from the beacon node.
	// Gossip topic fork digests must match the current fork, not a hardcoded future fork version.
	currentForkVersion, err := s.clClient.GetCurrentForkVersion(ctx)
	if err != nil {
		return fmt.Errorf("failed to get current fork version: %w", err)
	}

	s.log.WithFields(logrus.Fields{
		"current_fork_version":    fmt.Sprintf("0x%x", currentForkVersion[:]),
		"genesis_validators_root": fmt.Sprintf("0x%x", genesis.GenesisValidatorsRoot[:]),
	}).Info("Computing fork digest for proposer preferences topic")

	// Compute fork digest using the current fork version with all BPO XOR entries applied.
	// All entries from BLOB_SCHEDULE are applied unconditionally (Prysm includes the full
	// schedule in the digest regardless of activation epoch).
	forkDigest, err := p2p.ComputeForkDigestWithBPO(currentForkVersion, genesis.GenesisValidatorsRoot, chainSpec)
	if err != nil {
		return fmt.Errorf("failed to compute fork digest: %w", err)
	}

	// Build the full topic name: /eth2/{digest}/proposer_preferences/ssz_snappy
	topicName := p2p.BuildTopicName(forkDigest, GossipTopicName)

	s.log.WithFields(logrus.Fields{
		"topic":                topicName,
		"fork_digest":          fmt.Sprintf("%x", forkDigest),
		"current_fork_version": fmt.Sprintf("0x%x", currentForkVersion[:]),
	}).Info("Subscribing to proposer preferences gossip topic")

	sub, err := s.p2pHost.Subscribe(topicName)
	if err != nil {
		return fmt.Errorf("failed to subscribe to proposer preferences topic: %w", err)
	}

	// Precompute the domain for signature verification.
	// Proposer preferences is a Gloas feature, so the signing domain uses the Gloas fork version.
	if chainSpec.GloasForkVersion == nil {
		return fmt.Errorf("gloas fork version not available in chain spec")
	}
	domain := signer.ComputeDomain(DomainProposerPreferences, *chainSpec.GloasForkVersion, genesis.GenesisValidatorsRoot)

	svcCtx, cancel := context.WithCancel(ctx)
	s.cancelFunc = cancel

	s.wg.Add(1)

	go s.processMessages(svcCtx, sub, domain)

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

// processMessages reads gossip messages, decodes, validates signatures, and caches them.
func (s *Service) processMessages(ctx context.Context, sub *pubsub.Subscription, domain phase0.Domain) {
	defer s.wg.Done()

	for {
		msg, err := sub.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			s.log.WithError(err).Warn("Error receiving gossip message")

			continue
		}

		if msg == nil || msg.Data == nil {
			continue
		}

		s.handleMessage(msg.Data, domain)
	}
}

// handleMessage decodes, validates, and caches a single proposer preferences message.
func (s *Service) handleMessage(data []byte, domain phase0.Domain) {
	var signed gloas.SignedProposerPreferences
	if err := p2p.DecodeGossipMessage(data, &signed); err != nil {
		s.log.WithError(err).Debug("Failed to decode proposer preferences message")
		return
	}

	if signed.Message == nil {
		s.log.Debug("Received proposer preferences with nil message")
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

	log.Info("Received proposer preferences from gossip")

	// Skip if we already have preferences for this slot.
	if s.cache.Has(slot) {
		log.Debug("Already have proposer preferences for this slot, ignoring")
		return
	}

	// Look up the validator's public key.
	pubkey, ok := s.validatorCache.Get(validatorIndex)
	if !ok {
		log.Warn("Unknown validator index in proposer preferences, dropping")
		return
	}

	// Verify the BLS signature.
	if !VerifySignature(&signed, pubkey, domain) {
		log.Warn("Invalid signature on proposer preferences, dropping")
		return
	}

	// Cache the validated preferences.
	if s.cache.Add(slot, &signed) {
		log.WithFields(logrus.Fields{
			"fee_recipient": fmt.Sprintf("0x%x", signed.Message.FeeRecipient[:]),
			"gas_limit":     signed.Message.GasLimit,
		}).Info("Received valid proposer preferences")
	}
}

