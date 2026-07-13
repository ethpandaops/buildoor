package payload_bidder

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/memstore"
)

// RevealActionWithhold instructs the reveal service to intentionally NOT
// publish the payload envelope for a configured slot (deterministic fault
// injection for Gloas/ePBS testing). Building and bidding are unaffected.
const RevealActionWithhold = "withhold"

// SlotActionsNamespace is the kv_store namespace holding the persisted
// per-slot actions.
const SlotActionsNamespace = "slot_actions"

// SlotAction is the set of fault-injection actions configured for one exact
// slot. The JSON tags are the wire shape of the config API and the persisted
// kv_store value.
type SlotAction struct {
	Reveal string `json:"reveal,omitempty"` // only RevealActionWithhold today
}

// SlotActionsStore holds the per-slot fault-injection actions configured via
// the runtime config API. Writes replace the complete set of pending FUTURE
// actions; an action whose slot has started is immutable and is only dropped
// once it is stale. The reveal service reads it on every schedule decision.
type SlotActionsStore struct {
	// mu makes a complete replacement atomic to readers and to concurrent API
	// requests. memstore protects each individual operation, but ReplaceFuture
	// is intentionally a prune-plus-put transaction at this layer.
	mu    sync.RWMutex
	store *memstore.Store[phase0.Slot, *SlotAction]
}

// SlotActionCodec translates the slot-action store's entries to their
// persisted form: decimal slot string keys, JSON-encoded values.
type SlotActionCodec struct{}

var _ db.KVCodec[phase0.Slot, *SlotAction] = SlotActionCodec{}

// NewSlotActionsStore creates an empty SlotActionsStore.
func NewSlotActionsStore() *SlotActionsStore {
	return &SlotActionsStore{
		store: memstore.New[phase0.Slot, *SlotAction](),
	}
}

// SetPersistence attaches the optional state-db so configured actions survive
// restarts: previously persisted entries are loaded and future changes are
// flushed (buffered) into the store's kv_store namespace. Call Stop before
// the state-db closes.
func (s *SlotActionsStore) SetPersistence(ctx context.Context, stateDB *db.Database,
	log logrus.FieldLogger) {
	if stateDB == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.store.SetPersistence(ctx,
		db.NewKVPersistence(stateDB, SlotActionsNamespace, SlotActionCodec{}),
		log.WithField("component", "slot-actions-store"))
}

// Stop flushes pending changes and stops the persistence flush loop. No-op
// when no persistence is attached.
func (s *SlotActionsStore) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.store.Stop()
}

// Get returns the action configured for a slot and whether one was found.
func (s *SlotActionsStore) Get(slot phase0.Slot) (*SlotAction, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.store.Get(slot)
}

// ReplaceFuture replaces the complete set of pending future actions with the
// given set (an empty map clears it). Entries whose slot has started
// (slot <= currentSlot) are immutable: existing ones are kept, new ones are
// never stored (callers validate this; the check here is defensive). Stale
// entries (slot < currentSlot) are pruned. Returns the effective stored
// snapshot.
func (s *SlotActionsStore) ReplaceFuture(actions map[phase0.Slot]*SlotAction,
	currentSlot phase0.Slot) map[phase0.Slot]*SlotAction {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.store.Prune(func(slot phase0.Slot) bool {
		if slot < currentSlot {
			return true // stale
		}

		if slot == currentSlot {
			return false // in-flight, immutable
		}

		_, keep := actions[slot]

		return !keep // future entry not part of the new set
	})

	for slot, action := range actions {
		if slot > currentSlot && action != nil {
			s.store.Put(slot, action)
		}
	}

	return s.snapshotLocked()
}

// Snapshot returns a copy of all stored actions keyed by slot.
func (s *SlotActionsStore) Snapshot() map[phase0.Slot]*SlotAction {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.snapshotLocked()
}

func (s *SlotActionsStore) snapshotLocked() map[phase0.Slot]*SlotAction {
	return s.store.Entries()
}

// PruneBefore drops every action whose slot is older than currentSlot (the
// current slot's action is kept while the slot is in flight). Used to clear
// stale entries rehydrated from a previous run.
func (s *SlotActionsStore) PruneBefore(currentSlot phase0.Slot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.store.Prune(func(slot phase0.Slot) bool {
		return slot < currentSlot
	})
}

// EncodeKey encodes a slot as its decimal string form.
func (SlotActionCodec) EncodeKey(slot phase0.Slot) string {
	return strconv.FormatUint(uint64(slot), 10)
}

// DecodeKey parses a decimal slot string.
func (SlotActionCodec) DecodeKey(key string) (phase0.Slot, error) {
	slot, err := strconv.ParseUint(key, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid slot action key %q: %w", key, err)
	}

	return phase0.Slot(slot), nil
}

// EncodeValue JSON-encodes a slot action.
func (SlotActionCodec) EncodeValue(action *SlotAction) ([]byte, error) {
	if action == nil {
		return nil, fmt.Errorf("cannot encode nil slot action")
	}

	return json.Marshal(action)
}

// DecodeValue JSON-decodes a slot action.
func (SlotActionCodec) DecodeValue(value []byte) (*SlotAction, error) {
	action := &SlotAction{}
	if err := json.Unmarshal(value, action); err != nil {
		return nil, fmt.Errorf("failed to decode slot action: %w", err)
	}

	return action, nil
}
