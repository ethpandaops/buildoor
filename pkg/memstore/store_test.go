package memstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// persistBatch captures a single PersistBatch call.
type persistBatch struct {
	upserts map[string]int
	deletes []string
}

// fakePersistence is a thread-safe Persistence[string, int] recording every
// successful PersistBatch call. failNext makes the next N calls fail.
type fakePersistence struct {
	mu       sync.Mutex
	loaded   map[string]int
	loadErr  error
	failNext int
	attempts int
	batches  []persistBatch
}

func (f *fakePersistence) Load() (map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.loadErr != nil {
		return nil, f.loadErr
	}

	loaded := make(map[string]int, len(f.loaded))
	for key, value := range f.loaded {
		loaded[key] = value
	}

	return loaded, nil
}

func (f *fakePersistence) PersistBatch(upserts map[string]int, deletes []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.attempts++

	if f.failNext > 0 {
		f.failNext--

		return errors.New("persist failed")
	}

	batch := persistBatch{
		upserts: make(map[string]int, len(upserts)),
		deletes: make([]string, 0, len(deletes)),
	}
	for key, value := range upserts {
		batch.upserts[key] = value
	}

	batch.deletes = append(batch.deletes, deletes...)
	f.batches = append(f.batches, batch)

	return nil
}

func (f *fakePersistence) batchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.batches)
}

func (f *fakePersistence) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.attempts
}

func (f *fakePersistence) batch(i int) persistBatch {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.batches[i]
}

func testLog() logrus.FieldLogger {
	log := logrus.New()
	log.SetOutput(io.Discard)

	return log
}

func TestStoreWritePolicies(t *testing.T) {
	tests := []struct {
		name          string
		store         *Store[string, int]
		wantSecondPut bool
		wantValue     int
	}{
		{
			name:          "last-write-wins replaces",
			store:         New[string, int](),
			wantSecondPut: true,
			wantValue:     2,
		},
		{
			name:          "keep-existing rejects",
			store:         NewKeepExisting[string, int](),
			wantSecondPut: false,
			wantValue:     1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.True(t, tt.store.Put("key", 1))
			require.Equal(t, tt.wantSecondPut, tt.store.Put("key", 2))

			value, ok := tt.store.Get("key")
			require.True(t, ok)
			require.Equal(t, tt.wantValue, value)
			require.Equal(t, 1, tt.store.Len())
			require.True(t, tt.store.Has("key"))
		})
	}
}

func TestStoreReadersAndEntryCopy(t *testing.T) {
	store := New[string, int]()
	store.Put("a", 1)
	store.Put("b", 2)

	require.Equal(t, 2, store.Len())
	require.ElementsMatch(t, []int{1, 2}, store.Values())

	// Entries returns a copy: mutating it must not affect the store.
	entries := store.Entries()
	require.Equal(t, map[string]int{"a": 1, "b": 2}, entries)

	entries["a"] = 99
	delete(entries, "b")

	value, ok := store.Get("a")
	require.True(t, ok)
	require.Equal(t, 1, value)
	require.True(t, store.Has("b"))

	_, ok = store.Get("missing")
	require.False(t, ok)
}

func TestStoreDeletePruneClear(t *testing.T) {
	store := New[string, int]()
	store.Put("a", 1)
	store.Put("b", 2)
	store.Put("c", 3)

	store.Delete("a")
	require.False(t, store.Has("a"))
	require.Equal(t, 2, store.Len())

	// Delete of an absent key is a no-op.
	store.Delete("missing")
	require.Equal(t, 2, store.Len())

	pruned := store.Prune(func(key string) bool { return key == "b" })
	require.Equal(t, 1, pruned)
	require.False(t, store.Has("b"))
	require.True(t, store.Has("c"))

	store.Clear()
	require.Zero(t, store.Len())
}

func TestStoreFlushBatchesWrites(t *testing.T) {
	store := New[string, int]()
	store.flushInterval = 50 * time.Millisecond

	fake := &fakePersistence{}
	store.SetPersistence(context.Background(), fake, testLog())

	defer store.Stop()

	const puts = 10
	for i := range puts {
		store.Put(fmt.Sprintf("key-%d", i), i)
	}

	require.Eventually(t, func() bool { return fake.batchCount() == 1 },
		2*time.Second, 5*time.Millisecond, "expected one flushed batch")

	// All puts before the debounce elapsed must land in the single batch.
	batch := fake.batch(0)
	require.Len(t, batch.upserts, puts)
	require.Empty(t, batch.deletes)

	for i := range puts {
		require.Equal(t, i, batch.upserts[fmt.Sprintf("key-%d", i)])
	}

	// No further flushes without further changes.
	time.Sleep(3 * store.flushInterval)
	require.Equal(t, 1, fake.batchCount())
}

func TestStoreFlushMarksDeletions(t *testing.T) {
	tests := []struct {
		name        string
		remove      func(store *Store[string, int])
		wantDeletes []string
	}{
		{
			name:        "delete marks deletion",
			remove:      func(store *Store[string, int]) { store.Delete("a") },
			wantDeletes: []string{"a"},
		},
		{
			name: "prune marks deletions",
			remove: func(store *Store[string, int]) {
				pruned := store.Prune(func(key string) bool { return key != "c" })

				if pruned != 2 {
					panic("unexpected prune count")
				}
			},
			wantDeletes: []string{"a", "b"},
		},
		{
			name:        "clear marks all deletions",
			remove:      func(store *Store[string, int]) { store.Clear() },
			wantDeletes: []string{"a", "b", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := New[string, int]()
			store.flushInterval = time.Hour // flush only manually

			fake := &fakePersistence{}
			store.SetPersistence(context.Background(), fake, testLog())

			defer store.Stop()

			store.Put("a", 1)
			store.Put("b", 2)
			store.Put("c", 3)
			require.NoError(t, store.Flush())

			tt.remove(store)
			require.NoError(t, store.Flush())

			require.Equal(t, 2, fake.batchCount())

			batch := fake.batch(1)
			require.Empty(t, batch.upserts)
			require.ElementsMatch(t, tt.wantDeletes, batch.deletes)
		})
	}
}

func TestStoreRetriesFailedFlush(t *testing.T) {
	store := New[string, int]()
	store.flushInterval = 20 * time.Millisecond

	fake := &fakePersistence{failNext: 1}
	store.SetPersistence(context.Background(), fake, testLog())

	defer store.Stop()

	store.Put("key", 42)

	// First attempt fails, the batch is re-marked and retried by the loop.
	require.Eventually(t, func() bool { return fake.batchCount() == 1 },
		2*time.Second, 5*time.Millisecond, "expected a successful retry batch")

	require.GreaterOrEqual(t, fake.attemptCount(), 2)
	require.Equal(t, map[string]int{"key": 42}, fake.batch(0).upserts)
}

func TestStoreForceFlushAtMaxPending(t *testing.T) {
	store := New[string, int]()
	store.flushInterval = time.Hour // debounce would never fire
	store.maxPending = 5

	fake := &fakePersistence{}
	store.SetPersistence(context.Background(), fake, testLog())

	defer store.Stop()

	for i := range 5 {
		store.Put(fmt.Sprintf("key-%d", i), i)
	}

	require.Eventually(t, func() bool { return fake.batchCount() == 1 },
		2*time.Second, 5*time.Millisecond, "expected maxPending to force a flush")
	require.Len(t, fake.batch(0).upserts, 5)
}

func TestStoreFinalFlushOnStop(t *testing.T) {
	store := New[string, int]()
	store.flushInterval = time.Hour // the loop never flushes on its own

	fake := &fakePersistence{}
	store.SetPersistence(context.Background(), fake, testLog())

	store.Put("key", 7)
	store.Stop()

	require.Equal(t, 1, fake.batchCount())
	require.Equal(t, map[string]int{"key": 7}, fake.batch(0).upserts)

	// Stop is idempotent.
	store.Stop()
	require.Equal(t, 1, fake.batchCount())
}

func TestStoreStopWithoutPersistence(t *testing.T) {
	store := New[string, int]()
	store.Put("key", 1)

	// No persistence attached: Stop and Flush are no-ops.
	require.NoError(t, store.Flush())
	store.Stop()

	require.True(t, store.Has("key"))
}

func TestStoreLoadRehydration(t *testing.T) {
	tests := []struct {
		name  string
		store *Store[string, int]
	}{
		{name: "last-write-wins", store: New[string, int]()},
		{name: "keep-existing", store: NewKeepExisting[string, int]()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := tt.store
			store.flushInterval = 20 * time.Millisecond

			// Written before attach: must survive rehydration and be flushed.
			store.Put("pre", 100)

			fake := &fakePersistence{loaded: map[string]int{"pre": 1, "loaded": 2}}
			store.SetPersistence(context.Background(), fake, testLog())

			defer store.Stop()

			// The loaded value never overwrites the pre-attach in-memory one.
			value, ok := store.Get("pre")
			require.True(t, ok)
			require.Equal(t, 100, value)

			// Entries only present in the backing table are rehydrated.
			value, ok = store.Get("loaded")
			require.True(t, ok)
			require.Equal(t, 2, value)

			// The pre-attach write reaches the backing table on first flush.
			require.Eventually(t, func() bool { return fake.batchCount() >= 1 },
				2*time.Second, 5*time.Millisecond, "expected the pre-attach write to flush")
			require.Equal(t, map[string]int{"pre": 100}, fake.batch(0).upserts)
		})
	}
}

func TestStoreLoadErrorKeepsMemoryState(t *testing.T) {
	store := New[string, int]()
	store.flushInterval = time.Hour
	store.Put("key", 1)

	fake := &fakePersistence{loadErr: errors.New("load failed")}
	store.SetPersistence(context.Background(), fake, testLog())

	defer store.Stop()

	value, ok := store.Get("key")
	require.True(t, ok)
	require.Equal(t, 1, value)
}

func TestStoreConcurrentAccess(t *testing.T) {
	store := New[int, int]()
	store.flushInterval = 10 * time.Millisecond

	fake := &fakePersistence2{}
	store.SetPersistence(context.Background(), fake, testLog())

	var wg sync.WaitGroup

	for worker := range 4 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for i := range 100 {
				key := worker*100 + i
				store.Put(key, i)
				store.Get(key)
				store.Has(key)

				if i%10 == 0 {
					store.Delete(key)
				}
			}
		}()
	}

	wg.Wait()
	store.Stop()

	// 4 workers × 100 puts, every 10th deleted again.
	require.Equal(t, 360, store.Len())
}

// fakePersistence2 is a minimal thread-safe Persistence[int, int] for the
// concurrency smoke test.
type fakePersistence2 struct {
	mu sync.Mutex
}

func (f *fakePersistence2) Load() (map[int]int, error) {
	return map[int]int{}, nil
}

func (f *fakePersistence2) PersistBatch(_ map[int]int, _ []int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	return nil
}
