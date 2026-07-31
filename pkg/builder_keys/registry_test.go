package builder_keys

import (
	"fmt"
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

const testEntryKey = "3f2b8e1c9d4a6f70b5c8e2a1d7943f6058ac2be91d3f5074a6b8c2e1d9f30475"

func testRegistry(t *testing.T, keys config.BuilderKeysConfig) *Registry {
	t.Helper()

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	registry, err := NewRegistry(&config.Config{BuilderKeys: keys}, testEntryKey, log)
	require.NoError(t, err)

	return registry
}

func TestRegistryPrimaryIsTheEntryKey(t *testing.T) {
	registry := testRegistry(t, config.BuilderKeysConfig{TargetCount: 4})

	entrySigner, err := signer.NewBLSSigner(testEntryKey)
	require.NoError(t, err)

	primary := registry.Primary()
	require.NotNil(t, primary)
	require.Equal(t, uint64(0), primary.KeyIndex())
	require.Equal(t, entrySigner.PublicKey(), primary.Pubkey())
}

func TestRegistryDerivesUpToTarget(t *testing.T) {
	registry := testRegistry(t, config.BuilderKeysConfig{TargetCount: 5, DiscoveryGap: 3, MaxIndex: 100})
	registry.Refresh()

	keys := registry.Keys()
	// The scan reaches past the target looking for used keys, but unused
	// indices above it are not tracked: the fleet is the size of the target.
	require.Len(t, keys, 5)

	pubkeys := map[phase0.BLSPubKey]uint64{}
	for _, key := range keys {
		previous, seen := pubkeys[key.Pubkey()]
		require.False(t, seen, "key %d duplicates key %d", key.KeyIndex(), previous)

		pubkeys[key.Pubkey()] = key.KeyIndex()
		require.Equal(t, StatusUnused, key.Status())
	}
}

// Discovery must keep scanning past the target for keys we deposited in an
// earlier run, and stop only after a full gap of never-used indices.
func TestRegistryDiscoveryFindsUsedKeysAboveTarget(t *testing.T) {
	registry := testRegistry(t, config.BuilderKeysConfig{TargetCount: 1, DiscoveryGap: 4, MaxIndex: 100})

	// A key deposited in a previous run, well above the current target.
	registry.MarkDepositSubmitted(9)

	registry.Refresh()

	state := registry.State(9)
	require.NotNil(t, state)
	require.Equal(t, StatusDepositing, state.Status, "a just-submitted deposit stays in flight")
	require.Equal(t, uint32(1), state.UseCount)

	// The fleet is the target (key 0) plus the key we used before, not the whole
	// scanned range.
	indices := make([]uint64, 0, 2)
	for _, key := range registry.Keys() {
		indices = append(indices, key.KeyIndex())
	}

	require.Equal(t, []uint64{0, 9}, indices)
}

// A used key far above the target must survive every later refresh: the scan
// floor follows the highest key we know about, not the target.
func TestRegistryKeepsUsedKeysAcrossRefreshes(t *testing.T) {
	registry := testRegistry(t, config.BuilderKeysConfig{TargetCount: 1, DiscoveryGap: 2, MaxIndex: 100})

	registry.MarkDepositSubmitted(7)

	for range 3 {
		registry.Refresh()
	}

	state := registry.State(7)
	require.NotNil(t, state)
	require.Equal(t, StatusDepositing, state.Status)
	require.Len(t, registry.Keys(), 2)
}

func TestRegistryRespectsMaxIndex(t *testing.T) {
	registry := testRegistry(t, config.BuilderKeysConfig{TargetCount: 20, DiscoveryGap: 5, MaxIndex: 3})
	registry.Refresh()

	require.Len(t, registry.Keys(), 4, "the target is clamped to the cap, inclusive")

	_, err := registry.Key(4)
	require.ErrorContains(t, err, "derivation cap")
}

func TestRegistryDepositCandidatePrefersLowestReusableIndex(t *testing.T) {
	registry := testRegistry(t, config.BuilderKeysConfig{TargetCount: 4, DiscoveryGap: 2, MaxIndex: 100})
	registry.Refresh()

	candidate := registry.NextDepositCandidate()
	require.NotNil(t, candidate)
	require.Equal(t, uint64(0), candidate.KeyIndex())
}

func TestRegistryStatusResolution(t *testing.T) {
	const finalized = uint64(10)

	active := &chain.BuilderInfo{Index: 3, DepositEpoch: 5, WithdrawableEpoch: chain.FarFutureEpoch}
	unfinalized := &chain.BuilderInfo{Index: 4, DepositEpoch: 12, WithdrawableEpoch: chain.FarFutureEpoch}
	exiting := &chain.BuilderInfo{Index: 5, DepositEpoch: 5, WithdrawableEpoch: 40}
	exited := &chain.BuilderInfo{Index: 6, DepositEpoch: 5, WithdrawableEpoch: 20}

	tests := []struct {
		name            string
		info            *chain.BuilderInfo
		depositInFlight bool
		useCount        uint32
		want            Status
	}{
		{name: "never used", want: StatusUnused},
		{name: "deposit in flight", depositInFlight: true, want: StatusDepositing},
		{
			name:            "first deposit in flight outranks its own use count",
			depositInFlight: true,
			useCount:        1,
			want:            StatusDepositing,
		},
		{name: "used and gone from the registry", useCount: 2, want: StatusWithdrawn},
		{name: "registered but unfinalized", info: unfinalized, want: StatusPending},
		{name: "registered and finalized", info: active, want: StatusActive},
		{name: "exit initiated", info: exiting, want: StatusExiting},
		{name: "withdrawable epoch reached", info: exited, want: StatusExited},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := resolveStatus(test.info, test.depositInFlight, test.useCount, phase0.Epoch(25), finalized)
			require.Equal(t, test.want, got)
		})
	}
}

func TestEffectiveBalanceFloorsAtZero(t *testing.T) {
	tests := []struct {
		name  string
		state State
		want  uint64
	}{
		{name: "plain balance", state: State{Balance: 100}, want: 100},
		{name: "credit", state: State{Balance: 100, BalanceAdjustment: 25}, want: 125},
		{name: "debit", state: State{Balance: 100, BalanceAdjustment: -40}, want: 60},
		{name: "debit past zero", state: State{Balance: 10, BalanceAdjustment: -40}, want: 0},
		{name: "pending payments", state: State{Balance: 100, PendingPayments: 30}, want: 70},
		{name: "pending payments exceed balance", state: State{Balance: 10, PendingPayments: 30}, want: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, effectiveBalance(&test.state))
		})
	}
}

func TestStateReadyRequiresActiveAndFunded(t *testing.T) {
	require.True(t, (&State{Status: StatusActive, EffectiveBalance: 100}).Ready(100))
	require.False(t, (&State{Status: StatusActive, EffectiveBalance: 99}).Ready(100))
	require.False(t, (&State{Status: StatusPending, EffectiveBalance: 1000}).Ready(100))
	require.False(t, (&State{Status: StatusExiting, EffectiveBalance: 1000}).Ready(100))
}

func TestRegistryAggregateCountsStatuses(t *testing.T) {
	registry := testRegistry(t, config.BuilderKeysConfig{TargetCount: 3, DiscoveryGap: 1, MaxIndex: 100})
	registry.Refresh()

	aggregate := registry.Aggregate()
	require.Equal(t, uint64(3), aggregate.Target)
	require.Equal(t, uint64(0), aggregate.Managed)
	require.Equal(t, uint64(len(registry.Keys())), aggregate.Unused)
	require.False(t, registry.AnyActive())
}

func TestRegistryChangeEventsOnStatusTransition(t *testing.T) {
	registry := testRegistry(t, config.BuilderKeysConfig{TargetCount: 2, DiscoveryGap: 1, MaxIndex: 100})
	registry.Refresh()

	sub := registry.SubscribeChanges(4, false)
	defer sub.Unsubscribe()

	registry.MarkDepositSubmitted(1)

	select {
	case event := <-sub.Channel():
		require.NotNil(t, event)

		var found bool

		for _, state := range event.States {
			if state.KeyIndex == 1 {
				found = true

				require.Equal(t, StatusDepositing, state.Status)
			}
		}

		require.True(t, found, "change event must carry the whole key set")
	default:
		t.Fatal("expected a change event after a deposit was recorded")
	}
}

func TestRegistryRefreshIsIdempotent(t *testing.T) {
	registry := testRegistry(t, config.BuilderKeysConfig{TargetCount: 2, DiscoveryGap: 2, MaxIndex: 100})
	registry.Refresh()

	before := len(registry.Keys())

	sub := registry.SubscribeChanges(4, false)
	defer sub.Unsubscribe()

	registry.Refresh()
	registry.Refresh()

	require.Len(t, registry.Keys(), before)

	select {
	case event := <-sub.Channel():
		t.Fatalf("unchanged refresh must not fire an event, got %v", event)
	default:
	}
}

func TestRegistryLookupsByPubkey(t *testing.T) {
	registry := testRegistry(t, config.BuilderKeysConfig{TargetCount: 3, DiscoveryGap: 1, MaxIndex: 100})
	registry.Refresh()

	for _, key := range registry.Keys() {
		require.Same(t, key, registry.ByPubkey(key.Pubkey()))
	}

	require.Nil(t, registry.ByPubkey(phase0.BLSPubKey{0xde, 0xad}))
	require.Nil(t, registry.ByBuilderIndex(42), "no key is registered on chain in this fixture")
}

func TestKeyStringIdentifiesTheDerivationIndex(t *testing.T) {
	registry := testRegistry(t, config.BuilderKeysConfig{TargetCount: 2})

	key, err := registry.Key(1)
	require.NoError(t, err)

	pubkey := key.Pubkey()
	require.Equal(t, fmt.Sprintf("#1/%x", pubkey[:4]), key.String())
}

// Exits pick the highest index that can actually exit: the beacon chain silently
// ignores an exit request while the builder still owes a payment, so a key with
// pending payments must be skipped rather than burning the queue fee.
func TestRegistryExitCandidateSkipsPendingPayments(t *testing.T) {
	registry := testRegistry(t, config.BuilderKeysConfig{TargetCount: 4, DiscoveryGap: 1, MaxIndex: 32})

	active := func(state *State) {
		state.Status = StatusActive
		state.HasBuilderIndex = true
		state.BuilderIndex = state.KeyIndex + 10
	}

	for keyIndex := range uint64(3) {
		_, err := registry.PrimeKeyState(keyIndex, active)
		require.NoError(t, err)
	}

	// The highest key still owes a payment.
	_, err := registry.PrimeKeyState(2, func(state *State) {
		active(state)
		state.PendingPayments = 500
	})
	require.NoError(t, err)

	candidate := registry.NextExitCandidate()
	require.NotNil(t, candidate)
	require.Equal(t, uint64(1), candidate.KeyIndex())

	// Once it settles, it is the one to go.
	_, err = registry.PrimeKeyState(2, func(state *State) {
		active(state)
		state.PendingPayments = 0
	})
	require.NoError(t, err)

	candidate = registry.NextExitCandidate()
	require.NotNil(t, candidate)
	require.Equal(t, uint64(2), candidate.KeyIndex())
}

// Deposits reuse the lowest withdrawn index instead of extending the set, which
// is what keeps the highest derivation index bounded across target ramps.
func TestRegistryDepositCandidateReusesWithdrawnKeys(t *testing.T) {
	registry := testRegistry(t, config.BuilderKeysConfig{TargetCount: 3, DiscoveryGap: 1, MaxIndex: 32})

	for keyIndex := range uint64(3) {
		_, err := registry.PrimeKeyState(keyIndex, func(state *State) {
			state.Status = StatusActive
			state.HasBuilderIndex = true
			state.BuilderIndex = state.KeyIndex + 10
		})
		require.NoError(t, err)
	}

	// With every key in use the next deposit extends the set.
	require.Equal(t, uint64(3), registry.NextDepositCandidate().KeyIndex())

	// Once key 1 leaves the registry it becomes the preferred candidate again.
	_, err := registry.PrimeKeyState(1, func(state *State) {
		state.Status = StatusWithdrawn
		state.HasBuilderIndex = false
		state.UseCount = 1
	})
	require.NoError(t, err)

	require.Equal(t, uint64(1), registry.NextDepositCandidate().KeyIndex())
}

func TestStatusClassification(t *testing.T) {
	managed := map[Status]bool{
		StatusUnused: false, StatusDepositing: true, StatusPending: true,
		StatusActive: true, StatusExiting: false, StatusExited: false,
		StatusWithdrawn: false,
	}
	depositable := map[Status]bool{
		StatusUnused: true, StatusDepositing: false, StatusPending: false,
		StatusActive: false, StatusExiting: false, StatusExited: false,
		StatusWithdrawn: true,
	}

	for status, want := range managed {
		require.Equal(t, want, status.Managed(), "Managed(%s)", status)
	}

	for status, want := range depositable {
		require.Equal(t, want, status.Depositable(), "Depositable(%s)", status)
	}
}
