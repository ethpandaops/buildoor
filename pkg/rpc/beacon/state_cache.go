package beacon

import (
	"context"
	"fmt"
	"sync"

	"github.com/attestantio/go-eth2-client/spec"
	"github.com/attestantio/go-eth2-client/spec/phase0"
)

// StateCache caches beacon states with a maximum capacity.
// It uses a simple LRU-like eviction based on slot number.
type StateCache struct {
	states   map[phase0.Root]*CachedState
	maxSize  int
	mu       sync.RWMutex
	client   *Client
	fetching map[phase0.Root]bool // Track in-flight fetches
	fetchMu  sync.Mutex
}

// CachedState holds a cached beacon state with metadata.
type CachedState struct {
	State     *spec.VersionedBeaconState
	Slot      phase0.Slot
	BlockRoot phase0.Root
}

// NewStateCache creates a new state cache with the given maximum size.
func NewStateCache(client *Client, maxSize int) *StateCache {
	return &StateCache{
		states:   make(map[phase0.Root]*CachedState, maxSize),
		maxSize:  maxSize,
		client:   client,
		fetching: make(map[phase0.Root]bool, 2),
	}
}

// Get retrieves a cached state by block root.
func (c *StateCache) Get(blockRoot phase0.Root) *CachedState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.states[blockRoot]
}

// GetBySlot retrieves the cached state for a given slot.
// Returns nil if no state is cached for that slot.
func (c *StateCache) GetBySlot(slot phase0.Slot) *CachedState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, state := range c.states {
		if state.Slot == slot {
			return state
		}
	}

	return nil
}

// GetLatest returns the most recent cached state.
func (c *StateCache) GetLatest() *CachedState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var latest *CachedState

	for _, state := range c.states {
		if latest == nil || state.Slot > latest.Slot {
			latest = state
		}
	}

	return latest
}

// Store adds a state to the cache, evicting oldest if at capacity.
func (c *StateCache) Store(blockRoot phase0.Root, state *spec.VersionedBeaconState, slot phase0.Slot) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If already cached, update it
	if _, exists := c.states[blockRoot]; exists {
		c.states[blockRoot] = &CachedState{
			State:     state,
			Slot:      slot,
			BlockRoot: blockRoot,
		}

		return
	}

	// If at capacity, evict the oldest state
	if len(c.states) >= c.maxSize {
		var oldestRoot phase0.Root

		oldestSlot := ^phase0.Slot(0) // Max value

		for root, cached := range c.states {
			if cached.Slot < oldestSlot {
				oldestSlot = cached.Slot
				oldestRoot = root
			}
		}

		if oldestRoot != (phase0.Root{}) {
			delete(c.states, oldestRoot)
		}
	}

	c.states[blockRoot] = &CachedState{
		State:     state,
		Slot:      slot,
		BlockRoot: blockRoot,
	}
}

// FetchAndCache fetches the beacon state for a block and caches it.
// This is safe to call concurrently - duplicate fetches are deduplicated.
func (c *StateCache) FetchAndCache(
	ctx context.Context,
	blockRoot phase0.Root,
	stateRoot phase0.Root,
	slot phase0.Slot,
) error {
	// Check if already cached
	if cached := c.Get(blockRoot); cached != nil {
		return nil
	}

	// Check if fetch is already in progress
	c.fetchMu.Lock()
	if c.fetching[blockRoot] {
		c.fetchMu.Unlock()

		return nil // Another goroutine is fetching
	}

	c.fetching[blockRoot] = true
	c.fetchMu.Unlock()

	defer func() {
		c.fetchMu.Lock()
		delete(c.fetching, blockRoot)
		c.fetchMu.Unlock()
	}()

	// Fetch the state using state root
	stateID := fmt.Sprintf("0x%x", stateRoot[:])

	state, err := c.client.fetchBeaconState(ctx, stateID)
	if err != nil {
		return fmt.Errorf("failed to fetch beacon state: %w", err)
	}

	// Cache the state
	c.Store(blockRoot, state, slot)

	c.client.log.WithField("slot", slot).Debug("Beacon state cached")

	return nil
}

// Size returns the current number of cached states.
func (c *StateCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.states)
}

// Clear removes all cached states.
func (c *StateCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.states = make(map[phase0.Root]*CachedState, c.maxSize)
}
