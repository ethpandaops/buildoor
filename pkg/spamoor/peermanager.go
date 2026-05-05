package spamoor

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/sirupsen/logrus"
)

// Peer-manager tunables. Mirror beaconprobe defaults.
const (
	maxConcurrentDials = 500
	dialTimeout        = 4 * time.Second
	initialBackoff     = 30 * time.Second
	maxBackoff         = 30 * time.Minute
	peerCheckInterval  = 10 * time.Second
	pullInterval       = 500 * time.Millisecond
	pullBatch          = 50
)

type backoffEntry struct {
	failCount int
	nextDial  time.Time
}

// peerManager pulls candidate peers from a peerStore and dials them with
// backpressure (semaphore) and exponential backoff on failure.
type peerManager struct {
	h        host.Host
	store    *peerStore
	maxPeers int
	log      logrus.FieldLogger

	mu      sync.Mutex
	backoff map[peer.ID]backoffEntry
}

func newPeerManager(h host.Host, store *peerStore, maxPeers int, log logrus.FieldLogger) *peerManager {
	return &peerManager{
		h:        h,
		store:    store,
		maxPeers: maxPeers,
		backoff:  make(map[peer.ID]backoffEntry),
		log:      log.WithField("component", "spamoor-peermgr"),
	}
}

// run blocks until ctx is cancelled. Runs the dial loop.
func (pm *peerManager) run(ctx context.Context) {
	sem := make(chan struct{}, maxConcurrentDials)

	var inFlight atomic.Int64

	for {
		if pm.tooManyInFlight(&inFlight) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(peerCheckInterval):
				continue
			}
		}

		peers := pm.store.take(pullBatch, pm.shouldSkip)
		if len(peers) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pullInterval):
			}

			continue
		}

		for _, ai := range peers {
			if pm.tooManyInFlight(&inFlight) {
				break
			}

			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}

			inFlight.Add(1)

			go func(ai peer.AddrInfo) {
				defer func() { <-sem; inFlight.Add(-1) }()
				pm.handleCandidate(ctx, ai)
			}(ai)
		}
	}
}

func (pm *peerManager) shouldSkip(id peer.ID) bool {
	if pm.h.Network().Connectedness(id) == network.Connected {
		return true
	}

	return pm.inBackoff(id)
}

func (pm *peerManager) tooManyInFlight(inFlight *atomic.Int64) bool {
	if pm.maxPeers == 0 {
		return false
	}

	return len(pm.h.Network().Peers())+int(inFlight.Load()) >= pm.maxPeers+20
}

func (pm *peerManager) handleCandidate(ctx context.Context, ai peer.AddrInfo) {
	if pm.h.Network().Connectedness(ai.ID) == network.Connected {
		return
	}

	if pm.inBackoff(ai.ID) {
		return
	}

	if pm.maxPeers > 0 && len(pm.h.Network().Peers()) >= pm.maxPeers {
		return
	}

	connectCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	connectCtx = network.WithDialPeerTimeout(connectCtx, dialTimeout)

	if err := pm.h.Connect(connectCtx, ai); err != nil {
		pm.recordFailure(ai.ID)
		pm.log.WithError(err).WithField("peer", peerShort(ai.ID)).Debug("failed to connect")

		return
	}

	pm.clearBackoff(ai.ID)

	conns := pm.h.Network().ConnsToPeer(ai.ID)

	var remoteAddr string
	if len(conns) > 0 {
		remoteAddr = conns[0].RemoteMultiaddr().String()
	}

	pm.log.WithFields(logrus.Fields{
		"peer":        peerShort(ai.ID),
		"remote_addr": remoteAddr,
	}).Debug("connected to peer")
}

func (pm *peerManager) inBackoff(id peer.ID) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	entry, ok := pm.backoff[id]
	if !ok {
		return false
	}

	return time.Now().Before(entry.nextDial)
}

func (pm *peerManager) recordFailure(id peer.ID) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	entry := pm.backoff[id]
	entry.failCount++

	shift := entry.failCount - 1
	if shift > 10 {
		shift = 10
	}

	d := initialBackoff * (1 << shift)
	if d > maxBackoff {
		d = maxBackoff
	}

	entry.nextDial = time.Now().Add(d)
	pm.backoff[id] = entry
}

func (pm *peerManager) clearBackoff(id peer.ID) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	delete(pm.backoff, id)
}
