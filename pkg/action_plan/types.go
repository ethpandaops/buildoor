// Package action_plan implements the per-slot action plan: sparse, persisted
// per-slot operation modes for bidding, builder-api serving and reveal, with
// freeze semantics so a slot's plan becomes immutable once execution for it
// can begin.
package action_plan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/jqtransform"
)

// Mode is a per-category plan mode. Only explicit instructions are persisted:
// an absent category inherits the global baseline; "custom" force-activates
// the category for the slot (even when the module is globally disabled) with
// optional setting overrides; "disabled" suppresses it.
type Mode string

const (
	// ModeDisabled suppresses the category for the slot.
	ModeDisabled Mode = "disabled"
	// ModeCustom force-activates the category for the slot with optional
	// setting overrides (omitted overrides inherit the global config).
	ModeCustom Mode = "custom"
)

func validateMode(category string, mode Mode) error {
	switch mode {
	case ModeDisabled, ModeCustom:
		return nil
	default:
		return fmt.Errorf("%s: invalid mode %q (must be %q or %q)", category, mode, ModeDisabled, ModeCustom)
	}
}

// BidPlan is the per-slot p2p bidding instruction. All override fields are
// optional; nil inherits the effective global configuration at freeze time.
// Timing fields are signed milliseconds relative to slot start (negative =
// before slot start), matching the global EPBS config semantics.
type BidPlan struct {
	Mode Mode `json:"mode"`

	BidStartTime *int64  `json:"bid_start_time,omitempty"`
	BidEndTime   *int64  `json:"bid_end_time,omitempty"`
	BidMinAmount *uint64 `json:"bid_min_amount,omitempty"` // gwei
	BidIncrease  *uint64 `json:"bid_increase,omitempty"`   // gwei
	BidInterval  *int64  `json:"bid_interval,omitempty"`   // ms, >= 0, 0 = single bid
	BidSubsidy   *uint64 `json:"bid_subsidy,omitempty"`    // gwei

	// BidValueGwei is an absolute bid base value replacing
	// max(blockValue, min) + subsidy; BidIncrease still applies per re-bid.
	// Allows underbidding the block value for testing.
	BidValueGwei *uint64 `json:"bid_value_gwei,omitempty"`

	// IgnoreMissingPrefs bids with the payload's fee recipient when no gossip
	// proposer preferences arrived for the slot, bypassing the skip gate.
	IgnoreMissingPrefs bool `json:"ignore_missing_prefs,omitempty"`
}

func (p *BidPlan) clone() *BidPlan {
	if p == nil {
		return nil
	}

	c := *p
	c.BidStartTime = cloneScalar(p.BidStartTime)
	c.BidEndTime = cloneScalar(p.BidEndTime)
	c.BidMinAmount = cloneScalar(p.BidMinAmount)
	c.BidIncrease = cloneScalar(p.BidIncrease)
	c.BidInterval = cloneScalar(p.BidInterval)
	c.BidSubsidy = cloneScalar(p.BidSubsidy)
	c.BidValueGwei = cloneScalar(p.BidValueGwei)

	return &c
}

func (p *BidPlan) hasOverrides() bool {
	return p.BidStartTime != nil || p.BidEndTime != nil || p.BidMinAmount != nil ||
		p.BidIncrease != nil || p.BidInterval != nil || p.BidSubsidy != nil ||
		p.BidValueGwei != nil || p.IgnoreMissingPrefs
}

func (p *BidPlan) validate(slotMs int64) error {
	if err := validateMode("bid", p.Mode); err != nil {
		return err
	}

	if p.Mode == ModeDisabled && p.hasOverrides() {
		return errors.New("bid: overrides are only allowed in custom mode")
	}

	if err := validateTimeBound("bid.bid_start_time", p.BidStartTime, -slotMs, slotMs); err != nil {
		return err
	}

	if err := validateTimeBound("bid.bid_end_time", p.BidEndTime, -slotMs, slotMs); err != nil {
		return err
	}

	if p.BidStartTime != nil && p.BidEndTime != nil && *p.BidStartTime >= *p.BidEndTime {
		return fmt.Errorf("bid: bid_start_time (%d) must be before bid_end_time (%d)",
			*p.BidStartTime, *p.BidEndTime)
	}

	if p.BidInterval != nil && *p.BidInterval < 0 {
		return fmt.Errorf("bid: bid_interval must be >= 0, got %d", *p.BidInterval)
	}

	return nil
}

// BuilderAPIPlan is the per-slot Builder API serving instruction for both the
// legacy getHeader and the Gloas getExecutionPayloadBid endpoints.
type BuilderAPIPlan struct {
	Mode Mode `json:"mode"`

	// ValueSubsidyGwei replaces the global BlockValueSubsidyGwei for this slot.
	ValueSubsidyGwei *uint64 `json:"value_subsidy_gwei,omitempty"`

	// TotalValueOverrideGwei is the absolute total proposer-visible bid value
	// (before the Gloas execution-payment split), replacing block value +
	// subsidy. May exceed the block value to test payment edge cases.
	TotalValueOverrideGwei *uint64 `json:"total_value_override_gwei,omitempty"`

	// ResponseDelayMs delays the bid response by this many milliseconds
	// (context-cancellable, capped at one slot).
	ResponseDelayMs *int64 `json:"response_delay_ms,omitempty"`
}

func (p *BuilderAPIPlan) clone() *BuilderAPIPlan {
	if p == nil {
		return nil
	}

	c := *p
	c.ValueSubsidyGwei = cloneScalar(p.ValueSubsidyGwei)
	c.TotalValueOverrideGwei = cloneScalar(p.TotalValueOverrideGwei)
	c.ResponseDelayMs = cloneScalar(p.ResponseDelayMs)

	return &c
}

func (p *BuilderAPIPlan) hasOverrides() bool {
	return p.ValueSubsidyGwei != nil || p.TotalValueOverrideGwei != nil || p.ResponseDelayMs != nil
}

func (p *BuilderAPIPlan) validate(slotMs int64) error {
	if err := validateMode("builder_api", p.Mode); err != nil {
		return err
	}

	if p.Mode == ModeDisabled && p.hasOverrides() {
		return errors.New("builder_api: overrides are only allowed in custom mode")
	}

	if p.ResponseDelayMs != nil && (*p.ResponseDelayMs < 0 || *p.ResponseDelayMs > slotMs) {
		return fmt.Errorf("builder_api: response_delay_ms must be within [0, %d], got %d",
			slotMs, *p.ResponseDelayMs)
	}

	return nil
}

// RevealPlan is the per-slot payload reveal instruction. "disabled" withholds
// the envelope, "custom" force-activates the reveal (even when globally
// disabled) and overrides the gate settings (possibly past the in-slot
// deadline for adverse testing, clamped to at most one additional slot after
// slot end).
type RevealPlan struct {
	Mode Mode `json:"mode"`

	// RevealTimeMs is signed milliseconds relative to slot start (time gate).
	RevealTimeMs *int64 `json:"reveal_time_ms,omitempty"`

	// GateMode overrides the reveal gate: time | vote | vote_or_time |
	// vote_and_time.
	GateMode *string `json:"gate_mode,omitempty"`

	// VoteThresholdPct overrides the vote gate's participation threshold.
	VoteThresholdPct *uint64 `json:"vote_threshold_pct,omitempty"`

	// BroadcastValidation overrides the envelope submission's broadcast
	// validation level: gossip | consensus | consensus_and_equivocation.
	BroadcastValidation *string `json:"broadcast_validation,omitempty"`
}

func (p *RevealPlan) clone() *RevealPlan {
	if p == nil {
		return nil
	}

	c := *p
	c.RevealTimeMs = cloneScalar(p.RevealTimeMs)
	c.GateMode = cloneScalar(p.GateMode)
	c.VoteThresholdPct = cloneScalar(p.VoteThresholdPct)
	c.BroadcastValidation = cloneScalar(p.BroadcastValidation)

	return &c
}

func (p *RevealPlan) validate(slotMs int64) error {
	if err := validateMode("reveal", p.Mode); err != nil {
		return err
	}

	if p.Mode == ModeDisabled && (p.RevealTimeMs != nil || p.GateMode != nil ||
		p.VoteThresholdPct != nil || p.BroadcastValidation != nil) {
		return errors.New("reveal: overrides are only allowed in custom mode")
	}

	if p.GateMode != nil {
		probe := config.RevealConfig{GateMode: *p.GateMode}
		if probe.NormalizedGateMode() != *p.GateMode {
			return fmt.Errorf("reveal.gate_mode: invalid value %q", *p.GateMode)
		}
	}

	if p.VoteThresholdPct != nil && *p.VoteThresholdPct > 100 {
		return fmt.Errorf("reveal.vote_threshold_pct: must be within [0, 100], got %d", *p.VoteThresholdPct)
	}

	if p.BroadcastValidation != nil {
		probe := config.RevealConfig{BroadcastValidation: *p.BroadcastValidation}
		if probe.NormalizedBroadcastValidation() != *p.BroadcastValidation {
			return fmt.Errorf("reveal.broadcast_validation: invalid value %q", *p.BroadcastValidation)
		}
	}

	// Custom reveal times may run into the next slot for late-reveal testing,
	// but no further, so a typo cannot park a pending timer indefinitely.
	return validateTimeBound("reveal.reveal_time_ms", p.RevealTimeMs, -slotMs, 2*slotMs)
}

// BuildPlan is the per-slot payload-build instruction. Unlike the consumer
// categories it carries no custom/disabled mode: it only tweaks HOW the slot's
// payload is built when a build happens (the build decision itself stays with
// the schedule + consumer plans).
type BuildPlan struct {
	// ReorgParentPayload builds on the grandparent (n-2) execution payload
	// instead of the immediate parent: the FCU head block hash and the payload
	// attributes' withdrawals are taken from the PARENT slot's payload
	// attributes (whose parent is n-2), while every other property comes from
	// the current slot. This is a deliberate parent-payload reorg attempt —
	// rejected by mainnet forkchoice, but useful for exercising the reveal /
	// inclusion path against a withheld parent.
	ReorgParentPayload bool `json:"reorg_parent_payload,omitempty"`
}

func (p *BuildPlan) clone() *BuildPlan {
	if p == nil {
		return nil
	}

	c := *p

	return &c
}

// isZero reports whether the build plan carries no active instruction; such a
// plan is dropped rather than persisted.
func (p *BuildPlan) isZero() bool {
	return p == nil || !p.ReorgParentPayload
}

func (p *BuildPlan) validate() error {
	// No mode and no bounded fields yet; the boolean flag is always valid.
	return nil
}

// TransformPlan carries operator-supplied jq expressions applied to the JSON
// form of builder objects for arbitrary custom modifications. Like build it is
// modeless. Each expression is empty (no-op) or a valid jq program (validated
// on update). Semantics (see payload_builder / payload_bidder):
//   - Payload rewrites the built execution payload before it feeds both the
//     bid commitment and the envelope reveal.
//   - Bid rewrites the bid MESSAGE just before signing; the modified message is
//     then re-signed, so the result is validly signed but customized.
//   - Envelope rewrites the envelope MESSAGE just before signing, then re-signs.
type TransformPlan struct {
	Payload  string `json:"payload,omitempty"`
	Bid      string `json:"bid,omitempty"`
	Envelope string `json:"envelope,omitempty"`
}

func (p *TransformPlan) clone() *TransformPlan {
	if p == nil {
		return nil
	}

	c := *p

	return &c
}

// isZero reports whether the transform plan carries no expression.
func (p *TransformPlan) isZero() bool {
	return p == nil || (p.Payload == "" && p.Bid == "" && p.Envelope == "")
}

func (p *TransformPlan) validate() error {
	for name, expr := range map[string]string{
		"transforms.payload":  p.Payload,
		"transforms.bid":      p.Bid,
		"transforms.envelope": p.Envelope,
	} {
		if err := jqtransform.Validate(expr); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}

	return nil
}

// SlotPlan is the persisted per-slot instruction set. Each category is
// optional; an absent category inherits the global baseline (including the
// module enable flags).
type SlotPlan struct {
	Slot       phase0.Slot     `json:"slot"`
	Bid        *BidPlan        `json:"bid,omitempty"`
	BuilderAPI *BuilderAPIPlan `json:"builder_api,omitempty"`
	Reveal     *RevealPlan     `json:"reveal,omitempty"`
	Build      *BuildPlan      `json:"build,omitempty"`
	Transforms *TransformPlan  `json:"transforms,omitempty"`
	UpdatedAt  time.Time       `json:"updated_at"`
	UpdatedBy  string          `json:"updated_by"`
}

// Clone returns a deep copy. All plan values crossing the package boundary
// are clones, so callers can never mutate stored state.
func (p *SlotPlan) Clone() *SlotPlan {
	if p == nil {
		return nil
	}

	c := *p
	c.Bid = p.Bid.clone()
	c.BuilderAPI = p.BuilderAPI.clone()
	c.Reveal = p.Reveal.clone()
	c.Build = p.Build.clone()
	c.Transforms = p.Transforms.clone()

	return &c
}

// IsEmpty reports whether the plan carries no instruction at all.
func (p *SlotPlan) IsEmpty() bool {
	return p == nil || (p.Bid == nil && p.BuilderAPI == nil && p.Reveal == nil &&
		p.Build.isZero() && p.Transforms.isZero())
}

// BidOverride returns the per-slot enable override for p2p bidding:
// nil = inherit the global flag, true = force-active, false = suppressed.
func (p *SlotPlan) BidOverride() *bool {
	if p == nil || p.Bid == nil {
		return nil
	}

	active := p.Bid.Mode == ModeCustom

	return &active
}

// BuilderAPIOverride returns the per-slot enable override for Builder API
// bid serving: nil = inherit, true = force-active, false = suppressed.
func (p *SlotPlan) BuilderAPIOverride() *bool {
	if p == nil || p.BuilderAPI == nil {
		return nil
	}

	active := p.BuilderAPI.Mode == ModeCustom

	return &active
}

// RevealOverride returns the per-slot enable override for the reveal:
// nil = inherit (reveals always run), true = custom, false = suppressed.
func (p *SlotPlan) RevealOverride() *bool {
	if p == nil || p.Reveal == nil {
		return nil
	}

	active := p.Reveal.Mode == ModeCustom

	return &active
}

// Validate checks all category plans against the slot duration.
func (p *SlotPlan) Validate(secondsPerSlot time.Duration) error {
	slotMs := secondsPerSlot.Milliseconds()
	if slotMs <= 0 {
		return fmt.Errorf("invalid slot duration %s", secondsPerSlot)
	}

	if p.Bid != nil {
		if err := p.Bid.validate(slotMs); err != nil {
			return err
		}
	}

	if p.BuilderAPI != nil {
		if err := p.BuilderAPI.validate(slotMs); err != nil {
			return err
		}
	}

	if p.Reveal != nil {
		if err := p.Reveal.validate(slotMs); err != nil {
			return err
		}
	}

	if p.Build != nil {
		if err := p.Build.validate(); err != nil {
			return err
		}
	}

	if p.Transforms != nil {
		if err := p.Transforms.validate(); err != nil {
			return err
		}
	}

	return nil
}

// PlanUpdate is one mutation unit of the bulk-update API. Targets are the
// union of Slots and the inclusive FromSlot..ToSlot range. Category members
// are three-state: absent = unchanged, JSON null = clear (back to inherit),
// object = replace. Delete removes the whole plan for the targeted slots.
//
// Set applies fine-grained path updates AFTER the category members, so
// consumers never need to send a full category object for partial edits:
//
//	"bid"                    → whole category (null = clear, object = replace)
//	"bid.mode"               → single field ("custom" | "disabled")
//	"bid.bid_min_amount"     → single override (null = back to inherit)
//
// Setting a field on an absent category creates it with mode "custom".
// Paths are applied in lexicographic order (deterministic).
type PlanUpdate struct {
	Slots    []uint64 `json:"slots,omitempty"`
	FromSlot *uint64  `json:"from_slot,omitempty"`
	ToSlot   *uint64  `json:"to_slot,omitempty"`
	Delete   bool     `json:"delete,omitempty"`

	Bid        json.RawMessage `json:"bid,omitempty"`
	BuilderAPI json.RawMessage `json:"builder_api,omitempty"`
	Reveal     json.RawMessage `json:"reveal,omitempty"`
	Build      json.RawMessage `json:"build,omitempty"`
	Transforms json.RawMessage `json:"transforms,omitempty"`

	Set map[string]json.RawMessage `json:"set,omitempty"`
}

// TargetSlots expands the update's slot list and range into the full target
// set. The expansion is overflow-safe and bounded by MaxSlotsPerUpdate.
func (u *PlanUpdate) TargetSlots() ([]phase0.Slot, error) {
	count := uint64(len(u.Slots))

	if (u.FromSlot == nil) != (u.ToSlot == nil) {
		return nil, errors.New("from_slot and to_slot must be provided together")
	}

	if u.FromSlot != nil {
		if *u.ToSlot < *u.FromSlot {
			return nil, fmt.Errorf("invalid slot range %d..%d", *u.FromSlot, *u.ToSlot)
		}

		rangeCount := *u.ToSlot - *u.FromSlot + 1
		if rangeCount > MaxSlotsPerUpdate {
			return nil, fmt.Errorf("slot range %d..%d targets %d slots, max %d",
				*u.FromSlot, *u.ToSlot, rangeCount, MaxSlotsPerUpdate)
		}

		count += rangeCount
	}

	if count == 0 {
		return nil, errors.New("update targets no slots")
	}

	if count > MaxSlotsPerUpdate {
		return nil, fmt.Errorf("update targets %d slots, max %d", count, MaxSlotsPerUpdate)
	}

	targets := make([]phase0.Slot, 0, count)
	for _, slot := range u.Slots {
		targets = append(targets, phase0.Slot(slot))
	}

	if u.FromSlot != nil {
		for slot := *u.FromSlot; ; slot++ {
			targets = append(targets, phase0.Slot(slot))

			if slot == *u.ToSlot {
				break
			}
		}
	}

	return targets, nil
}

// ApplyUpdateToPlan merges a single update into an existing plan (which may be
// nil) and returns the resulting plan, or nil when the result carries no
// instruction anymore. The existing plan is never mutated. The returned plan
// is not yet validated against the slot duration; callers run Validate.
func ApplyUpdateToPlan(existing *SlotPlan, u *PlanUpdate) (*SlotPlan, error) {
	if u.Delete {
		return nil, nil
	}

	result := existing.Clone()
	if result == nil {
		result = &SlotPlan{}
	}

	bid, changed, err := applyCategoryPatch("bid", u.Bid, result.Bid)
	if err != nil {
		return nil, err
	} else if changed {
		result.Bid = bid
	}

	builderAPI, changed, err := applyCategoryPatch("builder_api", u.BuilderAPI, result.BuilderAPI)
	if err != nil {
		return nil, err
	} else if changed {
		result.BuilderAPI = builderAPI
	}

	reveal, changed, err := applyCategoryPatch("reveal", u.Reveal, result.Reveal)
	if err != nil {
		return nil, err
	} else if changed {
		result.Reveal = reveal
	}

	build, changed, err := applyCategoryPatch("build", u.Build, result.Build)
	if err != nil {
		return nil, err
	} else if changed {
		result.Build = build
	}

	transforms, changed, err := applyCategoryPatch("transforms", u.Transforms, result.Transforms)
	if err != nil {
		return nil, err
	} else if changed {
		result.Transforms = transforms
	}

	if err := applySetPaths(result, u.Set); err != nil {
		return nil, err
	}

	// A build plan with no active flag carries no instruction — drop it so it
	// does not keep an otherwise-empty plan alive.
	if result.Build.isZero() {
		result.Build = nil
	}

	// Likewise drop a transforms plan once all its expressions are empty.
	if result.Transforms.isZero() {
		result.Transforms = nil
	}

	if result.IsEmpty() {
		return nil, nil
	}

	return result, nil
}

// applySetPaths applies the fine-grained path updates of a PlanUpdate onto
// the (already category-patched) plan, in lexicographic path order.
func applySetPaths(plan *SlotPlan, set map[string]json.RawMessage) error {
	if len(set) == 0 {
		return nil
	}

	paths := make([]string, 0, len(set))
	for path := range set {
		paths = append(paths, path)
	}

	sort.Strings(paths)

	for _, path := range paths {
		category, field, hasField := strings.Cut(path, ".")

		switch category {
		case "bid":
			patched, err := applyFieldPatch("bid", field, hasField, set[path], plan.Bid)
			if err != nil {
				return err
			}

			plan.Bid = patched
		case "builder_api":
			patched, err := applyFieldPatch("builder_api", field, hasField, set[path], plan.BuilderAPI)
			if err != nil {
				return err
			}

			plan.BuilderAPI = patched
		case "reveal":
			patched, err := applyFieldPatch("reveal", field, hasField, set[path], plan.Reveal)
			if err != nil {
				return err
			}

			plan.Reveal = patched
		case "build":
			patched, err := applyFieldPatch("build", field, hasField, set[path], plan.Build)
			if err != nil {
				return err
			}

			plan.Build = patched
		case "transforms":
			patched, err := applyFieldPatch("transforms", field, hasField, set[path], plan.Transforms)
			if err != nil {
				return err
			}

			plan.Transforms = patched
		default:
			return fmt.Errorf(
				"set: unknown path %q (categories: bid, builder_api, reveal, build, transforms)", path)
		}
	}

	return nil
}

// categoryHasMode reports whether a category uses the custom/disabled mode
// model. The build and transforms categories are pure override sets with no
// mode field.
func categoryHasMode(category string) bool {
	return category != "build" && category != "transforms"
}

// applyFieldPatch applies one set-path onto a category: whole-category
// (null = clear, object = replace) or a single field via a JSON object
// round-trip so unknown fields are rejected by the strict decoder. A field
// set on an absent category creates it with mode "custom"; a null field
// value removes the override (back to inherit).
func applyFieldPatch[T any](category, field string, hasField bool,
	value json.RawMessage, current *T) (*T, error) {
	if !hasField {
		patched, _, err := applyCategoryPatch(category, value, current)

		return patched, err
	}

	if field == "" {
		return nil, fmt.Errorf("set: empty field on category %q", category)
	}

	fields := make(map[string]json.RawMessage, 8)

	if current != nil {
		encoded, err := json.Marshal(current)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", category, err)
		}

		if err := json.Unmarshal(encoded, &fields); err != nil {
			return nil, fmt.Errorf("%s: %w", category, err)
		}
	} else if field != "mode" && categoryHasMode(category) {
		// Creating a mode-based category through a field edit defaults it to
		// custom (fields only carry meaning in custom mode). The build
		// category has no mode, so nothing is injected.
		fields["mode"] = json.RawMessage(`"custom"`)
	}

	if isJSONNull(value) {
		if field == "mode" {
			return nil, fmt.Errorf("set: %s.mode cannot be null (clear the whole category instead)", category)
		}

		delete(fields, field)
	} else {
		fields[field] = value
	}

	merged, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", category, err)
	}

	patched, _, err := applyCategoryPatch(category, merged, current)

	return patched, err
}

// applyCategoryPatch implements the three-state member semantics for one
// category: absent raw = keep current, JSON null = clear, object = replace
// (strictly decoded, unknown fields rejected).
func applyCategoryPatch[T any](category string, raw json.RawMessage, current *T) (*T, bool, error) {
	if len(raw) == 0 {
		return current, false, nil
	}

	if isJSONNull(raw) {
		return nil, true, nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	patch := new(T)
	if err := dec.Decode(patch); err != nil {
		return nil, false, fmt.Errorf("%s: %w", category, err)
	}

	return patch, true, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func validateTimeBound(name string, value *int64, minMs, maxMs int64) error {
	if value == nil {
		return nil
	}

	if *value < minMs || *value > maxMs {
		return fmt.Errorf("%s must be within [%d, %d] ms, got %d", name, minMs, maxMs, *value)
	}

	return nil
}

func cloneScalar[T any](v *T) *T {
	if v == nil {
		return nil
	}

	c := *v

	return &c
}
