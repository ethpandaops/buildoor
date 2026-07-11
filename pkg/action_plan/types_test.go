package action_plan

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func int64Ptr(v int64) *int64    { return &v }
func uint64Ptr(v uint64) *uint64 { return &v }

func TestApplyUpdateToPlanThreeStateSemantics(t *testing.T) {
	existing := &SlotPlan{
		Bid:    &BidPlan{Mode: ModeCustom, BidMinAmount: uint64Ptr(5000)},
		Reveal: &RevealPlan{Mode: ModeDisabled},
	}

	tests := []struct {
		name   string
		update *PlanUpdate
		check  func(t *testing.T, result *SlotPlan)
	}{
		{
			name:   "absent members keep current categories",
			update: &PlanUpdate{},
			check: func(t *testing.T, result *SlotPlan) {
				require.NotNil(t, result.Bid)
				require.Equal(t, uint64(5000), *result.Bid.BidMinAmount)
				require.NotNil(t, result.Reveal)
				require.Nil(t, result.BuilderAPI)
			},
		},
		{
			name:   "null member clears the category",
			update: &PlanUpdate{Reveal: json.RawMessage("null")},
			check: func(t *testing.T, result *SlotPlan) {
				require.Nil(t, result.Reveal)
				require.NotNil(t, result.Bid)
			},
		},
		{
			name:   "object member replaces the category",
			update: &PlanUpdate{Bid: json.RawMessage(`{"mode":"disabled"}`)},
			check: func(t *testing.T, result *SlotPlan) {
				require.NotNil(t, result.Bid)
				require.Equal(t, ModeDisabled, result.Bid.Mode)
				require.Nil(t, result.Bid.BidMinAmount, "replace must not merge old overrides")
			},
		},
		{
			name: "new category added alongside existing ones",
			update: &PlanUpdate{
				BuilderAPI: json.RawMessage(`{"mode":"custom","response_delay_ms":250}`),
			},
			check: func(t *testing.T, result *SlotPlan) {
				require.NotNil(t, result.BuilderAPI)
				require.Equal(t, int64(250), *result.BuilderAPI.ResponseDelayMs)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ApplyUpdateToPlan(existing, tt.update)
			require.NoError(t, err)
			require.NotNil(t, result)
			tt.check(t, result)

			// The source plan must never be mutated.
			require.Equal(t, ModeCustom, existing.Bid.Mode)
			require.Equal(t, uint64(5000), *existing.Bid.BidMinAmount)
			require.NotNil(t, existing.Reveal)
			require.Nil(t, existing.BuilderAPI)
		})
	}
}

func TestApplyUpdateToPlanDeleteAndEmpty(t *testing.T) {
	existing := &SlotPlan{Bid: &BidPlan{Mode: ModeDisabled}}

	result, err := ApplyUpdateToPlan(existing, &PlanUpdate{Delete: true})
	require.NoError(t, err)
	require.Nil(t, result)

	// Clearing the last category deletes the plan.
	result, err = ApplyUpdateToPlan(existing, &PlanUpdate{Bid: json.RawMessage("null")})
	require.NoError(t, err)
	require.Nil(t, result)

	// An update on a nil plan creating nothing stays nil.
	result, err = ApplyUpdateToPlan(nil, &PlanUpdate{Reveal: json.RawMessage("null")})
	require.NoError(t, err)
	require.Nil(t, result)
}

func TestApplyUpdateToPlanRejectsUnknownFieldsAndModes(t *testing.T) {
	_, err := ApplyUpdateToPlan(nil, &PlanUpdate{
		Bid: json.RawMessage(`{"mode":"custom","no_such_field":1}`),
	})
	require.Error(t, err)

	result, err := ApplyUpdateToPlan(nil, &PlanUpdate{
		Bid: json.RawMessage(`{"mode":"sometimes"}`),
	})
	require.NoError(t, err, "mode enum is checked by Validate, not the patch decoder")
	require.Error(t, result.Validate(12*time.Second))
}

func TestSlotPlanValidate(t *testing.T) {
	secondsPerSlot := 12 * time.Second

	tests := []struct {
		name    string
		plan    *SlotPlan
		wantErr string
	}{
		{
			name: "valid custom bid with signed negative window",
			plan: &SlotPlan{Bid: &BidPlan{
				Mode:         ModeCustom,
				BidStartTime: int64Ptr(-400),
				BidEndTime:   int64Ptr(-100),
			}},
		},
		{
			name: "bid window start after end",
			plan: &SlotPlan{Bid: &BidPlan{
				Mode:         ModeCustom,
				BidStartTime: int64Ptr(100),
				BidEndTime:   int64Ptr(-100),
			}},
			wantErr: "must be before",
		},
		{
			name: "bid time out of slot bounds",
			plan: &SlotPlan{Bid: &BidPlan{
				Mode:         ModeCustom,
				BidStartTime: int64Ptr(-13000),
			}},
			wantErr: "within",
		},
		{
			name: "negative bid interval",
			plan: &SlotPlan{Bid: &BidPlan{
				Mode:        ModeCustom,
				BidInterval: int64Ptr(-1),
			}},
			wantErr: "bid_interval",
		},
		{
			name:    "overrides on disabled bid",
			plan:    &SlotPlan{Bid: &BidPlan{Mode: ModeDisabled, BidSubsidy: uint64Ptr(1)}},
			wantErr: "custom mode",
		},
		{
			name: "reveal beyond one extra slot",
			plan: &SlotPlan{Reveal: &RevealPlan{
				Mode:         ModeCustom,
				RevealTimeMs: int64Ptr(30000),
			}},
			wantErr: "reveal_time_ms",
		},
		{
			name: "late reveal within the clamp",
			plan: &SlotPlan{Reveal: &RevealPlan{
				Mode:         ModeCustom,
				RevealTimeMs: int64Ptr(20000),
			}},
		},
		{
			name: "builder api delay above slot duration",
			plan: &SlotPlan{BuilderAPI: &BuilderAPIPlan{
				Mode:            ModeCustom,
				ResponseDelayMs: int64Ptr(15000),
			}},
			wantErr: "response_delay_ms",
		},
		{
			name:    "invalid mode",
			plan:    &SlotPlan{BuilderAPI: &BuilderAPIPlan{Mode: Mode("maybe")}},
			wantErr: "invalid mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.plan.Validate(secondsPerSlot)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestPlanUpdateTargetSlots(t *testing.T) {
	from := uint64(100)
	to := uint64(103)

	targets, err := (&PlanUpdate{Slots: []uint64{7, 9}, FromSlot: &from, ToSlot: &to}).TargetSlots()
	require.NoError(t, err)
	require.Len(t, targets, 6)

	_, err = (&PlanUpdate{}).TargetSlots()
	require.ErrorContains(t, err, "no slots")

	_, err = (&PlanUpdate{FromSlot: &from}).TargetSlots()
	require.ErrorContains(t, err, "together")

	badTo := uint64(50)
	_, err = (&PlanUpdate{FromSlot: &from, ToSlot: &badTo}).TargetSlots()
	require.ErrorContains(t, err, "invalid slot range")

	// Overflow-safe huge range rejection without allocation.
	hugeFrom := uint64(0)
	hugeTo := uint64(1) << 62
	_, err = (&PlanUpdate{FromSlot: &hugeFrom, ToSlot: &hugeTo}).TargetSlots()
	require.ErrorContains(t, err, "max")
}

func TestSlotPlanEnableOverrides(t *testing.T) {
	var nilPlan *SlotPlan

	require.Nil(t, nilPlan.BidOverride())
	require.Nil(t, (&SlotPlan{}).BidOverride())

	custom := &SlotPlan{
		Bid:        &BidPlan{Mode: ModeCustom},
		BuilderAPI: &BuilderAPIPlan{Mode: ModeDisabled},
		Reveal:     &RevealPlan{Mode: ModeCustom},
	}

	require.True(t, *custom.BidOverride())
	require.False(t, *custom.BuilderAPIOverride())
	require.True(t, *custom.RevealOverride())
}

func TestSlotPlanCloneIsDeep(t *testing.T) {
	original := &SlotPlan{
		Bid: &BidPlan{Mode: ModeCustom, BidValueGwei: uint64Ptr(42)},
	}

	clone := original.Clone()
	*clone.Bid.BidValueGwei = 7
	clone.Bid.Mode = ModeDisabled

	require.Equal(t, uint64(42), *original.Bid.BidValueGwei)
	require.Equal(t, ModeCustom, original.Bid.Mode)
}
