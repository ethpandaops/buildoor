package action_plan

import (
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"

	"github.com/ethpandaops/buildoor/pkg/config"
)

// Build skip reasons carried by ResolvedBuildSettings.SkipReason.
const (
	// BuildSkipReasonSchedule marks slots skipped by the schedule (start
	// slot, every_nth cadence or an exhausted next_n budget).
	BuildSkipReasonSchedule = "schedule"
	// BuildSkipReasonPlanDisabled marks slots where the per-slot plan
	// suppressed a consumer that would otherwise be active, leaving no
	// effectively active consumer.
	BuildSkipReasonPlanDisabled = "plan_disabled"
	// BuildSkipReasonNoConsumer marks slots where no consumer is effectively
	// active (all globally disabled or not applicable) without a plan being
	// responsible for it.
	BuildSkipReasonNoConsumer = "no_consumer"
)

// FrozenPlan is the immutable per-slot execution snapshot taken when execution
// for the slot can begin: the raw sparse plan plus all effective settings
// resolved from the live global config at freeze time. Consumers act on the
// resolved values for the rest of the slot, so later global config changes
// never rewrite a partially executed slot. Runtime availability gates
// (registration, signer, server) are still checked per action.
type FrozenPlan struct {
	Slot     phase0.Slot `json:"slot"`
	Plan     *SlotPlan   `json:"plan,omitempty"` // raw sparse plan; nil = none existed
	Fork     string      `json:"fork"`           // fork name at the target slot
	FrozenAt time.Time   `json:"frozen_at"`

	// Build is the complete resolved build decision for the slot (schedule +
	// plan force/suppress). Always non-nil.
	Build *ResolvedBuildSettings `json:"build"`

	// Resolved effective settings; a nil category = suppressed for this slot
	// (by plan or by the global enable flags / protocol applicability).
	Bid        *ResolvedBidSettings        `json:"bid,omitempty"`
	BuilderAPI *ResolvedBuilderAPISettings `json:"builder_api,omitempty"`
	Reveal     *ResolvedRevealSettings     `json:"reveal,omitempty"`
}

// ResolvedBuildSettings is the effective build decision and timing for a slot.
// The plan service is the single scheduling authority: it resolves the global
// schedule (all/every_nth/next_n, start slot), the per-slot plan's force/
// suppress instructions and the effective build timing in one place.
type ResolvedBuildSettings struct {
	// Build is the final decision: build a payload for this slot or not.
	Build bool `json:"build"`

	// Forced marks builds the plan pushed past the schedule (they never
	// consume the next_n budget).
	Forced bool `json:"forced,omitempty"`

	// SkipReason is one of the BuildSkipReason* constants when Build is
	// false, empty otherwise.
	SkipReason string `json:"skip_reason,omitempty"`

	// PlanInvolved marks decisions where a per-slot plan existed or any
	// consumer was effectively active — i.e. skips worth surfacing.
	PlanInvolved bool `json:"plan_involved,omitempty"`

	// BuildStartTimeMs is the effective build start time, milliseconds
	// relative to slot start (signed).
	BuildStartTimeMs int64 `json:"build_start_time_ms"`
}

// ResolvedBidSettings are the effective p2p bidding parameters for the slot.
type ResolvedBidSettings struct {
	StartMs      int64  `json:"start_ms"`
	EndMs        int64  `json:"end_ms"`
	IntervalMs   int64  `json:"interval_ms"`
	MinGwei      uint64 `json:"min_gwei"`
	IncreaseGwei uint64 `json:"increase_gwei"`
	SubsidyGwei  uint64 `json:"subsidy_gwei"`

	// ValueGwei, when set, is the absolute bid base value replacing the
	// max(blockValue, min) + subsidy formula (IncreaseGwei still applies).
	ValueGwei *uint64 `json:"value_gwei,omitempty"`

	// IgnoreMissingPrefs bids without gossip proposer preferences.
	IgnoreMissingPrefs bool `json:"ignore_missing_prefs,omitempty"`

	// Forced marks that the plan activated bidding although the module is
	// globally disabled.
	Forced bool `json:"forced,omitempty"`
}

// ResolvedBuilderAPISettings are the effective Builder API bid-serving
// parameters for the slot.
type ResolvedBuilderAPISettings struct {
	SubsidyGwei uint64 `json:"subsidy_gwei"`

	// TotalValueGwei, when set, is the absolute total proposer-visible bid
	// value (before the Gloas execution-payment split).
	TotalValueGwei *uint64 `json:"total_value_gwei,omitempty"`

	DelayMs int64 `json:"delay_ms,omitempty"`

	// Forced marks that the plan activated serving although the module is
	// globally disabled.
	Forced bool `json:"forced,omitempty"`
}

// ResolvedRevealSettings are the effective reveal parameters for the slot.
// Reveals have no global enable flag, so the category is always present with
// a Suppressed marker instead of being nil.
type ResolvedRevealSettings struct {
	Suppressed   bool  `json:"suppressed,omitempty"`
	RevealTimeMs int64 `json:"reveal_time_ms"`

	// BypassDeadline disables the "past the in-slot deadline → skip" check so
	// deliberately late reveals are attempted.
	BypassDeadline bool `json:"bypass_deadline,omitempty"`
}

// resolveFrozenPlan merges the live global config with a slot's plan (which
// may be nil) into the frozen execution snapshot. slotsBuilt is the schedule
// counter for next_n mode at freeze time.
func resolveFrozenPlan(slot phase0.Slot, plan *SlotPlan, cfg *config.Config,
	fork version.DataVersion, frozenAt time.Time, slotsBuilt uint64) *FrozenPlan {
	frozen := &FrozenPlan{
		Slot:     slot,
		Plan:     plan,
		Fork:     fork.String(),
		FrozenAt: frozenAt,
	}

	frozen.Bid = resolveBid(plan, cfg, fork)
	frozen.BuilderAPI = resolveBuilderAPI(plan, cfg)
	frozen.Reveal = resolveReveal(plan, cfg)
	frozen.Build = resolveBuild(frozen, cfg, slotsBuilt)

	return frozen
}

// resolveBuild derives the complete build decision for the slot from the
// already-resolved consumer settings, the plan's explicit instructions and
// the global schedule.
func resolveBuild(frozen *FrozenPlan, cfg *config.Config, slotsBuilt uint64) *ResolvedBuildSettings {
	build := &ResolvedBuildSettings{
		BuildStartTimeMs: cfg.EPBS.BuildStartTime,
		PlanInvolved: frozen.Plan != nil || frozen.Bid != nil ||
			frozen.BuilderAPI != nil,
	}

	// A plan that explicitly activates (mode custom) an available consumer
	// forces the build past the schedule. A merely-inherited active consumer
	// never forces, and a custom category whose consumer is unavailable
	// (e.g. p2p bidding pre-Gloas) cannot force either.
	if planForcesConsumer(frozen) {
		build.Build = true
		build.Forced = true

		return build
	}

	// Without any effectively active consumer the payload would have no
	// taker, so building is pointless.
	if frozen.Bid == nil && frozen.BuilderAPI == nil {
		if planSuppressesActiveConsumer(frozen, cfg) {
			build.SkipReason = BuildSkipReasonPlanDisabled
		} else {
			build.SkipReason = BuildSkipReasonNoConsumer
		}

		return build
	}

	// Global schedule.
	startSlot := phase0.Slot(cfg.Schedule.StartSlot)
	if startSlot > 0 && frozen.Slot < startSlot {
		build.SkipReason = BuildSkipReasonSchedule

		return build
	}

	switch cfg.Schedule.Mode {
	case config.ScheduleModeEveryN:
		if cfg.Schedule.EveryNth == 0 {
			build.Build = true

			return build
		}

		slotsSinceStart := uint64(frozen.Slot)
		if startSlot > 0 {
			slotsSinceStart = uint64(frozen.Slot - startSlot)
		}

		if slotsSinceStart%cfg.Schedule.EveryNth == 0 {
			build.Build = true
		} else {
			build.SkipReason = BuildSkipReasonSchedule
		}

	case config.ScheduleModeNextN:
		if cfg.Schedule.NextN > 0 && slotsBuilt < cfg.Schedule.NextN {
			build.Build = true
		} else {
			build.SkipReason = BuildSkipReasonSchedule
		}

	case config.ScheduleModeAll:
		build.Build = true

	default:
		build.Build = true
	}

	return build
}

// planForcesConsumer reports whether the plan explicitly activates (mode
// custom) a consumer that is actually available for the slot.
func planForcesConsumer(frozen *FrozenPlan) bool {
	if frozen.Plan == nil {
		return false
	}

	if override := frozen.Plan.BidOverride(); override != nil && *override && frozen.Bid != nil {
		return true
	}

	if override := frozen.Plan.BuilderAPIOverride(); override != nil && *override &&
		frozen.BuilderAPI != nil {
		return true
	}

	return false
}

// planSuppressesActiveConsumer reports whether the plan explicitly disabled a
// consumer whose module is globally enabled — i.e. the plan is the reason no
// consumer is active for the slot. The pre-Gloas fork gate on p2p bidding is
// deliberately not re-checked: the reason is informational, and an operator
// who disabled bidding for a slot gets "plan_disabled" even when the fork
// alone would already have suppressed it.
func planSuppressesActiveConsumer(frozen *FrozenPlan, cfg *config.Config) bool {
	if frozen.Plan == nil {
		return false
	}

	if override := frozen.Plan.BidOverride(); override != nil && !*override && cfg.EPBSEnabled {
		return true
	}

	if override := frozen.Plan.BuilderAPIOverride(); override != nil && !*override &&
		cfg.BuilderAPIEnabled && cfg.APIPort > 0 {
		return true
	}

	return false
}

func resolveBid(plan *SlotPlan, cfg *config.Config, fork version.DataVersion) *ResolvedBidSettings {
	// p2p bidding is a Gloas+ protocol; a plan cannot activate it earlier.
	if fork < version.DataVersionGloas {
		return nil
	}

	active := cfg.EPBSEnabled
	forced := false

	if override := plan.BidOverride(); override != nil {
		forced = *override && !cfg.EPBSEnabled
		active = *override
	}

	if !active {
		return nil
	}

	resolved := &ResolvedBidSettings{
		StartMs:      cfg.EPBS.BidStartTime,
		EndMs:        cfg.EPBS.BidEndTime,
		IntervalMs:   cfg.EPBS.BidInterval,
		MinGwei:      cfg.EPBS.BidMinAmount,
		IncreaseGwei: cfg.EPBS.BidIncrease,
		SubsidyGwei:  cfg.EPBS.BidSubsidy,
		Forced:       forced,
	}

	if cfg.EPBS.BidValueOverride > 0 {
		value := cfg.EPBS.BidValueOverride
		resolved.ValueGwei = &value
	}

	if plan != nil && plan.Bid != nil && plan.Bid.Mode == ModeCustom {
		bid := plan.Bid
		applyOverride(&resolved.StartMs, bid.BidStartTime)
		applyOverride(&resolved.EndMs, bid.BidEndTime)
		applyOverride(&resolved.IntervalMs, bid.BidInterval)
		applyOverride(&resolved.MinGwei, bid.BidMinAmount)
		applyOverride(&resolved.IncreaseGwei, bid.BidIncrease)
		applyOverride(&resolved.SubsidyGwei, bid.BidSubsidy)

		if bid.BidValueGwei != nil {
			resolved.ValueGwei = cloneScalar(bid.BidValueGwei)
		}

		resolved.IgnoreMissingPrefs = bid.IgnoreMissingPrefs
	}

	return resolved
}

func resolveBuilderAPI(plan *SlotPlan, cfg *config.Config) *ResolvedBuilderAPISettings {
	// The Builder API is served on the API port; without a server no plan can
	// activate it (hard availability, not an enable policy).
	if cfg.APIPort <= 0 {
		return nil
	}

	active := cfg.BuilderAPIEnabled
	forced := false

	if override := plan.BuilderAPIOverride(); override != nil {
		forced = *override && !cfg.BuilderAPIEnabled
		active = *override
	}

	if !active {
		return nil
	}

	resolved := &ResolvedBuilderAPISettings{
		SubsidyGwei: cfg.BuilderAPI.BlockValueSubsidyGwei,
		Forced:      forced,
	}

	if cfg.BuilderAPI.ValueOverrideGwei > 0 {
		value := cfg.BuilderAPI.ValueOverrideGwei
		resolved.TotalValueGwei = &value
	}

	if plan != nil && plan.BuilderAPI != nil && plan.BuilderAPI.Mode == ModeCustom {
		api := plan.BuilderAPI
		applyOverride(&resolved.SubsidyGwei, api.ValueSubsidyGwei)
		applyOverride(&resolved.DelayMs, api.ResponseDelayMs)

		if api.TotalValueOverrideGwei != nil {
			resolved.TotalValueGwei = cloneScalar(api.TotalValueOverrideGwei)
		}
	}

	return resolved
}

func resolveReveal(plan *SlotPlan, cfg *config.Config) *ResolvedRevealSettings {
	resolved := &ResolvedRevealSettings{
		RevealTimeMs: cfg.EPBS.RevealTime,
	}

	if plan == nil || plan.Reveal == nil {
		return resolved
	}

	if plan.Reveal.Mode == ModeDisabled {
		resolved.Suppressed = true

		return resolved
	}

	resolved.BypassDeadline = true
	applyOverride(&resolved.RevealTimeMs, plan.Reveal.RevealTimeMs)

	return resolved
}

func applyOverride[T any](target *T, override *T) {
	if override != nil {
		*target = *override
	}
}
