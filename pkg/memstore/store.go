// Package memstore provides a generic, thread-safe, keyed in-memory store with
// optional buffered write-behind persistence. The stored content type, its
// validation, and its persisted encoding (the "flavor") live with the module
// that manages the data — this package only owns concurrency, write policy,
// pruning, rehydration, and flush batching.
package memstore

import (
	"context"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	// defaultFlushInterval debounces persistence flushes: after the first
	// change the flush loop waits this long so bursts (e.g. a full validator
	// set re-registering) collapse into a single batch.
	defaultFlushInterval = 2 * time.Second
	// defaultMaxPending forces an immediate flush once this many changes are
	// pending, bounding the amount of unflushed state during heavy write
	// bursts.
	defaultMaxPending = 256
)

// Persistence adapts a store to a backing table. Load is called once when the
// adapter is attached; PersistBatch is called from the store's flush loop with
// everything that changed since the last flush (upserts + deletions) and must
// apply the batch in a single transaction. Best-effort: on error the store
// logs and retries the same batch on the next flush.
type Persistence[K comparable, V any] interface {
	Load() (map[K]V, error)
	PersistBatch(upserts map[K]V, deletes []K) error
}

// Store is a generic, thread-safe, keyed in-memory store with a configurable
// write policy (last-write-wins or keep-existing) and optional buffered
// write-behind persistence. Writers never touch the persistence backend on
// their hot path; changes are tracked in dirty/deleted sets drained by the
// store's own flush loop.
type Store[K comparable, V any] struct {
	mu           sync.RWMutex
	entries      map[K]V
	keepExisting bool

	persistence Persistence[K, V] // nil = in-memory only
	dirty       map[K]struct{}    // changed since last flush
	deleted     map[K]struct{}    // deleted since last flush

	flushInterval time.Duration // test override hook
	maxPending    int           // test override hook

	poke  chan struct{} // cap 1; nudges the flush loop on a change
	force chan struct{} // cap 1; skips the debounce at maxPending

	flushMu sync.Mutex // serialises Flush (loop vs final flush on Stop)
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	log     logrus.FieldLogger
}

// New creates a store with the last-write-wins policy: Put always replaces.
func New[K comparable, V any]() *Store[K, V] {
	return newStore[K, V](false)
}

// NewKeepExisting creates a store with the first-write-wins policy: Put is
// rejected when the key is already present.
func NewKeepExisting[K comparable, V any]() *Store[K, V] {
	return newStore[K, V](true)
}

func newStore[K comparable, V any](keepExisting bool) *Store[K, V] {
	return &Store[K, V]{
		entries:       make(map[K]V, 64),
		keepExisting:  keepExisting,
		dirty:         make(map[K]struct{}, 64),
		deleted:       make(map[K]struct{}, 16),
		flushInterval: defaultFlushInterval,
		maxPending:    defaultMaxPending,
		poke:          make(chan struct{}, 1),
		force:         make(chan struct{}, 1),
	}
}

// SetPersistence attaches the persistence adapter, rehydrates the store via
// Load() (loaded entries never overwrite in-memory ones written before
// attach), and starts the flush loop. Entries written before attach are
// marked dirty so they reach the backing table on the first flush. Attach at
// most once; a second call is ignored with a warning.
func (s *Store[K, V]) SetPersistence(ctx context.Context, p Persistence[K, V], log logrus.FieldLogger) {
	if p == nil {
		return
	}

	if log == nil {
		log = logrus.StandardLogger()
	}

	s.mu.Lock()

	if s.persistence != nil {
		s.mu.Unlock()
		log.Warn("memstore persistence already attached; ignoring")

		return
	}

	s.persistence = p
	s.log = log

	// Everything written before attach is unknown to the backing table.
	for key := range s.entries {
		s.dirty[key] = struct{}{}
	}

	s.mu.Unlock()

	loaded, err := p.Load()
	if err != nil {
		log.WithError(err).Warn("failed to load persisted entries; continuing with in-memory state only")
	}

	s.mu.Lock()

	for key, value := range loaded {
		if _, exists := s.entries[key]; !exists {
			s.entries[key] = value
		}
	}

	pending := len(s.dirty) + len(s.deleted)
	s.ctx, s.cancel = context.WithCancel(ctx)

	s.mu.Unlock()

	s.wg.Add(1)

	go s.run()

	s.notify(pending)
}

// Stop terminates the flush loop and synchronously flushes all pending
// changes. Must be called before the backing database closes. No-op when no
// persistence is attached; idempotent.
func (s *Store[K, V]) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()

	if cancel == nil {
		return
	}

	cancel()
	s.wg.Wait()

	if err := s.Flush(); err != nil {
		s.log.WithError(err).Error("final memstore flush on shutdown failed; pending changes lost")
	}
}

// Flush synchronously persists all pending changes in one batch. On failure
// the batch is re-marked and retried on the next flush. No-op when no
// persistence is attached or nothing is pending.
func (s *Store[K, V]) Flush() error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	s.mu.Lock()

	p := s.persistence
	if p == nil || (len(s.dirty) == 0 && len(s.deleted) == 0) {
		s.mu.Unlock()

		return nil
	}

	upserts := make(map[K]V, len(s.dirty))

	for key := range s.dirty {
		if value, exists := s.entries[key]; exists {
			upserts[key] = value
		}
	}

	deletes := make([]K, 0, len(s.deleted))
	for key := range s.deleted {
		deletes = append(deletes, key)
	}

	dirtySnap := s.dirty
	deletedSnap := s.deleted
	s.dirty = make(map[K]struct{}, 64)
	s.deleted = make(map[K]struct{}, 16)

	s.mu.Unlock()

	if err := p.PersistBatch(upserts, deletes); err != nil {
		s.requeue(dirtySnap, deletedSnap)

		return err
	}

	return nil
}

// requeue re-marks a failed batch for the next flush without clobbering
// changes made while the batch was in flight.
func (s *Store[K, V]) requeue(dirtySnap, deletedSnap map[K]struct{}) {
	s.mu.Lock()

	for key := range dirtySnap {
		if _, deleted := s.deleted[key]; !deleted {
			s.dirty[key] = struct{}{}
		}
	}

	for key := range deletedSnap {
		if _, redone := s.dirty[key]; !redone {
			s.deleted[key] = struct{}{}
		}
	}

	pending := len(s.dirty) + len(s.deleted)

	s.mu.Unlock()

	s.notify(pending)
}

// Put stores value under key. With the keep-existing policy it returns false
// and leaves the store untouched when the key is already present; otherwise
// it returns true.
func (s *Store[K, V]) Put(key K, value V) bool {
	s.mu.Lock()

	if s.keepExisting {
		if _, exists := s.entries[key]; exists {
			s.mu.Unlock()

			return false
		}
	}

	s.entries[key] = value
	pending := s.markDirtyLocked(key)

	s.mu.Unlock()

	s.notify(pending)

	return true
}

// Get returns the value stored under key.
func (s *Store[K, V]) Get(key K) (V, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	value, exists := s.entries[key]

	return value, exists
}

// Has reports whether key is present.
func (s *Store[K, V]) Has(key K) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, exists := s.entries[key]

	return exists
}

// Len returns the number of stored entries.
func (s *Store[K, V]) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.entries)
}

// Values returns a snapshot slice of all stored values.
func (s *Store[K, V]) Values() []V {
	s.mu.RLock()
	defer s.mu.RUnlock()

	values := make([]V, 0, len(s.entries))
	for _, value := range s.entries {
		values = append(values, value)
	}

	return values
}

// Entries returns a copy of the stored entries; mutating the returned map
// does not affect the store.
func (s *Store[K, V]) Entries() map[K]V {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make(map[K]V, len(s.entries))
	for key, value := range s.entries {
		entries[key] = value
	}

	return entries
}

// Delete removes key from the store and marks the deletion for the next
// flush. No-op when the key is absent.
func (s *Store[K, V]) Delete(key K) {
	s.mu.Lock()

	if _, exists := s.entries[key]; !exists {
		s.mu.Unlock()

		return
	}

	delete(s.entries, key)
	pending := s.markDeletedLocked(key)

	s.mu.Unlock()

	s.notify(pending)
}

// Prune removes every key for which drop returns true, marking the deletions
// for the next flush, and returns the number of removed entries. The drop
// predicate is called with the store lock held and must not call back into
// the store.
func (s *Store[K, V]) Prune(drop func(key K) bool) int {
	s.mu.Lock()

	pruned := 0
	pending := 0

	for key := range s.entries {
		if !drop(key) {
			continue
		}

		delete(s.entries, key)

		pending = s.markDeletedLocked(key)
		pruned++
	}

	s.mu.Unlock()

	s.notify(pending)

	return pruned
}

// Clear removes all entries, marking every deletion for the next flush.
func (s *Store[K, V]) Clear() {
	s.mu.Lock()

	pending := 0
	for key := range s.entries {
		pending = s.markDeletedLocked(key)
	}

	s.entries = make(map[K]V, 64)

	s.mu.Unlock()

	s.notify(pending)
}

// markDirtyLocked records a change for the next flush. Caller must hold mu.
// Returns the pending-change count (0 when no persistence is attached).
func (s *Store[K, V]) markDirtyLocked(key K) int {
	if s.persistence == nil {
		return 0
	}

	delete(s.deleted, key)
	s.dirty[key] = struct{}{}

	return len(s.dirty) + len(s.deleted)
}

// markDeletedLocked records a deletion for the next flush. Caller must hold
// mu. Returns the pending-change count (0 when no persistence is attached).
func (s *Store[K, V]) markDeletedLocked(key K) int {
	if s.persistence == nil {
		return 0
	}

	delete(s.dirty, key)
	s.deleted[key] = struct{}{}

	return len(s.dirty) + len(s.deleted)
}

// notify nudges the flush loop about pending changes; never blocks.
func (s *Store[K, V]) notify(pending int) {
	if pending <= 0 {
		return
	}

	select {
	case s.poke <- struct{}{}:
	default:
	}

	if pending >= s.maxPending {
		select {
		case s.force <- struct{}{}:
		default:
		}
	}
}

// run is the flush loop: wait for a change, debounce flushInterval (cut short
// when maxPending forces an immediate flush), then flush. Failed batches are
// re-marked by Flush and retried on the next round.
func (s *Store[K, V]) run() {
	defer s.wg.Done()

	for {
		immediate := false

		select {
		case <-s.ctx.Done():
			return
		case <-s.poke:
		case <-s.force:
			immediate = true
		}

		if !immediate {
			timer := time.NewTimer(s.flushInterval)

			select {
			case <-s.ctx.Done():
				timer.Stop()

				return
			case <-s.force:
				timer.Stop()
			case <-timer.C:
			}
		}

		if err := s.Flush(); err != nil {
			s.log.WithError(err).Warn("memstore flush failed; batch will be retried on next flush")
		}
	}
}
