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

func TestRedactedHidesRevealTargetCredentials(t *testing.T) {
	secret := "https://user:pass@bn.example.org/key/abc"
	plain := "http://cl-1-lighthouse-geth:4000"
	targets := []RevealTarget{{URLs: []string{plain}}, {URLs: []string{secret}, DelayMs: 500}}

	plan := &SlotPlan{Slot: 10, Reveal: &RevealPlan{Mode: ModeCustom, Targets: targets}}
	redacted := plan.Redacted()
	require.Equal(t, plain, redacted.Reveal.Targets[0].URLs[0])
	require.Equal(t, "https://***@bn.example.org/***", redacted.Reveal.Targets[1].URLs[0])
	require.Equal(t, int64(500), redacted.Reveal.Targets[1].DelayMs)
	require.Equal(t, secret, plan.Reveal.Targets[1].URLs[0], "the stored plan is untouched")

	open := &SlotPlan{Slot: 11, Reveal: &RevealPlan{Mode: ModeCustom, Targets: targets[:1]}}
	require.Same(t, open, open.Redacted(), "URLs without credentials are no reason to copy")

	rule := &SlotRule{ID: "r", Reveal: &RevealPlan{Mode: ModeCustom, Targets: targets}}
	require.Equal(t, "https://***@bn.example.org/***", rule.Redacted().Reveal.Targets[1].URLs[0])

	frozen := &FrozenPlan{Plan: plan, Reveal: &ResolvedRevealSettings{Targets: targets}}
	redactedFrozen := frozen.Redacted()
	require.Equal(t, "https://***@bn.example.org/***", redactedFrozen.Plan.Reveal.Targets[1].URLs[0])
	require.Equal(t, "https://***@bn.example.org/***", redactedFrozen.Reveal.Targets[1].URLs[0])
	require.Equal(t, secret, frozen.Reveal.Targets[1].URLs[0])

	// A redacted URL sent back is refused instead of being stored.
	back := &RevealPlan{Mode: ModeCustom, Targets: redacted.Reveal.Targets}
	require.ErrorContains(t, back.validate(12000), "redacted for this reader")
}
