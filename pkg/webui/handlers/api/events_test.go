package api

import (
	"sync"
	"testing"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestEventStreamManager builds a manager suitable for exercising the
// broadcast / replay-cache paths, which touch no injected service.
func newTestEventStreamManager() *EventStreamManager {
	return NewEventStreamManager(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
}

func slotEvent(slot uint64) *StreamEvent {
	return &StreamEvent{
		Type:      EventTypeSlotStart,
		Timestamp: time.Now().UnixMilli(),
		Data:      SlotStartEvent{Slot: slot},
	}
}

// drain reads every immediately available event from the channel.
func drain(ch chan *StreamEvent) []*StreamEvent {
	events := make([]*StreamEvent, 0, len(ch))

	for {
		select {
		case ev := <-ch:
			events = append(events, ev)
		default:
			return events
		}
	}
}

func TestEventStreamManagerReplayOnRegister(t *testing.T) {
	m := newTestEventStreamManager()

	for slot := uint64(1); slot <= 3; slot++ {
		m.broadcastForSlot(phase0.Slot(slot), slotEvent(slot))
	}

	ch := m.RegisterClient()
	defer m.RemoveClient(ch)

	replayed := drain(ch)
	require.Len(t, replayed, 3, "all cached events must be prefilled")

	var lastSeq uint64

	for i, ev := range replayed {
		data, ok := ev.Data.(SlotStartEvent)
		require.True(t, ok)
		assert.Equal(t, uint64(i+1), data.Slot, "replay must preserve broadcast order")
		assert.Greater(t, ev.Seq, lastSeq, "sequence numbers must be strictly increasing")
		lastSeq = ev.Seq
	}

	// A live event after registration arrives exactly once, after the replay.
	m.broadcastForSlot(phase0.Slot(4), slotEvent(4))

	live := drain(ch)
	require.Len(t, live, 1)
	assert.Greater(t, live[0].Seq, lastSeq)
}

func TestEventStreamManagerBroadcastNotCached(t *testing.T) {
	m := newTestEventStreamManager()

	m.Broadcast(&StreamEvent{Type: EventTypeStats, Timestamp: time.Now().UnixMilli()})

	ch := m.RegisterClient()
	defer m.RemoveClient(ch)

	assert.Empty(t, drain(ch), "non-slot broadcasts must not be replayed")
}

func TestEventStreamManagerPruneEventCache(t *testing.T) {
	tests := []struct {
		name        string
		cachedSlots []uint64
		currentSlot uint64
		wantSlots   []uint64
	}{
		{
			name:        "below window keeps everything",
			cachedSlots: []uint64{0, 1, 2},
			currentSlot: 3,
			wantSlots:   []uint64{0, 1, 2},
		},
		{
			name:        "drops slots outside the window",
			cachedSlots: []uint64{5, 6, 7, 8, 9, 10},
			currentSlot: 10,
			wantSlots:   []uint64{6, 7, 8, 9, 10},
		},
		{
			name:        "keeps future-slot events",
			cachedSlots: []uint64{100, 106},
			currentSlot: 105,
			wantSlots:   []uint64{106},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestEventStreamManager()

			for _, slot := range tt.cachedSlots {
				m.broadcastForSlot(phase0.Slot(slot), slotEvent(slot))
			}

			m.pruneEventCache(phase0.Slot(tt.currentSlot))

			ch := m.RegisterClient()
			defer m.RemoveClient(ch)

			replayed := drain(ch)
			gotSlots := make([]uint64, 0, len(replayed))

			for _, ev := range replayed {
				data, ok := ev.Data.(SlotStartEvent)
				require.True(t, ok)
				gotSlots = append(gotSlots, data.Slot)
			}

			assert.Equal(t, tt.wantSlots, gotSlots)
		})
	}
}

func TestEventStreamManagerCacheCap(t *testing.T) {
	m := newTestEventStreamManager()

	for i := 0; i < maxCachedEvents+100; i++ {
		m.broadcastForSlot(phase0.Slot(1), slotEvent(uint64(i)))
	}

	m.mu.Lock()
	size := len(m.eventCache)
	oldest, ok := m.eventCache[0].event.Data.(SlotStartEvent)
	m.mu.Unlock()

	require.True(t, ok)
	assert.Equal(t, maxCachedEvents, size, "cache must be capped")
	assert.Equal(t, uint64(100), oldest.Slot, "oldest entries must be dropped first")
}

func TestEventStreamManagerConcurrentBroadcastAndRegister(t *testing.T) {
	m := newTestEventStreamManager()

	var wg sync.WaitGroup

	for worker := 0; worker < 4; worker++ {
		wg.Add(1)

		go func(base uint64) {
			defer wg.Done()

			for i := uint64(0); i < 100; i++ {
				m.broadcastForSlot(phase0.Slot(base+i), slotEvent(base+i))
			}
		}(uint64(worker) * 1000)
	}

	channels := make([]chan *StreamEvent, 0, 8)

	var chMu sync.Mutex

	for reader := 0; reader < 8; reader++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			ch := m.RegisterClient()

			chMu.Lock()
			channels = append(channels, ch)
			chMu.Unlock()

			// Every client's stream must have strictly increasing seqs:
			// replay prefill and live events form one ordered sequence.
			var lastSeq uint64

			for _, ev := range drain(ch) {
				assert.Greater(t, ev.Seq, lastSeq)
				lastSeq = ev.Seq
			}
		}()
	}

	wg.Wait()

	for _, ch := range channels {
		m.RemoveClient(ch)
	}
}
