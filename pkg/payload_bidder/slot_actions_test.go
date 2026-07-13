package payload_bidder

import (
	"path/filepath"
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/db"
)

func withhold() *SlotAction {
	return &SlotAction{Reveal: RevealActionWithhold}
}

func TestSlotActionsStore_ReplaceFuture(t *testing.T) {
	store := NewSlotActionsStore()

	// Seed: 5 (past), 10 (current), 15 and 20 (future) from slot 0's view.
	store.ReplaceFuture(map[phase0.Slot]*SlotAction{
		5: withhold(), 10: withhold(), 15: withhold(), 20: withhold(),
	}, 0)
	require.Equal(t, 4, len(store.Snapshot()))

	// Replace at current slot 10: 5 is stale (pruned), 10 has started
	// (immutable, kept), 15 is dropped (not in the new set), 20 is kept,
	// 25 is added.
	effective := store.ReplaceFuture(map[phase0.Slot]*SlotAction{
		20: withhold(), 25: withhold(),
	}, 10)

	assert.Equal(t, map[phase0.Slot]*SlotAction{
		10: withhold(), 20: withhold(), 25: withhold(),
	}, effective)
	assert.Equal(t, effective, store.Snapshot())
}

func TestSlotActionsStore_ReplaceFutureClears(t *testing.T) {
	store := NewSlotActionsStore()
	store.ReplaceFuture(map[phase0.Slot]*SlotAction{15: withhold(), 20: withhold()}, 10)

	// An empty set clears all pending future actions.
	effective := store.ReplaceFuture(map[phase0.Slot]*SlotAction{}, 10)
	assert.Empty(t, effective)
	assert.Empty(t, store.Snapshot())
}

func TestSlotActionsStore_ReplaceFutureKeepsStartedSlot(t *testing.T) {
	store := NewSlotActionsStore()
	store.ReplaceFuture(map[phase0.Slot]*SlotAction{15: withhold()}, 10)

	// Slot 15 has started: clearing must not remove it (slot-boundary lock).
	effective := store.ReplaceFuture(map[phase0.Slot]*SlotAction{}, 15)
	require.Len(t, effective, 1)
	action, ok := store.Get(15)
	require.True(t, ok)
	assert.Equal(t, RevealActionWithhold, action.Reveal)
}

func TestSlotActionsStore_ReplaceFutureSkipsNonFutureEntries(t *testing.T) {
	store := NewSlotActionsStore()

	// Defensive: entries at or before the current slot are never stored.
	effective := store.ReplaceFuture(map[phase0.Slot]*SlotAction{
		5: withhold(), 10: withhold(), 15: withhold(),
	}, 10)

	assert.Equal(t, map[phase0.Slot]*SlotAction{15: withhold()}, effective)
}

func TestSlotActionsStore_PruneBefore(t *testing.T) {
	store := NewSlotActionsStore()
	store.ReplaceFuture(map[phase0.Slot]*SlotAction{5: withhold(), 10: withhold(), 15: withhold()}, 0)

	// The current slot's action is kept while in flight; older ones drop.
	store.PruneBefore(10)

	assert.Equal(t, map[phase0.Slot]*SlotAction{
		10: withhold(), 15: withhold(),
	}, store.Snapshot())
}

func TestSlotActionCodec_RoundTrip(t *testing.T) {
	codec := SlotActionCodec{}

	key := codec.EncodeKey(384)
	assert.Equal(t, "384", key)

	slot, err := codec.DecodeKey(key)
	require.NoError(t, err)
	assert.Equal(t, phase0.Slot(384), slot)

	_, err = codec.DecodeKey("not-a-slot")
	require.Error(t, err)

	value, err := codec.EncodeValue(withhold())
	require.NoError(t, err)

	decoded, err := codec.DecodeValue(value)
	require.NoError(t, err)
	assert.Equal(t, RevealActionWithhold, decoded.Reveal)

	_, err = codec.EncodeValue(nil)
	require.Error(t, err)
}

func TestSlotActionsStore_Persistence(t *testing.T) {
	logger, _ := newHookedLogger()

	stateDB := db.NewDatabase(&db.Config{File: filepath.Join(t.TempDir(), "state.db")}, logger)
	require.NoError(t, stateDB.Init())

	defer func() {
		require.NoError(t, stateDB.Close())
	}()

	store := NewSlotActionsStore()
	store.SetPersistence(t.Context(), stateDB, logger)
	store.ReplaceFuture(map[phase0.Slot]*SlotAction{384: withhold(), 416: withhold()}, 10)

	// Stop flushes; the actions land in the kv_store's slot_actions namespace.
	store.Stop()

	persisted, err := db.NewKVPersistence(stateDB, SlotActionsNamespace, SlotActionCodec{}).Load()
	require.NoError(t, err)
	require.Len(t, persisted, 2)
	assert.Equal(t, RevealActionWithhold, persisted[phase0.Slot(384)].Reveal)

	// A fresh store rehydrates on persistence attach; stale entries are
	// pruned like the startup sequence does, and the deletions persist.
	rehydrated := NewSlotActionsStore()
	rehydrated.SetPersistence(t.Context(), stateDB, logger)
	rehydrated.PruneBefore(400)

	action, ok := rehydrated.Get(416)
	require.True(t, ok)
	assert.Equal(t, RevealActionWithhold, action.Reveal)
	_, ok = rehydrated.Get(384)
	assert.False(t, ok, "stale entries must be pruned after rehydration")

	rehydrated.Stop()

	persisted, err = db.NewKVPersistence(stateDB, SlotActionsNamespace, SlotActionCodec{}).Load()
	require.NoError(t, err)
	require.Len(t, persisted, 1, "the prune must propagate to the kv_store")
}
