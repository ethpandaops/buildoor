package p2p

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"

	"github.com/libp2p/go-libp2p"
	mplex "github.com/libp2p/go-libp2p-mplex"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptcp "github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"
	"github.com/sirupsen/logrus"
)

// HostConfig holds configuration for the P2P host.
type HostConfig struct {
	// ListenPort is the TCP port to listen on. Default: 9500.
	ListenPort uint

	// PeerAddrs are multiaddrs of peers to connect to (e.g. beacon node P2P endpoints).
	PeerAddrs []string
}

// Host is a lightweight libp2p host for subscribing to gossip topics.
type Host struct {
	cfg    HostConfig
	host   host.Host
	pubsub *pubsub.PubSub
	log    logrus.FieldLogger
}

// NewHost creates a new P2P host. Call Start() to initialize GossipSub and connect to peers.
func NewHost(cfg HostConfig, log logrus.FieldLogger) (*Host, error) {
	hostLog := log.WithField("component", "p2p")

	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate P2P key: %w", err)
	}

	privKey, _, err := crypto.ECDSAKeyPairFromKey(ecdsaKey)
	if err != nil {
		return nil, fmt.Errorf("failed to convert P2P key: %w", err)
	}

	listenPort := cfg.ListenPort
	if listenPort == 0 {
		listenPort = 9500
	}

	listenAddr := fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", listenPort)

	h, err := libp2p.New(
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.Transport(libp2ptcp.NewTCPTransport),
		libp2p.DefaultMuxers,
		libp2p.Muxer("/mplex/6.7.0", mplex.DefaultTransport),
		libp2p.Security(noise.ID, noise.New),
		libp2p.Ping(false),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create libp2p host: %w", err)
	}

	hostLog.WithField("peer_id", h.ID().String()).Info("Created P2P host")

	return &Host{
		cfg:  cfg,
		host: h,
		log:  hostLog,
	}, nil
}

// Start initializes GossipSub and connects to configured static peers.
func (h *Host) Start(ctx context.Context) error {
	ps, err := pubsub.NewGossipSub(ctx, h.host)
	if err != nil {
		return fmt.Errorf("failed to create GossipSub: %w", err)
	}

	h.pubsub = ps

	for _, addr := range h.cfg.PeerAddrs {
		ma, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			h.log.WithError(err).WithField("addr", addr).Warn("Invalid peer multiaddr, skipping")
			continue
		}

		info, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			h.log.WithError(err).WithField("addr", addr).Warn("Failed to parse peer addr info, skipping")
			continue
		}

		if err := h.host.Connect(ctx, *info); err != nil {
			h.log.WithError(err).WithField("peer", info.ID.String()).Warn("Failed to connect to peer")
			continue
		}

		h.log.WithField("peer", info.ID.String()).Info("Connected to P2P peer")
	}

	return nil
}

// Subscribe joins a GossipSub topic and returns a subscription.
func (h *Host) Subscribe(topicName string) (*pubsub.Subscription, error) {
	if h.pubsub == nil {
		return nil, fmt.Errorf("GossipSub not initialized, call Start() first")
	}

	topic, err := h.pubsub.Join(topicName)
	if err != nil {
		return nil, fmt.Errorf("failed to join topic %s: %w", topicName, err)
	}

	sub, err := topic.Subscribe()
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to topic %s: %w", topicName, err)
	}

	h.log.WithField("topic", topicName).Info("Subscribed to gossip topic")

	return sub, nil
}

// Stop closes the libp2p host.
func (h *Host) Stop() error {
	return h.host.Close()
}
