package payload_builder

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestCompareTxLists(t *testing.T) {
	a, b, c := common.Hash{1}, common.Hash{2}, common.Hash{3}

	check := CompareTxLists([]common.Hash{a, b, c}, []common.Hash{a, b, c})
	require.True(t, check.Match)
	require.Equal(t, 3, check.ActualCount)
	require.Equal(t, -1, check.FirstMismatch)

	check = CompareTxLists([]common.Hash{a, b, c}, []common.Hash{a, c, b})
	require.False(t, check.Match, "order matters")
	require.Equal(t, 1, check.FirstMismatch)
	require.Contains(t, check.Detail, "position 1")

	check = CompareTxLists([]common.Hash{a, b, c}, []common.Hash{a, b})
	require.False(t, check.Match, "count matters")
	require.Equal(t, -1, check.FirstMismatch)
	require.Contains(t, check.Detail, "expected 3 transactions, block has 2")

	check = CompareTxLists(nil, nil)
	require.True(t, check.Match, "an empty plan matches an empty block")
}

func TestCalcGasLimitMirrorsGeth(t *testing.T) {
	// Toward a higher target: at most parent/1024 - 1 per block.
	require.Equal(t, uint64(30_000_000+29_295), calcGasLimit(30_000_000, 60_000_000))
	// Exact target within reach.
	require.Equal(t, uint64(30_010_000), calcGasLimit(30_000_000, 30_010_000))
	// Toward a lower target.
	require.Equal(t, uint64(30_000_000-29_295), calcGasLimit(30_000_000, 10_000_000))
	// Unchanged when equal.
	require.Equal(t, uint64(30_000_000), calcGasLimit(30_000_000, 30_000_000))
}
