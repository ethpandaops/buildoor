// Package spamoor implements a libp2p-based publisher for ePBS execution payload bids.
// It bypasses the beacon node's HTTP submission endpoint and instead gossips signed
// SignedExecutionPayloadBid messages directly to consensus-layer peers.
//
// The libp2p stack mirrors beaconprobe (https://github.com/sukunrt/beaconprobe):
// noise security, TCP+QUIC transports, yamux+mplex muxers, secp256k1 identity.
package spamoor

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	gcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/libp2p/go-libp2p"
	mplex "github.com/libp2p/go-libp2p-mplex"
	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
)

// newHost creates a libp2p host bound to the given TCP and QUIC ports using
// privKey as the secp256k1 identity. The discv5 listener also uses privKey,
// so the libp2p PeerID matches the ENR node ID.
func newHost(tcpPort, quicPort uint, privKey *ecdsa.PrivateKey) (host.Host, error) {
	libp2pKey, err := crypto.UnmarshalSecp256k1PrivateKey(gcrypto.FromECDSA(privKey))
	if err != nil {
		return nil, fmt.Errorf("convert key: %w", err)
	}

	opts := []libp2p.Option{
		libp2p.Identity(libp2pKey),
		libp2p.Security(noise.ID, noise.New),
		libp2p.DisableRelay(),
		libp2p.ResourceManager(&network.NullResourceManager{}),
		libp2p.ConnectionManager(&connmgr.NullConnMgr{}),
		libp2p.UDPBlackHoleSuccessCounter(nil),
		libp2p.IPv6BlackHoleSuccessCounter(nil),
		libp2p.ListenAddrStrings(
			fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", tcpPort),
			fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", quicPort),
		),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Muxer(yamux.ID, yamux.DefaultTransport),
		libp2p.Muxer(mplex.ID, mplex.DefaultTransport),
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("create libp2p host: %w", err)
	}

	return h, nil
}

// loadOrGenerateKey loads a secp256k1 private key from keyFile or generates a
// new one (saving to keyFile if non-empty). Accepts both raw 32-byte and
// hex-encoded 64-byte key files.
func loadOrGenerateKey(keyFile string) (*ecdsa.PrivateKey, error) {
	if keyFile != "" {
		src, err := os.ReadFile(keyFile)
		if err == nil {
			if len(src) == 32 {
				return gcrypto.ToECDSA(src)
			}

			raw := make([]byte, hex.DecodedLen(len(src)))
			if _, err := hex.Decode(raw, src); err != nil {
				return nil, fmt.Errorf("decode key hex: %w", err)
			}

			return gcrypto.ToECDSA(raw)
		}

		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read key file: %w", err)
		}
	}

	privKey, err := ecdsa.GenerateKey(gcrypto.S256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	if keyFile != "" {
		raw := gcrypto.FromECDSA(privKey)
		dst := make([]byte, hex.EncodedLen(len(raw)))
		hex.Encode(dst, raw)

		if err := os.WriteFile(keyFile, dst, 0600); err != nil {
			return nil, fmt.Errorf("save key: %w", err)
		}
	}

	return privKey, nil
}
