package action_plan

import (
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

// RulesNamespace is the kv_store namespace holding the persisted recurring
// rules.
const RulesNamespace = "slot_rules"

// MaxRules bounds the persisted rule set. Rules are evaluated per slot, and a
// long list is a sign that explicit plans are the better tool.
const MaxRules = 32

// ruleIDPattern constrains rule ids to stable, url/kv-safe slugs.
var ruleIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// SlotRule is a recurring plan template: every slot whose index within the
// epoch matches gets the rule's categories as its plan. It exists for
// scenarios that must repeat for a whole devnet run — e.g. "win slot 31 of
// every epoch and withhold the payload" — which explicit per-slot plans can
// only express for a bounded horizon.
//
// Precedence is wholesale, never merged: an explicit plan for a slot replaces
// any rule, and a plan with IgnoreRules opts the slot out entirely. Rules are
// resolved at freeze time from the live rule set, exactly like the global
// config baseline, so a rule change never rewrites an already frozen slot.
type SlotRule struct {
	ID          string `json:"id"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description,omitempty"`

	// SlotsInEpoch are the matched slot indices within an epoch (0-based, so
	// 31 is the last slot of a 32-slot epoch).
	SlotsInEpoch []uint64 `json:"slots_in_epoch"`

	// FromEpoch / ToEpoch bound the rule to an inclusive epoch window; nil is
	// unbounded on that side.
	FromEpoch *uint64 `json:"from_epoch,omitempty"`
	ToEpoch   *uint64 `json:"to_epoch,omitempty"`

	Bid        *BidPlan        `json:"bid,omitempty"`
	BuilderAPI *BuilderAPIPlan `json:"builder_api,omitempty"`
	Reveal     *RevealPlan     `json:"reveal,omitempty"`
	Build      *BuildPlan      `json:"build,omitempty"`
	Transforms *TransformPlan  `json:"transforms,omitempty"`

	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// Clone returns a deep copy. Every rule crossing the package boundary is a
// clone, so callers can never mutate stored state.
func (r *SlotRule) Clone() *SlotRule {
	if r == nil {
		return nil
	}

	c := *r
	c.SlotsInEpoch = slices.Clone(r.SlotsInEpoch)
	c.FromEpoch = cloneScalar(r.FromEpoch)
	c.ToEpoch = cloneScalar(r.ToEpoch)
	c.Bid = r.Bid.clone()
	c.BuilderAPI = r.BuilderAPI.clone()
	c.Reveal = r.Reveal.clone()
	c.Build = r.Build.clone()
	c.Transforms = r.Transforms.clone()

	return &c
}

// Matches reports whether the rule applies to the slot. A disabled rule never
// matches.
func (r *SlotRule) Matches(slot phase0.Slot, slotsPerEpoch uint64) bool {
	if r == nil || !r.Enabled || slotsPerEpoch == 0 {
		return false
	}

	epoch := uint64(slot) / slotsPerEpoch
	if r.FromEpoch != nil && epoch < *r.FromEpoch {
		return false
	}

	if r.ToEpoch != nil && epoch > *r.ToEpoch {
		return false
	}

	return slices.Contains(r.SlotsInEpoch, uint64(slot)%slotsPerEpoch)
}

// planFor materializes the rule as the slot's plan. The result is marked with
// the rule id and is never persisted — it is synthesized on every read.
func (r *SlotRule) planFor(slot phase0.Slot) *SlotPlan {
	return &SlotPlan{
		Slot:       slot,
		Bid:        r.Bid.clone(),
		BuilderAPI: r.BuilderAPI.clone(),
		Reveal:     r.Reveal.clone(),
		Build:      r.Build.clone(),
		Transforms: r.Transforms.clone(),
		RuleID:     r.ID,
		UpdatedAt:  r.UpdatedAt,
		UpdatedBy:  r.UpdatedBy,
	}
}

// Validate checks the matcher and, through a materialized plan, every category
// against the chain spec.
func (r *SlotRule) Validate(slotsPerEpoch uint64, secondsPerSlot time.Duration) error {
	if !ruleIDPattern.MatchString(r.ID) {
		return fmt.Errorf("rule id %q must match %s", r.ID, ruleIDPattern.String())
	}

	if len(r.SlotsInEpoch) == 0 {
		return fmt.Errorf("rule %q: slots_in_epoch must not be empty", r.ID)
	}

	seen := make(map[uint64]struct{}, len(r.SlotsInEpoch))

	for _, index := range r.SlotsInEpoch {
		if slotsPerEpoch > 0 && index >= slotsPerEpoch {
			return fmt.Errorf("rule %q: slot index %d out of range for %d slots per epoch",
				r.ID, index, slotsPerEpoch)
		}

		if _, dup := seen[index]; dup {
			return fmt.Errorf("rule %q: duplicate slot index %d", r.ID, index)
		}

		seen[index] = struct{}{}
	}

	if r.FromEpoch != nil && r.ToEpoch != nil && *r.ToEpoch < *r.FromEpoch {
		return fmt.Errorf("rule %q: invalid epoch range %d..%d", r.ID, *r.FromEpoch, *r.ToEpoch)
	}

	plan := r.planFor(0)
	if plan.IsEmpty() {
		return fmt.Errorf("rule %q carries no instruction", r.ID)
	}

	if err := plan.Validate(secondsPerSlot); err != nil {
		return fmt.Errorf("rule %q: %w", r.ID, err)
	}

	return nil
}
