package action_plan

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactedHidesExplicitTransactions(t *testing.T) {
	plan := &SlotPlan{
		Slot: 10,
		Build: &BuildPlan{Local: &LocalBuildPlan{
			TxSource:     "explicit",
			Transactions: []string{"0x01", "0x02"},
		}},
	}

	redacted := plan.Redacted()
	require.Nil(t, redacted.Build.Local.Transactions)
	require.Equal(t, 2, redacted.Build.Local.TransactionsRedacted)
	require.Len(t, plan.Build.Local.Transactions, 2, "the stored plan is untouched")

	rule := &SlotRule{ID: "r", Build: &BuildPlan{Local: &LocalBuildPlan{Transactions: []string{"0x01"}}}}
	require.Equal(t, 1, rule.Redacted().Build.Local.TransactionsRedacted)
	require.Len(t, rule.Build.Local.Transactions, 1)

	frozen := &FrozenPlan{
		Plan:  plan,
		Build: &ResolvedBuildSettings{Local: &ResolvedLocalBuildSettings{Transactions: []string{"0x01", "0x02"}}},
	}

	redactedFrozen := frozen.Redacted()
	require.Nil(t, redactedFrozen.Plan.Build.Local.Transactions)
	require.Nil(t, redactedFrozen.Build.Local.Transactions)
	require.Equal(t, 2, redactedFrozen.Build.Local.TransactionsRedacted)
	require.Len(t, frozen.Build.Local.Transactions, 2)

	plain := &SlotPlan{Slot: 11}
	require.Same(t, plain, plain.Redacted(), "plans without a list are returned as is")
}

func TestRedactedListCannotBeSavedBack(t *testing.T) {
	local := &LocalBuildPlan{TxSource: "explicit", TransactionsRedacted: 2}
	require.ErrorIs(t, local.validate(), errRedactedTransactions)
}
