package action_plan

import "errors"

// Explicit local build lists are raw signed transactions that never passed
// through a public mempool. Readers without a login get plans, rules and
// frozen snapshots with each list replaced by its length
// (transactions_redacted); writes that send such a redacted list back are
// refused instead of silently dropping the transactions.

// errRedactedTransactions refuses a write carrying a list hidden from its
// reader.
var errRedactedTransactions = errors.New(
	"build.local.transactions were hidden from this reader (no login); reload the plan logged in before saving it")

// Redacted returns the plan without raw explicit transactions (the plan
// itself when it has none).
func (p *SlotPlan) Redacted() *SlotPlan {
	if p == nil || p.Build == nil || p.Build.Local == nil || len(p.Build.Local.Transactions) == 0 {
		return p
	}

	out := *p
	out.Build = p.Build.redacted()

	return &out
}

// Redacted returns the rule without raw explicit transactions.
func (r *SlotRule) Redacted() *SlotRule {
	if r == nil || r.Build == nil || r.Build.Local == nil || len(r.Build.Local.Transactions) == 0 {
		return r
	}

	out := *r
	out.Build = r.Build.redacted()

	return &out
}

// Redacted returns the frozen snapshot without raw explicit transactions, in
// the raw plan and in the resolved settings.
func (f *FrozenPlan) Redacted() *FrozenPlan {
	if f == nil {
		return nil
	}

	resolvedTxs := f.Build != nil && f.Build.Local != nil && len(f.Build.Local.Transactions) > 0
	plan := f.Plan.Redacted()

	if !resolvedTxs && plan == f.Plan {
		return f
	}

	out := *f
	out.Plan = plan

	if resolvedTxs {
		build := *f.Build
		local := *f.Build.Local
		local.TransactionsRedacted = len(local.Transactions)
		local.Transactions = nil
		build.Local = &local
		out.Build = &build
	}

	return &out
}

// RedactedPlans returns the plans without raw explicit transactions.
func RedactedPlans(plans []*SlotPlan) []*SlotPlan {
	out := make([]*SlotPlan, len(plans))
	for i, plan := range plans {
		out[i] = plan.Redacted()
	}

	return out
}

// RedactedRules returns the rules without raw explicit transactions.
func RedactedRules(rules []*SlotRule) []*SlotRule {
	out := make([]*SlotRule, len(rules))
	for i, rule := range rules {
		out[i] = rule.Redacted()
	}

	return out
}

func (b *BuildPlan) redacted() *BuildPlan {
	build := *b
	local := *b.Local
	local.TransactionsRedacted = len(local.Transactions)
	local.Transactions = nil
	build.Local = &local

	return &build
}
