package tx_plan_verifier

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestCompareTxLists(t *testing.T) {
	a, b, c := common.Hash{1}, common.Hash{2}, common.Hash{3}

	check := CompareTxLists([]common.Hash{a, b}, []common.Hash{a, b})
	require.True(t, check.Match)
	require.Equal(t, 2, check.ActualCount)
	require.Equal(t, -1, check.FirstMismatch)

	check = CompareTxLists([]common.Hash{a, b}, []common.Hash{a, c})
	require.False(t, check.Match)
	require.Equal(t, 1, check.FirstMismatch)
	require.Contains(t, check.Detail, "position 1")

	check = CompareTxLists([]common.Hash{a, b}, []common.Hash{a})
	require.False(t, check.Match)
	require.Equal(t, -1, check.FirstMismatch)
	require.Contains(t, check.Detail, "expected 2 transactions, block has 1")

	check = CompareTxLists(nil, nil)
	require.True(t, check.Match, "an empty plan matches an empty block")

	check = CompareTxLists([]common.Hash{}, []common.Hash{a})
	require.False(t, check.Match)
}
