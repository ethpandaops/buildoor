package spamoor

import (
	"math/rand/v2"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
)

// peerStore holds discovered dial candidates. Single-instance simplification
// of beaconprobe's PeerFinder/SharedPeerSet (we don't shard peers across
// multiple hosts).
type peerStore struct {
	mu    sync.Mutex
	peers map[peer.ID]peer.AddrInfo
}

func newPeerStore() *peerStore {
	return &peerStore{peers: make(map[peer.ID]peer.AddrInfo)}
}

// add stores a candidate peer; idempotent (later calls update addresses).
func (ps *peerStore) add(ai peer.AddrInfo) {
	ps.mu.Lock()
	ps.peers[ai.ID] = ai
	ps.mu.Unlock()
}

// take returns up to count random candidates that don't satisfy the skip
// predicate (used by peerManager to avoid re-dialing connected/backoff peers).
func (ps *peerStore) take(count int, skip func(peer.ID) bool) []peer.AddrInfo {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if len(ps.peers) == 0 {
		return nil
	}

	eligible := make([]peer.ID, 0, len(ps.peers))

	for id := range ps.peers {
		if skip != nil && skip(id) {
			continue
		}

		eligible = append(eligible, id)
	}

	if len(eligible) == 0 {
		return nil
	}

	rand.Shuffle(len(eligible), func(i, j int) {
		eligible[i], eligible[j] = eligible[j], eligible[i]
	})

	if count > len(eligible) {
		count = len(eligible)
	}

	result := make([]peer.AddrInfo, count)
	for i := 0; i < count; i++ {
		result[i] = ps.peers[eligible[i]]
	}

	return result
}

// size returns the number of candidates currently in the store.
func (ps *peerStore) size() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	return len(ps.peers)
}
