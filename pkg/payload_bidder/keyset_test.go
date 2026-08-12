package payload_bidder

import (
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/builder_keys"
	"github.com/ethpandaops/buildoor/pkg/config"
)

// testEntryPrivkey roots the key set used by the tests in this package.
const testEntryPrivkey = "0000000000000000000000000000000000000000000000000000000000000001"

// newTestKeyRegistry builds a key registry whose keys are primed as active
// builders at the given on-chain builder indices (key i -> builderIndices[i]).
func newTestKeyRegistry(t *testing.T, builderIndices ...uint64) *builder_keys.Registry {
	t.Helper()

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	cfg := &config.Config{BuilderKeys: config.BuilderKeysConfig{
		TargetCount:  uint64(len(builderIndices)),
		DiscoveryGap: 1,
		MaxIndex:     32,
	}}

	registry, err := builder_keys.NewRegistry(cfg, testEntryPrivkey, log)
	require.NoError(t, err)

	for keyIndex, builderIndex := range builderIndices {
		_, err := registry.PrimeKeyState(uint64(keyIndex), func(state *builder_keys.State) {
			state.Status = builder_keys.StatusActive
			state.BuilderIndex = builderIndex
			state.HasBuilderIndex = true
			state.Balance = 1_000_000_000_000
			state.EffectiveBalance = 1_000_000_000_000
		})
		require.NoError(t, err)
	}

	return registry
}
