package builderapi

import (
	"math/big"
	"sync"
)

// BidWonEntry represents a single successfully delivered block via Builder API.
type BidWonEntry struct {
	Slot            uint64 `json:"slot"`
	BlockHash       string `json:"block_hash"`
	NumTransactions int    `json:"num_transactions"`
	NumBlobs        int    `json:"num_blobs"`
	ValueETH        string `json:"value_eth"` // Formatted as ETH string for precision
	ValueWei        uint64 `json:"value_wei"` // Stored in wei for sorting
	Timestamp       int64  `json:"timestamp"` // Unix timestamp in milliseconds
}

// BidsWonStore manages an in-memory circular buffer of bid wins.
// Thread-safe for concurrent access.
type BidsWonStore struct {
	entries []BidWonEntry
	maxSize int
	mu      sync.RWMutex
}

// NewBidsWonStore creates a new BidsWonStore with the specified maximum size.
// When the store reaches capacity, oldest entries are evicted (FIFO).
func NewBidsWonStore(maxSize int) *BidsWonStore {
	if maxSize <= 0 {
		maxSize = 1000 // Default size
	}
	return &BidsWonStore{
		entries: make([]BidWonEntry, 0, maxSize),
		maxSize: maxSize,
	}
}

// Add adds a new bid won entry to the store.
// Entries are stored in reverse chronological order (newest first).
// If at capacity, the oldest entry is evicted.
func (s *BidsWonStore) Add(entry BidWonEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Prepend new entry (newest first)
	s.entries = append([]BidWonEntry{entry}, s.entries...)

	// Evict oldest if over capacity
	if len(s.entries) > s.maxSize {
		s.entries = s.entries[:s.maxSize]
	}
}

// GetPage returns a page of entries with pagination support.
// Returns (entries, totalCount) where entries is the requested page
// and totalCount is the total number of entries in the store.
func (s *BidsWonStore) GetPage(offset, limit int) ([]BidWonEntry, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := len(s.entries)

	// Validate offset
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []BidWonEntry{}, total
	}

	// Calculate end index
	end := offset + limit
	if end > total {
		end = total
	}

	// Return slice copy to prevent external modification
	page := make([]BidWonEntry, end-offset)
	copy(page, s.entries[offset:end])

	return page, total
}

// Count returns the total number of entries in the store.
func (s *BidsWonStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// weiToETH converts wei (uint64) to ETH string with 18 decimal places.
func weiToETH(wei uint64) string {
	// Convert wei to big.Float
	weiFloat := new(big.Float).SetUint64(wei)

	// Divide by 1e18 to get ETH
	ethDivisor := new(big.Float).SetFloat64(1e18)
	ethValue := new(big.Float).Quo(weiFloat, ethDivisor)

	// Format with 18 decimal precision
	return ethValue.Text('f', 18)
}
