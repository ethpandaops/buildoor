package action_plan

import (
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"

	"github.com/ethpandaops/buildoor/pkg/config"
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

	// Resolved effective settings; a nil category = suppressed for this slot
	// (by plan or by the global enable flags / protocol applicability).
	Bid        *ResolvedBidSettings        `json:"bid,omitempty"`
	BuilderAPI *ResolvedBuilderAPISettings `json:"builder_api,omitempty"`
	Reveal     *ResolvedRevealSettings     `json:"reveal,omitempty"`
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
// may be nil) into the frozen execution snapshot.
func resolveFrozenPlan(slot phase0.Slot, plan *SlotPlan, cfg *config.Config,
	fork version.DataVersion, frozenAt time.Time) *FrozenPlan {
	frozen := &FrozenPlan{
		Slot:     slot,
		Plan:     plan,
		Fork:     fork.String(),
		FrozenAt: frozenAt,
	}

	frozen.Bid = resolveBid(plan, cfg, fork)
	frozen.BuilderAPI = resolveBuilderAPI(plan, cfg)
	frozen.Reveal = resolveReveal(plan, cfg)

	return frozen
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
