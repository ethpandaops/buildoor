package chain

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/config"
)

// The settings validation keeps its own list of candidate keys (the config
// package cannot import this one); the two must not drift.
func TestConfigKnowsEveryCandidateKey(t *testing.T) {
	keys := make([]string, 0, len(AllCandidateKeys))
	for _, key := range AllCandidateKeys {
		keys = append(keys, string(key))
	}

	require.ElementsMatch(t, keys, config.CandidateKeys)
}
