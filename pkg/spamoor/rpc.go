package spamoor

import (
	"context"
	"io"
	"time"

	"github.com/OffchainLabs/prysm/v7/beacon-chain/p2p/encoder"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	fastssz "github.com/prysmaticlabs/fastssz"
	"github.com/sirupsen/logrus"
)

// Standard CL req/resp protocol IDs used in the consensus-layer P2P stack.
const (
	statusProtocolV1   = "/eth2/beacon_chain/req/status/1/ssz_snappy"
	statusProtocolV2   = "/eth2/beacon_chain/req/status/2/ssz_snappy"
	pingProtocol       = "/eth2/beacon_chain/req/ping/1/ssz_snappy"
	metadataProtocolV1 = "/eth2/beacon_chain/req/metadata/1/ssz_snappy"
	metadataProtocolV2 = "/eth2/beacon_chain/req/metadata/2/ssz_snappy"
	metadataProtocolV3 = "/eth2/beacon_chain/req/metadata/3/ssz_snappy"
	goodbyeProtocol    = "/eth2/beacon_chain/req/goodbye/1/ssz_snappy"

	responseCodeSuccess = byte(0x00)

	streamTimeout = 10 * time.Second
)

var enc = encoder.SszNetworkEncoder{}

// statusProvider returns our current StatusV2 message.
type statusProvider func() *ethpb.StatusV2

// registerRPCHandlers attaches stream handlers for the standard CL req/resp
// protocols. Peers will not gossip to us until we complete a Status handshake.
func registerRPCHandlers(h host.Host, sp statusProvider, attnets []byte, log logrus.FieldLogger) {
	statusH := func(stream network.Stream) {
		defer stream.Close()
		handleStatus(stream, sp, log)
	}
	h.SetStreamHandler(protocol.ID(statusProtocolV1), statusH)
	h.SetStreamHandler(protocol.ID(statusProtocolV2), statusH)

	h.SetStreamHandler(protocol.ID(pingProtocol), func(stream network.Stream) {
		defer stream.Close()
		handlePing(stream, log)
	})

	metadataH := func(stream network.Stream) {
		defer stream.Close()
		handleMetadata(stream, attnets, log)
	}
	h.SetStreamHandler(protocol.ID(metadataProtocolV1), metadataH)
	h.SetStreamHandler(protocol.ID(metadataProtocolV2), metadataH)
	h.SetStreamHandler(protocol.ID(metadataProtocolV3), metadataH)

	h.SetStreamHandler(protocol.ID(goodbyeProtocol), func(stream network.Stream) {
		defer stream.Close()
		handleGoodbye(stream, log)
	})
}

func peerShort(id peer.ID) string {
	s := id.String()
	if len(s) > 16 {
		return s[:16]
	}

	return s
}

func handleStatus(stream network.Stream, sp statusProvider, log logrus.FieldLogger) {
	_ = stream.SetDeadline(time.Now().Add(streamTimeout))

	var peerStatus ethpb.StatusV2
	if err := enc.DecodeWithMaxLength(stream, &peerStatus); err != nil {
		log.WithError(err).Debug("failed to decode peer status")
		return
	}

	log.WithFields(logrus.Fields{
		"peer":      peerShort(stream.Conn().RemotePeer()),
		"head_slot": peerStatus.HeadSlot,
	}).Debug("received status request")

	ourStatus := sp()
	if _, err := stream.Write([]byte{responseCodeSuccess}); err != nil {
		log.WithError(err).Debug("failed to write status response code")
		return
	}

	if _, err := enc.EncodeWithMaxLength(stream, ourStatus); err != nil {
		log.WithError(err).Debug("failed to encode status response")
		return
	}
}

func handlePing(stream network.Stream, log logrus.FieldLogger) {
	_ = stream.SetDeadline(time.Now().Add(streamTimeout))

	var ping primitives.SSZUint64
	if err := enc.DecodeWithMaxLength(stream, &ping); err != nil {
		log.WithError(err).Debug("failed to decode ping")
		return
	}

	var seq primitives.SSZUint64
	if _, err := stream.Write([]byte{responseCodeSuccess}); err != nil {
		return
	}

	if _, err := enc.EncodeWithMaxLength(stream, &seq); err != nil {
		return
	}
}

func handleMetadata(stream network.Stream, attnets []byte, log logrus.FieldLogger) {
	_ = stream.SetDeadline(time.Now().Add(streamTimeout))

	if _, err := stream.Write([]byte{responseCodeSuccess}); err != nil {
		return
	}

	var md fastssz.Marshaler

	switch string(stream.Protocol()) {
	case metadataProtocolV3:
		md = &ethpb.MetaDataV2{
			SeqNumber:         0,
			Attnets:           attnets,
			Syncnets:          []byte{0},
			CustodyGroupCount: 4,
		}
	case metadataProtocolV1:
		md = &ethpb.MetaDataV0{
			SeqNumber: 0,
			Attnets:   attnets,
		}
	default:
		md = &ethpb.MetaDataV1{
			SeqNumber: 0,
			Attnets:   attnets,
			Syncnets:  []byte{0},
		}
	}

	if _, err := enc.EncodeWithMaxLength(stream, md); err != nil {
		log.WithError(err).Debug("failed to encode metadata response")
		return
	}
}

var goodbyeReasons = map[uint64]string{
	1:   "client shutdown",
	2:   "irrelevant network",
	3:   "fault/error",
	128: "unable to verify network",
	129: "too many peers",
	250: "peer score too low",
	251: "client banned this node",
}

func handleGoodbye(stream network.Stream, log logrus.FieldLogger) {
	_ = stream.SetDeadline(time.Now().Add(streamTimeout))

	var reason primitives.SSZUint64
	if err := enc.DecodeWithMaxLength(stream, &reason); err != nil {
		log.WithError(err).Debug("failed to decode goodbye")
		return
	}

	code := uint64(reason)

	msg := goodbyeReasons[code]
	if msg == "" {
		msg = "unknown"
	}

	log.WithFields(logrus.Fields{
		"peer":   peerShort(stream.Conn().RemotePeer()),
		"reason": msg,
		"code":   code,
	}).Debug("received goodbye")
}

// sendStatusOnConnect installs a network notifee that initiates a Status
// handshake with every newly connected peer. Without this, most CL clients
// will refuse to relay gossipsub messages from us.
func sendStatusOnConnect(h host.Host, sp statusProvider, log logrus.FieldLogger) {
	notifier := &network.NotifyBundle{
		ConnectedF: func(_ network.Network, conn network.Conn) {
			go sendStatus(h, conn.RemotePeer(), sp, log)
		},
	}
	h.Network().Notify(notifier)
}

func sendStatus(h host.Host, peerID peer.ID, sp statusProvider, log logrus.FieldLogger) {
	ctx, cancel := context.WithTimeout(context.Background(), streamTimeout)
	defer cancel()

	stream, err := h.NewStream(ctx, peerID, protocol.ID(statusProtocolV2), protocol.ID(statusProtocolV1))
	if err != nil {
		log.WithError(err).WithField("peer", peerShort(peerID)).Debug("failed to open status stream")
		return
	}
	defer stream.Close()

	_ = stream.SetDeadline(time.Now().Add(streamTimeout))

	ourStatus := sp()
	if _, err := enc.EncodeWithMaxLength(stream, ourStatus); err != nil {
		log.WithError(err).Debug("failed to encode outbound status")
		return
	}

	if err := stream.CloseWrite(); err != nil {
		log.WithError(err).Debug("failed to close write on status stream")
		return
	}

	code := make([]byte, 1)
	if _, err := io.ReadFull(stream, code); err != nil {
		log.WithError(err).Debug("failed to read status response code")
		return
	}

	if code[0] != responseCodeSuccess {
		log.WithField("code", code[0]).Debug("peer returned non-success status")
		return
	}

	var peerStatus ethpb.StatusV2
	if err := enc.DecodeWithMaxLength(stream, &peerStatus); err != nil {
		log.WithError(err).Debug("failed to decode peer status response")
		return
	}

	log.WithFields(logrus.Fields{
		"peer":      peerShort(peerID),
		"head_slot": peerStatus.HeadSlot,
	}).Debug("status handshake complete")
}

// makeStatusProvider returns a statusProvider that synthesizes a plausible
// StatusV2 from local genesis time. Mirrors beaconprobe's approach: we don't
// query our beacon node — we report the current slot derived from genesis
// and zero finalized/head roots.
//
// slotDuration is used directly (instead of prysm's params.BeaconConfig)
// so devnets with non-mainnet slot times report the correct head_slot.
func makeStatusProvider(forkDigest [4]byte, genesisTime time.Time, slotDuration time.Duration) statusProvider {
	return func() *ethpb.StatusV2 {
		headSlot := primitives.Slot(0)
		if slotDuration > 0 {
			elapsed := time.Since(genesisTime)
			if elapsed > 0 {
				cs := uint64(elapsed / slotDuration)
				if cs > 0 {
					headSlot = primitives.Slot(cs - 1)
				}
			}
		}

		return &ethpb.StatusV2{
			ForkDigest:            forkDigest[:],
			FinalizedRoot:         make([]byte, 32),
			FinalizedEpoch:        0,
			HeadRoot:              make([]byte, 32),
			HeadSlot:              headSlot,
			EarliestAvailableSlot: 0,
		}
	}
}
