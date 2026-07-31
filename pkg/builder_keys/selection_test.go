package builder_keys

import (
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/config"
)

// activeRegistry builds a registry with `count` active keys, each holding
// balanceGwei.
func activeRegistry(t *testing.T, count, balanceGwei uint64) *Registry {
	t.Helper()

	registry := testRegistry(t, config.BuilderKeysConfig{
		TargetCount: count, DiscoveryGap: 1, MaxIndex: 64,
	})

	for keyIndex := range count {
		_, err := registry.PrimeKeyState(keyIndex, func(state *State) {
			state.Status = StatusActive
			state.HasBuilderIndex = true
			state.BuilderIndex = state.KeyIndex + 100
			state.Balance = balanceGwei
			state.EffectiveBalance = balanceGwei
		})
		require.NoError(t, err)
	}

	return registry
}

func selectedIndices(keys []*Key) []uint64 {
	indices := make([]uint64, 0, len(keys))
	for _, key := range keys {
		indices = append(indices, key.KeyIndex())
	}

	return indices
}

func TestSelectForBidStrategies(t *testing.T) {
	registry := activeRegistry(t, 4, 1_000)

	tests := []struct {
		name     string
		strategy string
		slot     phase0.Slot
		want     []uint64
	}{
		{name: "single always takes the lowest index", strategy: StrategySingle, slot: 7, want: []uint64{0}},
		{name: "round robin rotates by slot", strategy: StrategyRoundRobin, slot: 6, want: []uint64{2, 3, 0, 1}},
		{name: "round robin wraps", strategy: StrategyRoundRobin, slot: 8, want: []uint64{0, 1, 2, 3}},
		{name: "unknown falls back to round robin", strategy: "bogus", slot: 5, want: []uint64{1, 2, 3, 0}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := registry.SelectForBid(test.slot, SelectRequest{Strategy: test.strategy})
			require.Equal(t, test.want, selectedIndices(got))
		})
	}
}

func TestSelectForBidRandomIsDeterministicPerSlot(t *testing.T) {
	registry := activeRegistry(t, 5, 1_000)

	first := selectedIndices(registry.SelectForBid(11, SelectRequest{Strategy: StrategyRandom}))
	again := selectedIndices(registry.SelectForBid(11, SelectRequest{Strategy: StrategyRandom}))
	require.Equal(t, first, again, "the same slot must yield the same assignment")

	other := selectedIndices(registry.SelectForBid(12, SelectRequest{Strategy: StrategyRandom}))
	require.ElementsMatch(t, first, other)
}

func TestSelectForBidLeastUsedPrefersIdleKeys(t *testing.T) {
	registry := activeRegistry(t, 3, 1_000)

	registry.RecordBid(0)
	registry.RecordBid(0)
	registry.RecordBid(1)

	got := registry.SelectForBid(1, SelectRequest{Strategy: StrategyLeastUsed})
	require.Equal(t, []uint64{2, 1, 0}, selectedIndices(got))
}

// A key already committed to a candidate must not be handed out again: its
// second bid for the slot would be ignored by the gossip rules.
func TestSelectForBidExcludesCommittedKeys(t *testing.T) {
	registry := activeRegistry(t, 3, 1_000)

	got := registry.SelectForBid(0, SelectRequest{
		Strategy: StrategyRoundRobin,
		Exclude:  map[uint64]struct{}{0: {}, 2: {}},
	})

	require.Equal(t, []uint64{1}, selectedIndices(got))
}

// Balance is a preference, not a filter: an underfunded key is still offered
// once nothing else can cover the bid, because deliberately underfunded bids
// are a scenario buildoor exists to test.
func TestSelectForBidPrefersFundedKeys(t *testing.T) {
	registry := activeRegistry(t, 3, 1_000)

	_, err := registry.PrimeKeyState(1, func(state *State) {
		state.Status = StatusActive
		state.HasBuilderIndex = true
		state.BuilderIndex = 101
		state.Balance = 10
		state.EffectiveBalance = 10
	})
	require.NoError(t, err)

	got := registry.SelectForBid(0, SelectRequest{Strategy: StrategyRoundRobin, RequiredGwei: 500})
	require.Equal(t, []uint64{0, 2, 1}, selectedIndices(got), "the underfunded key sorts last")

	// With only the underfunded key left it is still offered.
	got = registry.SelectForBid(0, SelectRequest{
		Strategy:     StrategyRoundRobin,
		RequiredGwei: 500,
		Exclude:      map[uint64]struct{}{0: {}, 2: {}},
	})
	require.Equal(t, []uint64{1}, selectedIndices(got))
}

func TestSelectForBidSkipsInactiveKeys(t *testing.T) {
	registry := activeRegistry(t, 3, 1_000)

	for keyIndex, status := range map[uint64]Status{1: StatusExiting, 2: StatusPending} {
		_, err := registry.PrimeKeyState(keyIndex, func(state *State) {
			state.Status = status
		})
		require.NoError(t, err)
	}

	got := registry.SelectForBid(0, SelectRequest{Strategy: StrategyRoundRobin})
	require.Equal(t, []uint64{0}, selectedIndices(got))

	// Nothing active at all yields no key rather than an unusable one.
	_, err := registry.PrimeKeyState(0, func(state *State) { state.Status = StatusExited })
	require.NoError(t, err)

	require.Nil(t, registry.SelectForBid(0, SelectRequest{Strategy: StrategyRoundRobin}))
}

func TestSelectForBidHonoursCount(t *testing.T) {
	registry := activeRegistry(t, 4, 1_000)

	got := registry.SelectForBid(0, SelectRequest{Strategy: StrategyRoundRobin, Count: 2})
	require.Len(t, got, 2)
}
