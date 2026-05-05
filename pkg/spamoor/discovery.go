package spamoor

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"fmt"
	"net"
	"os"
	"strings"

	ecdsaprysm "github.com/OffchainLabs/prysm/v7/crypto/ecdsa"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/enr"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/sirupsen/logrus"
)

const (
	eth2ENRKey         = "eth2"
	farFutureEpochUint = uint64(1<<64 - 1)
)

// quicProtocol is the "quic" ENR key that holds the QUIC port of a node.
type quicProtocol uint16

func (quicProtocol) ENRKey() string { return "quic" }

// discoveryConfig configures the discv5 listener and discovery loop.
type discoveryConfig struct {
	PrivKey    *ecdsa.PrivateKey
	DiscPort   uint
	ForkDigest [4]byte
	Bootnodes  []*enode.Node
	Store      *peerStore
	Log        logrus.FieldLogger
}

// startDiscovery brings up the discv5 listener and (asynchronously) feeds
// matching ENRs into the peer store.
func startDiscovery(ctx context.Context, cfg discoveryConfig) (*discover.UDPv5, error) {
	db, err := enode.OpenDB("")
	if err != nil {
		return nil, fmt.Errorf("open enode db: %w", err)
	}

	localNode := enode.NewLocalNode(db, cfg.PrivKey)
	localNode.SetFallbackIP(net.IPv4zero)
	localNode.SetFallbackUDP(int(cfg.DiscPort))

	// Advertise our fork digest so honest peers will dial us back.
	enrForkID := &ethpb.ENRForkID{
		CurrentForkDigest: cfg.ForkDigest[:],
		NextForkVersion:   cfg.ForkDigest[:],
		NextForkEpoch:     primitives.Epoch(farFutureEpochUint),
	}

	enrForkIDBytes, err := enrForkID.MarshalSSZ()
	if err != nil {
		return nil, fmt.Errorf("marshal ENR fork ID: %w", err)
	}

	localNode.Set(enr.WithEntry(eth2ENRKey, enrForkIDBytes))

	udpAddr := &net.UDPAddr{IP: net.IPv4zero, Port: int(cfg.DiscPort)}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("listen udp: %w", err)
	}

	listener, err := discover.ListenV5(conn, localNode, discover.Config{
		PrivateKey: cfg.PrivKey,
		Bootnodes:  cfg.Bootnodes,
	})
	if err != nil {
		return nil, fmt.Errorf("listen discv5: %w", err)
	}

	cfg.Log.WithField("bootnodes", len(cfg.Bootnodes)).Info("discv5 listener started")

	forkFilter := enode.Filter(listener.RandomNodes(), func(n *enode.Node) bool {
		return matchesForkDigest(n, cfg.ForkDigest)
	})

	go discoverPeers(ctx, forkFilter, cfg.Store, cfg.Log)

	return listener, nil
}

func discoverPeers(ctx context.Context, iterator enode.Iterator, store *peerStore, log logrus.FieldLogger) {
	defer iterator.Close()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !iterator.Next() {
			return
		}

		node := iterator.Node()

		ai, err := enodeToAddrInfo(node)
		if err != nil || ai == nil {
			continue
		}

		store.add(*ai)

		log.WithFields(logrus.Fields{
			"peer":  peerShort(ai.ID),
			"total": store.size(),
		}).Debug("discovered peer")
	}
}

func matchesForkDigest(node *enode.Node, forkDigest [4]byte) bool {
	var eth2Data []byte
	if err := node.Record().Load(enr.WithEntry(eth2ENRKey, &eth2Data)); err != nil {
		return false
	}

	if len(eth2Data) < 4 {
		return false
	}

	var forkID ethpb.ENRForkID
	if err := forkID.UnmarshalSSZ(eth2Data); err != nil {
		return false
	}

	if len(forkID.CurrentForkDigest) < 4 {
		return false
	}

	return forkID.CurrentForkDigest[0] == forkDigest[0] &&
		forkID.CurrentForkDigest[1] == forkDigest[1] &&
		forkID.CurrentForkDigest[2] == forkDigest[2] &&
		forkID.CurrentForkDigest[3] == forkDigest[3]
}

func enodeToAddrInfo(node *enode.Node) (*peer.AddrInfo, error) {
	multiaddrs, err := retrieveMultiAddrsFromNode(node)
	if err != nil {
		return nil, err
	}

	if len(multiaddrs) == 0 {
		return nil, nil
	}

	infos, err := peer.AddrInfosFromP2pAddrs(multiaddrs...)
	if err != nil {
		return nil, fmt.Errorf("convert multiaddr to peer info: %w", err)
	}

	if len(infos) != 1 {
		return nil, fmt.Errorf("expected exactly 1 peer info, got %d", len(infos))
	}

	return &infos[0], nil
}

func retrieveMultiAddrsFromNode(node *enode.Node) ([]ma.Multiaddr, error) {
	multiaddrs := make([]ma.Multiaddr, 0, 2)

	pubkey := node.Pubkey()
	if pubkey == nil {
		return nil, fmt.Errorf("no pubkey")
	}

	asserted, err := ecdsaprysm.ConvertToInterfacePubkey(pubkey)
	if err != nil {
		return nil, fmt.Errorf("convert pubkey: %w", err)
	}

	id, err := peer.IDFromPublicKey(asserted)
	if err != nil {
		return nil, fmt.Errorf("derive peer id: %w", err)
	}

	ip := node.IP()
	if ip == nil {
		return nil, nil
	}

	ipType := "ip4"
	if ip.To4() == nil && ip.To16() != nil {
		ipType = "ip6"
	}

	var qp quicProtocol
	if err := node.Load(&qp); err == nil && qp != 0 {
		addr, err := ma.NewMultiaddr(fmt.Sprintf("/%s/%s/udp/%d/quic-v1/p2p/%s", ipType, ip, qp, id))
		if err != nil {
			return nil, fmt.Errorf("build QUIC multiaddr: %w", err)
		}

		multiaddrs = append(multiaddrs, addr)
	}

	if tcpPort := node.TCP(); tcpPort != 0 {
		addr, err := ma.NewMultiaddr(fmt.Sprintf("/%s/%s/tcp/%d/p2p/%s", ipType, ip, tcpPort, id))
		if err != nil {
			return nil, fmt.Errorf("build TCP multiaddr: %w", err)
		}

		multiaddrs = append(multiaddrs, addr)
	}

	return multiaddrs, nil
}

// parseBootnodes parses one or more ENRs.
// spec is either a comma-separated list of ENRs or "@/path/to/file" (one ENR per line).
func parseBootnodes(spec string) ([]*enode.Node, error) {
	if spec == "" {
		return nil, nil
	}

	var lines []string

	if strings.HasPrefix(spec, "@") {
		f, err := os.Open(spec[1:])
		if err != nil {
			return nil, fmt.Errorf("open bootnodes file: %w", err)
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				lines = append(lines, line)
			}
		}

		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read bootnodes file: %w", err)
		}
	} else {
		for _, raw := range strings.Split(spec, ",") {
			if line := strings.TrimSpace(raw); line != "" {
				lines = append(lines, line)
			}
		}
	}

	nodes := make([]*enode.Node, 0, len(lines))
	for _, line := range lines {
		n, err := enode.Parse(enode.ValidSchemes, line)
		if err != nil {
			return nil, fmt.Errorf("parse enr %q: %w", line, err)
		}

		nodes = append(nodes, n)
	}

	return nodes, nil
}

// parseStaticPeers parses a comma-separated list of libp2p multiaddrs into
// peer.AddrInfo values suitable for direct dialing.
func parseStaticPeers(spec string) ([]peer.AddrInfo, error) {
	if spec == "" {
		return nil, nil
	}

	var addrs []ma.Multiaddr

	for _, raw := range strings.Split(spec, ",") {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}

		a, err := ma.NewMultiaddr(s)
		if err != nil {
			return nil, fmt.Errorf("parse multiaddr %q: %w", s, err)
		}

		addrs = append(addrs, a)
	}

	if len(addrs) == 0 {
		return nil, nil
	}

	infos, err := peer.AddrInfosFromP2pAddrs(addrs...)
	if err != nil {
		return nil, fmt.Errorf("addrinfos from multiaddrs: %w", err)
	}

	return infos, nil
}
