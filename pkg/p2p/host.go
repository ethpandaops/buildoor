package p2p

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p"
	mplex "github.com/libp2p/go-libp2p-mplex"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptcp "github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"
	"github.com/sirupsen/logrus"
)

const (
	// statusProtocolV2 is the libp2p protocol ID for the StatusV2 RPC (Fulu/Gloas).
	statusProtocolV2 = "/eth2/beacon_chain/req/status/2/ssz_snappy"

	// statusStreamTimeout is the deadline for reading/writing a status stream.
	statusStreamTimeout = 15 * time.Second
)

// StatusProvider can return the current chain status for Status RPC messages.
type StatusProvider interface {
	GetChainStatus(ctx context.Context) (*StatusMessage, error)
}

// HostConfig holds configuration for the P2P host.
type HostConfig struct {
	// ListenPort is the TCP port to listen on. Default: 9500.
	ListenPort uint

	// PeerAddrs are multiaddrs of peers to connect to (e.g. beacon node P2P endpoints).
	PeerAddrs []string
}

// Host is a lightweight libp2p host for subscribing to gossip topics.
type Host struct {
	cfg            HostConfig
	host           host.Host
	pubsub         *pubsub.PubSub
	statusProvider StatusProvider
	log            logrus.FieldLogger
}

// NewHost creates a new P2P host. Call Start() to initialize GossipSub and connect to peers.
func NewHost(cfg HostConfig, log logrus.FieldLogger) (*Host, error) {
	hostLog := log.WithField("component", "p2p")

	privKey, _, err := crypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate P2P key: %w", err)
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

// SetStatusProvider sets the provider used to build StatusV2 messages.
// Must be called before Start() for status handshaking to work.
func (h *Host) SetStatusProvider(sp StatusProvider) {
	h.statusProvider = sp
}

// Start initializes GossipSub, registers the StatusV2 RPC handler, and connects to configured peers.
func (h *Host) Start(ctx context.Context) error {
	// Register the StatusV2 RPC handler so Prysm can call us periodically.
	h.host.SetStreamHandler(protocol.ID(statusProtocolV2), h.handleStatusStream)

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

		// Send the StatusV2 handshake immediately after connecting.
		// Prysm requires a status within 10 seconds (timeForStatus) or it disconnects us.
		peerID := info.ID
		go func() {
			h.log.Info("Sending StatusV2 handshake to peer")
			if err := h.sendStatus(ctx, peerID); err != nil {
				h.log.WithError(err).WithField("peer", peerID.String()).Warn("Status handshake failed")
			}
		}()
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

// sendStatus opens a StatusV2 RPC stream to peerID, sends our status, and reads the response.
// This satisfies Prysm's connection handshake requirement (must arrive within 10 seconds).
func (h *Host) sendStatus(ctx context.Context, peerID peer.ID) error {
	if h.statusProvider == nil {
		h.log.Debug("No status provider configured, skipping status handshake")
		return nil
	}

	ourStatus, err := h.statusProvider.GetChainStatus(ctx)
	if err != nil {
		return fmt.Errorf("get chain status: %w", err)
	}

	h.log.WithFields(logrus.Fields{
		"peer":        peerID.String(),
		"fork_digest": fmt.Sprintf("%x", ourStatus.ForkDigest),
		"head_slot":   ourStatus.HeadSlot,
		"finalized_epoch": ourStatus.FinalizedEpoch,
		"earliest_available_slot": ourStatus.EarliestAvailableSlot,
	}).Info("Sending StatusV2 handshake to peer after getting the status")

	streamCtx, cancel := context.WithTimeout(ctx, statusStreamTimeout)
	defer cancel()

	stream, err := h.host.NewStream(streamCtx, peerID, protocol.ID(statusProtocolV2))
	if err != nil {
		return fmt.Errorf("open status stream: %w", err)
	}
	defer stream.Reset() //nolint:errcheck

	if err := stream.SetDeadline(time.Now().Add(statusStreamTimeout)); err != nil {
		return fmt.Errorf("set stream deadline: %w", err)
	}

	encoded, err := EncodeStatusMessage(ourStatus)
	if err != nil {
		return fmt.Errorf("encode status: %w", err)
	}

	if _, err := stream.Write(encoded); err != nil {
		return fmt.Errorf("write status: %w", err)
	}

	h.log.WithField("peer", peerID.String()).Info("Wrote StatusV2 handshake to peer")

	// Half-close our write side so the peer knows the request is complete.
	if err := stream.CloseWrite(); err != nil {
		return fmt.Errorf("close write: %w", err)
	}

	// Read the response: 1-byte result code + encoded StatusV2.
	h.log.WithField("peer", peerID.String()).Info("Reading StatusV2 handshake response from peer")
	respData, err := io.ReadAll(stream)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	h.log.WithField("peer", peerID.String()).Info("Read StatusV2 handshake response from peer")

	if len(respData) == 0 {
		return fmt.Errorf("empty response from peer")
	}

	if respData[0] != 0x00 {
		return fmt.Errorf("peer returned error code 0x%02x", respData[0])
	}

	h.log.WithField("peer", peerID.String()).Info("Status handshake response code is 0x00")

	if len(respData) < 2 {
		h.log.WithField("peer", peerID.String()).Debug("Status handshake complete (no body in response)")
		return nil
	}

	h.log.WithField("peer", peerID.String()).Info("Decoding StatusV2 handshake response from peer")
	peerStatus, _, err := DecodeStatusMessage(respData[1:])
	if err != nil {
		// Non-fatal — we sent ours successfully; peer's response is informational only.
		h.log.WithError(err).WithField("peer", peerID.String()).Debug("Could not decode peer status response")
		return nil
	}

	h.log.WithFields(logrus.Fields{
		"peer":                    peerID.String(),
		"fork_digest":             fmt.Sprintf("%x", peerStatus.ForkDigest),
		"head_slot":               peerStatus.HeadSlot,
		"finalized_epoch":         peerStatus.FinalizedEpoch,
		"earliest_available_slot": peerStatus.EarliestAvailableSlot,
	}).Info("StatusV2 handshake complete")

	return nil
}

// handleStatusStream handles an incoming StatusV2 RPC stream from a peer.
// Prysm calls this periodically (twice per epoch) for peer re-validation.
func (h *Host) handleStatusStream(stream network.Stream) {
	defer stream.Reset() //nolint:errcheck

	if err := stream.SetDeadline(time.Now().Add(statusStreamTimeout)); err != nil {
		h.log.WithError(err).Debug("Failed to set status stream deadline")
		return
	}

	peerID := stream.Conn().RemotePeer()
	log := h.log.WithField("peer", peerID.String())

	reqData, err := io.ReadAll(stream)
	if err != nil {
		log.WithError(err).Debug("Failed to read status request")
		return
	}

	peerStatus, _, err := DecodeStatusMessage(reqData)
	if err != nil {
		log.WithError(err).Debug("Failed to decode incoming StatusV2 message")
	} else {
		log.WithFields(logrus.Fields{
			"fork_digest":             fmt.Sprintf("%x", peerStatus.ForkDigest),
			"head_slot":               peerStatus.HeadSlot,
			"finalized_epoch":         peerStatus.FinalizedEpoch,
			"earliest_available_slot": peerStatus.EarliestAvailableSlot,
		}).Debug("Received StatusV2 request from peer")
	}

	if h.statusProvider == nil {
		if _, err := stream.Write([]byte{0x02}); err != nil { // responseCodeServerError
			log.WithError(err).Debug("Failed to write status error response")
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), statusStreamTimeout)
	defer cancel()

	ourStatus, err := h.statusProvider.GetChainStatus(ctx)
	if err != nil {
		log.WithError(err).Warn("Failed to get chain status for status response")

		if _, err := stream.Write([]byte{0x02}); err != nil { // responseCodeServerError
			log.WithError(err).Debug("Failed to write status error response")
		}

		return
	}

	encoded, err := EncodeStatusMessage(ourStatus)
	if err != nil {
		log.WithError(err).Warn("Failed to encode status response")

		if _, err := stream.Write([]byte{0x02}); err != nil { // responseCodeServerError
			log.WithError(err).Debug("Failed to write status error response")
		}

		return
	}

	// Write: success code byte + encoded StatusV2.
	resp := make([]byte, 1+len(encoded))
	resp[0] = 0x00 // responseCodeSuccess
	copy(resp[1:], encoded)

	if _, err := stream.Write(resp); err != nil {
		log.WithError(err).Debug("Failed to write status response")
		return
	}

	if err := stream.Close(); err != nil {
		log.WithError(err).Debug("Failed to close status stream")
	}

	log.WithFields(logrus.Fields{
		"head_slot":       ourStatus.HeadSlot,
		"finalized_epoch": ourStatus.FinalizedEpoch,
	}).Debug("Responded to StatusV2 request")
}
