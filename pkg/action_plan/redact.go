package action_plan

import (
	"errors"

	"github.com/ethpandaops/buildoor/pkg/config"
)

// Readers without a login get plans, rules and frozen snapshots with their
// private parts hidden:
//   - explicit local build lists are raw signed transactions that never
//     passed through a public mempool: each list is replaced by its length
//     (transactions_redacted);
//   - reveal target URLs may carry beacon node credentials: user info, path
//     and query are replaced by config.RedactedMarker.
//
// Writes that send such a redacted value back are refused instead of
// silently storing the redaction.

// errRedactedTransactions refuses a write carrying a list hidden from its
// reader.
var errRedactedTransactions = errors.New(
	"build.local.transactions were hidden from this reader (no login); reload the plan logged in before saving it")

// Redacted returns the plan without its private parts (the plan itself when
// it has none).
func (p *SlotPlan) Redacted() *SlotPlan {
	if p == nil {
		return nil
	}

	build, reveal := p.Build.redacted(), p.Reveal.redacted()
	if build == p.Build && reveal == p.Reveal {
		return p
	}

	out := *p
	out.Build = build
	out.Reveal = reveal

	return &out
}

// Redacted returns the rule without its private parts.
func (r *SlotRule) Redacted() *SlotRule {
	if r == nil {
		return nil
	}

	build, reveal := r.Build.redacted(), r.Reveal.redacted()
	if build == r.Build && reveal == r.Reveal {
		return r
	}

	out := *r
	out.Build = build
	out.Reveal = reveal

	return &out
}

// Redacted returns the frozen snapshot without its private parts, in the raw
// plan and in the resolved settings.
func (f *FrozenPlan) Redacted() *FrozenPlan {
	if f == nil {
		return nil
	}

	resolvedTxs := f.Build != nil && f.Build.Local != nil && len(f.Build.Local.Transactions) > 0
	var (
		targets         []RevealTarget
		targetsRedacted bool
	)

	if f.Reveal != nil {
		targets, targetsRedacted = redactedTargets(f.Reveal.Targets)
	}

	plan := f.Plan.Redacted()

	if !resolvedTxs && !targetsRedacted && plan == f.Plan {
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

	if targetsRedacted {
		reveal := *f.Reveal
		reveal.Targets = targets
		out.Reveal = &reveal
	}

	return &out
}

// RedactedPlans returns the plans without their private parts.
func RedactedPlans(plans []*SlotPlan) []*SlotPlan {
	out := make([]*SlotPlan, len(plans))
	for i, plan := range plans {
		out[i] = plan.Redacted()
	}

	return out
}

// RedactedRules returns the rules without their private parts.
func RedactedRules(rules []*SlotRule) []*SlotRule {
	out := make([]*SlotRule, len(rules))
	for i, rule := range rules {
		out[i] = rule.Redacted()
	}

	return out
}

// redacted returns the build plan without raw explicit transactions (the
// plan itself when it has none).
func (b *BuildPlan) redacted() *BuildPlan {
	if b == nil || b.Local == nil || len(b.Local.Transactions) == 0 {
		return b
	}

	build := *b
	local := *b.Local
	local.TransactionsRedacted = len(local.Transactions)
	local.Transactions = nil
	build.Local = &local

	return &build
}

// redacted returns the reveal plan with its target URLs redacted (the plan
// itself when no URL changes).
func (p *RevealPlan) redacted() *RevealPlan {
	if p == nil {
		return nil
	}

	targets, changed := redactedTargets(p.Targets)
	if !changed {
		return p
	}

	out := *p
	out.Targets = targets

	return &out
}

// redactedTargets returns the targets with their URLs redacted, and whether
// any URL changed (the input slice when none did).
func redactedTargets(targets []RevealTarget) ([]RevealTarget, bool) {
	var out []RevealTarget

	for i, target := range targets {
		for j, raw := range target.URLs {
			redacted := config.RedactURL(raw)
			if redacted == raw {
				continue
			}

			if out == nil {
				out = make([]RevealTarget, len(targets))
				for k := range targets {
					out[k] = targets[k].clone()
				}
			}

			out[i].URLs[j] = redacted
		}
	}

	if out == nil {
		return targets, false
	}

	return out, true
}
