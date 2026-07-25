package action_plan

import (
	"encoding/json"
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/config"
)

// slot31Rule is the motivating scenario: win the last slot of every epoch and
// never reveal its payload.
func slot31Rule() *SlotRule {
	value := uint64(1_000_000)

	return &SlotRule{
		ID:           "slot31-withhold",
		Enabled:      true,
		Description:  "win slot 31 and withhold the payload",
		SlotsInEpoch: []uint64{31},
		Bid: &BidPlan{
			Mode:               ModeCustom,
			BidValueGwei:       &value,
			IgnoreMissingPrefs: true,
		},
		Reveal: &RevealPlan{Mode: ModeDisabled},
	}
}

func TestSlotRuleMatches(t *testing.T) {
	rule := slot31Rule()

	require.True(t, rule.Matches(31, 32))
	require.True(t, rule.Matches(32*500+31, 32))
	require.False(t, rule.Matches(30, 32))
	require.False(t, rule.Matches(32, 32))

	// A disabled rule never matches, and an unknown epoch length cannot match.
	rule.Enabled = false
	require.False(t, rule.Matches(31, 32))

	rule.Enabled = true
	require.False(t, rule.Matches(31, 0))
}

func TestSlotRuleEpochBounds(t *testing.T) {
	rule := slot31Rule()
	from, to := uint64(10), uint64(12)
	rule.FromEpoch = &from
	rule.ToEpoch = &to

	require.False(t, rule.Matches(9*32+31, 32))
	require.True(t, rule.Matches(10*32+31, 32))
	require.True(t, rule.Matches(12*32+31, 32))
	require.False(t, rule.Matches(13*32+31, 32))
}

func TestSlotRuleValidate(t *testing.T) {
	chainSvc := newStubChain()
	spec := chainSvc.spec

	require.NoError(t, slot31Rule().Validate(spec.SlotsPerEpoch, spec.SecondsPerSlot))

	tests := []struct {
		name   string
		mutate func(*SlotRule)
		errMsg string
	}{
		{"bad id", func(r *SlotRule) { r.ID = "Not A Slug" }, "must match"},
		{"no slots", func(r *SlotRule) { r.SlotsInEpoch = nil }, "must not be empty"},
		{"slot out of range", func(r *SlotRule) { r.SlotsInEpoch = []uint64{32} }, "out of range"},
		{"duplicate slot", func(r *SlotRule) { r.SlotsInEpoch = []uint64{31, 31} }, "duplicate slot index"},
		{
			"inverted epoch range",
			func(r *SlotRule) {
				from, to := uint64(9), uint64(5)
				r.FromEpoch, r.ToEpoch = &from, &to
			},
			"invalid epoch range",
		},
		{
			"no instruction",
			func(r *SlotRule) { r.Bid, r.Reveal = nil, nil },
			"carries no instruction",
		},
		{
			"invalid category",
			func(r *SlotRule) { r.Reveal = &RevealPlan{Mode: "bogus"} },
			"invalid mode",
		},
		{
			"overrides on a disabled category",
			func(r *SlotRule) {
				start := int64(100)
				r.Bid = &BidPlan{Mode: ModeDisabled, BidStartTime: &start}
			},
			"overrides are only allowed in custom mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := slot31Rule()
			tt.mutate(rule)
			require.ErrorContains(t, rule.Validate(spec.SlotsPerEpoch, spec.SecondsPerSlot), tt.errMsg)
		})
	}
}

func TestSetRulesAtomicAndPersistedShape(t *testing.T) {
	svc := newTestService(newStubChain(), nil)

	rules, err := svc.SetRules([]*SlotRule{slot31Rule()}, "tester")
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.Equal(t, "tester", rules[0].UpdatedBy)
	require.False(t, rules[0].UpdatedAt.IsZero())

	// A rejected batch leaves the committed set untouched.
	broken := slot31Rule()
	broken.ID = "second"
	broken.SlotsInEpoch = []uint64{99}

	_, err = svc.SetRules([]*SlotRule{slot31Rule(), broken}, "tester")
	require.ErrorContains(t, err, "out of range")
	require.Len(t, svc.Rules(), 1)

	// Duplicate ids are rejected before anything is committed.
	_, err = svc.SetRules([]*SlotRule{slot31Rule(), slot31Rule()}, "tester")
	require.ErrorContains(t, err, "duplicate rule id")

	// The set is replaced wholesale: the old rule is gone.
	replacement := slot31Rule()
	replacement.ID = "other"

	rules, err = svc.SetRules([]*SlotRule{replacement}, "tester")
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.Equal(t, "other", rules[0].ID)

	// Clearing works, and callers never hold a reference into the store.
	rules, err = svc.SetRules(nil, "tester")
	require.NoError(t, err)
	require.Empty(t, rules)
}

func TestRulesAreCloned(t *testing.T) {
	svc := newTestService(newStubChain(), nil)
	rule := slot31Rule()

	_, err := svc.SetRules([]*SlotRule{rule}, "tester")
	require.NoError(t, err)

	// Mutating the caller's rule must not reach the store.
	rule.SlotsInEpoch[0] = 7
	rule.Enabled = false
	*rule.Bid.BidValueGwei = 1

	stored := svc.Rules()
	require.Equal(t, []uint64{31}, stored[0].SlotsInEpoch)
	require.True(t, stored[0].Enabled)
	require.Equal(t, uint64(1_000_000), *stored[0].Bid.BidValueGwei)

	// Nor does mutating a returned clone.
	stored[0].SlotsInEpoch[0] = 7
	require.Equal(t, []uint64{31}, svc.Rules()[0].SlotsInEpoch)
}

func TestPlanForSlotResolvesRules(t *testing.T) {
	svc := newTestService(newStubChain(), nil)

	_, err := svc.SetRules([]*SlotRule{slot31Rule()}, "tester")
	require.NoError(t, err)

	matched := svc.PlanForSlot(2047) // 2047 = epoch 63, slot 31
	require.NotNil(t, matched)
	require.Equal(t, "slot31-withhold", matched.RuleID)
	require.Equal(t, phase0.Slot(2047), matched.Slot)
	require.Equal(t, ModeDisabled, matched.Reveal.Mode)

	require.Nil(t, svc.PlanForSlot(2046))

	// Explicit plans win wholesale — the rule's reveal instruction is gone.
	_, err = svc.ApplyUpdates([]*PlanUpdate{{
		Slots: []uint64{2047},
		Bid:   json.RawMessage(`{"mode":"custom","bid_min_amount":5}`),
	}}, "tester")
	require.NoError(t, err)

	explicit := svc.PlanForSlot(2047)
	require.Empty(t, explicit.RuleID)
	require.Nil(t, explicit.Reveal)
	require.Equal(t, uint64(5), *explicit.Bid.BidMinAmount)

	// Get only ever reports the stored plan.
	require.Nil(t, svc.Get(2079))
}

func TestIgnoreRulesOptsSlotOut(t *testing.T) {
	svc := newTestService(newStubChain(), nil)

	_, err := svc.SetRules([]*SlotRule{slot31Rule()}, "tester")
	require.NoError(t, err)

	ignore := true

	event, err := svc.ApplyUpdates([]*PlanUpdate{{
		Slots:       []uint64{2047},
		IgnoreRules: &ignore,
	}}, "tester")
	require.NoError(t, err)
	require.NotNil(t, event.Plans[0], "an opt-out is an instruction, not an empty plan")
	require.True(t, event.Plans[0].IgnoreRules)

	plan := svc.PlanForSlot(2047)
	require.NotNil(t, plan)
	require.Empty(t, plan.RuleID)
	require.Nil(t, plan.Bid)
	require.Nil(t, plan.Reveal)

	// The slot runs on the plain global baseline again.
	frozen := svc.Freeze(2047)
	require.NotNil(t, frozen.Reveal)
	require.False(t, frozen.Reveal.Suppressed)
}

func TestFreezeAppliesRule(t *testing.T) {
	chainSvc := newStubChain()
	svc := newTestService(chainSvc, nil)

	_, err := svc.SetRules([]*SlotRule{slot31Rule()}, "tester")
	require.NoError(t, err)

	frozen := svc.Freeze(2047)
	require.NotNil(t, frozen.Plan)
	require.Equal(t, "slot31-withhold", frozen.Plan.RuleID)

	// The rule forces the build past the schedule and suppresses the reveal.
	require.True(t, frozen.Build.Build)
	require.True(t, frozen.Build.Forced)
	require.NotNil(t, frozen.Bid)
	require.Equal(t, uint64(1_000_000), *frozen.Bid.ValueGwei)
	require.True(t, frozen.Bid.IgnoreMissingPrefs)
	require.True(t, frozen.Reveal.Suppressed)

	// A frozen slot keeps its snapshot even when the rule set changes.
	_, err = svc.SetRules(nil, "tester")
	require.NoError(t, err)
	require.True(t, svc.Freeze(2047).Reveal.Suppressed)
}

func TestGetRangeIncludesRulePlans(t *testing.T) {
	chainSvc := newStubChain()
	chainSvc.currentSlot = 2020
	svc := newTestService(chainSvc, nil)

	_, err := svc.SetRules([]*SlotRule{slot31Rule()}, "tester")
	require.NoError(t, err)

	// Epochs 62..64 cover slots 1984..2079, so 2015, 2047 and 2079 match the
	// rule — but 2015 has already passed.
	plans := svc.GetRange(1984, 2079)
	require.Len(t, plans, 2, "past slots keep their recorded history, not the current rule")
	require.Equal(t, phase0.Slot(2047), plans[0].Slot)
	require.Equal(t, phase0.Slot(2079), plans[1].Slot)
	require.Equal(t, "slot31-withhold", plans[0].RuleID)

	// A frozen slot is no longer open for the rule set to describe.
	svc.Freeze(2047)
	plans = svc.GetRange(1984, 2079)
	require.Len(t, plans, 1)
	require.Equal(t, phase0.Slot(2079), plans[0].Slot)

	// Explicit plans replace the rule-derived entry rather than duplicating it.
	_, err = svc.ApplyUpdates([]*PlanUpdate{{
		Slots: []uint64{2079},
		Bid:   json.RawMessage(`{"mode":"disabled"}`),
	}}, "tester")
	require.NoError(t, err)

	plans = svc.GetRange(1984, 2079)
	require.Len(t, plans, 1)
	require.Equal(t, phase0.Slot(2079), plans[0].Slot)
	require.Empty(t, plans[0].RuleID)
	require.Equal(t, ModeDisabled, plans[0].Bid.Mode)
}

func TestRuleForcedBuildDoesNotConsumeNextN(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.EPBSEnabled = true
	cfg.BuilderAPIEnabled = true
	cfg.APIPort = 8080
	cfg.Schedule.Mode = config.ScheduleModeNextN
	cfg.Schedule.NextN = 1

	svc := newTestService(newStubChain(), cfg)

	_, err := svc.SetRules([]*SlotRule{slot31Rule()}, "tester")
	require.NoError(t, err)

	// A rule forces the build past the schedule, so it must not spend budget.
	forced := svc.Freeze(2047)
	require.True(t, forced.Build.Build)
	require.True(t, forced.Build.Forced)

	svc.OnSlotBuilt(2047)
	require.Equal(t, uint64(0), svc.GetSlotsBuilt())
	require.Equal(t, 1, svc.GetSlotsRemaining())

	// A scheduled build on an unmatched slot still consumes it.
	scheduled := svc.Freeze(2048)
	require.True(t, scheduled.Build.Build)
	require.False(t, scheduled.Build.Forced)

	svc.OnSlotBuilt(2048)
	require.Equal(t, uint64(1), svc.GetSlotsBuilt())
	require.Equal(t, 0, svc.GetSlotsRemaining())

	// With the budget spent the rule keeps forcing its own slots.
	require.True(t, svc.Freeze(2079).Build.Build)
	require.False(t, svc.Freeze(2080).Build.Build)
}

func TestUnmatchableRulesAfterSpecChange(t *testing.T) {
	chainSvc := newStubChain()
	svc := newTestService(chainSvc, nil)

	_, err := svc.SetRules([]*SlotRule{slot31Rule()}, "tester")
	require.NoError(t, err)
	require.Empty(t, svc.unmatchableRules())

	// Same state-db, network with a shorter epoch: slot index 31 cannot occur.
	chainSvc.spec.SlotsPerEpoch = 8

	require.Equal(t, []string{"slot31-withhold"}, svc.unmatchableRules())
	require.Nil(t, svc.PlanForSlot(2047), "an unmatchable rule must not resolve")
}

func TestMatchRuleOrderIsDeterministic(t *testing.T) {
	svc := newTestService(newStubChain(), nil)

	first := slot31Rule()
	first.ID = "aaa-first"

	second := slot31Rule()
	second.ID = "zzz-second"
	second.Reveal = &RevealPlan{Mode: ModeCustom}

	_, err := svc.SetRules([]*SlotRule{second, first}, "tester")
	require.NoError(t, err)

	require.Equal(t, "aaa-first", svc.PlanForSlot(2047).RuleID)
}

func TestRuleCodecRoundTrip(t *testing.T) {
	codec := RuleCodec{}

	encoded, err := codec.EncodeValue(slot31Rule())
	require.NoError(t, err)

	decoded, err := codec.DecodeValue(encoded)
	require.NoError(t, err)
	require.Equal(t, slot31Rule().SlotsInEpoch, decoded.SlotsInEpoch)
	require.Equal(t, ModeDisabled, decoded.Reveal.Mode)

	key, err := codec.DecodeKey(codec.EncodeKey("slot31-withhold"))
	require.NoError(t, err)
	require.Equal(t, "slot31-withhold", key)

	_, err = codec.DecodeKey("")
	require.Error(t, err)
}
