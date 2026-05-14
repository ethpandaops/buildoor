package spamoor

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/config"
)

// Service owns the libp2p stack used to gossip ePBS execution_payload_bid
// messages directly to consensus-layer peers, bypassing the beacon node's
// HTTP submission endpoint. Implements epbs.BidSubmitter.
type Service struct {
	cfg *config.SpamoorConfig
	log logrus.FieldLogger

	forkDigest   [4]byte
	gvr          phase0.Root
	genesisTime  time.Time
	slotDuration time.Duration

	privKey *ecdsa.PrivateKey
	host    host.Host
	store   *peerStore
	peerMgr *peerManager
	discv5  *discover.UDPv5
	ps      *pubsub.PubSub
	topic   *pubsub.Topic
	sub     *pubsub.Subscription

	submitter *gossipSubmitter

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewService constructs the spamoor service. forkVersion is the current fork
// version (Gloas) used to compute the fork digest along with gvr.
func NewService(
	cfg *config.SpamoorConfig,
	forkVersion phase0.Version,
	gvr phase0.Root,
	genesisTime time.Time,
	slotDuration time.Duration,
	log logrus.FieldLogger,
) (*Service, error) {
	if cfg == nil {
		return nil, errors.New("spamoor: config is nil")
	}

	digest, err := computeForkDigest(forkVersion, gvr)
	if err != nil {
		return nil, fmt.Errorf("compute fork digest: %w", err)
	}

	return &Service{
		cfg:          cfg,
		log:          log.WithField("component", "spamoor"),
		forkDigest:   digest,
		gvr:          gvr,
		genesisTime:  genesisTime,
		slotDuration: slotDuration,
		store:        newPeerStore(),
	}, nil
}

// Start brings up the libp2p host, RPC handlers, discovery, peer manager,
// and gossipsub bid topic. After Start, Submit can be called.
func (s *Service) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	privKey, err := loadOrGenerateKey(s.cfg.P2PPrivKey)
	if err != nil {
		return fmt.Errorf("p2p key: %w", err)
	}

	s.privKey = privKey

	h, err := newHost(s.cfg.TCPPort, s.cfg.QUICPort, privKey)
	if err != nil {
		return fmt.Errorf("create libp2p host: %w", err)
	}

	s.host = h

	s.log.WithFields(logrus.Fields{
		"peer_id":     h.ID().String(),
		"fork_digest": fmt.Sprintf("0x%x", s.forkDigest[:]),
		"tcp_port":    s.cfg.TCPPort,
		"quic_port":   s.cfg.QUICPort,
		"disc_port":   s.cfg.DiscPort,
		"gossip_d":    s.cfg.GossipD,
	}).Info("spamoor libp2p host started")

	// RPC handlers + outbound status on every new connection.
	statusFn := makeStatusProvider(s.forkDigest, s.genesisTime, s.slotDuration)
	registerRPCHandlers(h, statusFn, make([]byte, 8), s.log)
	sendStatusOnConnect(h, statusFn, s.log)

	// Static peers are added directly to the dial pool.
	staticPeers, err := parseStaticPeers(s.cfg.StaticPeers)
	if err != nil {
		return fmt.Errorf("static peers: %w", err)
	}

	for _, ai := range staticPeers {
		s.store.add(ai)
	}

	if len(staticPeers) > 0 {
		s.log.WithField("count", len(staticPeers)).Info("loaded static peers")
	}

	// Optional discv5 — only spin up if the user provided bootnodes.
	bootnodes, err := parseBootnodes(s.cfg.Bootnodes)
	if err != nil {
		return fmt.Errorf("bootnodes: %w", err)
	}

	if len(bootnodes) > 0 {
		listener, err := startDiscovery(s.ctx, discoveryConfig{
			PrivKey:    privKey,
			DiscPort:   s.cfg.DiscPort,
			ForkDigest: s.forkDigest,
			Bootnodes:  bootnodes,
			Store:      s.store,
			Log:        s.log,
		})
		if err != nil {
			return fmt.Errorf("start discovery: %w", err)
		}

		s.discv5 = listener
	}

	// Peer manager dial loop.
	s.peerMgr = newPeerManager(h, s.store, s.cfg.MaxPeers, s.log)

	s.wg.Add(1)

	go func() {
		defer s.wg.Done()
		s.peerMgr.run(s.ctx)
	}()

	// Gossipsub + bid topic.
	ps, err := newGossipSub(s.ctx, h, s.gvr[:], s.cfg.GossipD)
	if err != nil {
		return fmt.Errorf("init gossipsub: %w", err)
	}

	s.ps = ps

	topic, sub, err := joinBidTopic(ps, s.forkDigest)
	if err != nil {
		return fmt.Errorf("join bid topic: %w", err)
	}

	s.topic = topic
	s.sub = sub
	s.submitter = newGossipSubmitter(topic)

	// Drain incoming bid messages so libp2p-pubsub doesn't backpressure us.
	// We only publish; we don't validate or use received bids.
	s.wg.Add(1)

	go s.drainSubscription()

	s.log.WithField("topic", executionPayloadBidTopic(s.forkDigest)).Info("spamoor joined bid topic")

	return nil
}

// drainSubscription reads (and discards) inbound messages on the bid topic.
// Required because the pubsub subscription has a bounded channel.
func (s *Service) drainSubscription() {
	defer s.wg.Done()

	for {
		_, err := s.sub.Next(s.ctx)
		if err != nil {
			return
		}
	}
}

// Submit publishes a signed bid to the gossipsub mesh. Implements epbs.BidSubmitter.
func (s *Service) Submit(ctx context.Context, signedBid *gloas.SignedExecutionPayloadBid) error {
	if s.submitter == nil {
		return errors.New("spamoor: service not started")
	}

	return s.submitter.Submit(ctx, signedBid)
}

// Stop tears down the libp2p stack.
func (s *Service) Stop() {
	s.log.Info("stopping spamoor service")

	if s.cancel != nil {
		s.cancel()
	}

	if s.sub != nil {
		s.sub.Cancel()
	}

	if s.topic != nil {
		_ = s.topic.Close()
	}

	if s.discv5 != nil {
		s.discv5.Close()
	}

	if s.host != nil {
		_ = s.host.Close()
	}

	s.wg.Wait()

	s.log.Info("spamoor service stopped")
}
