package action_plan

import (
	"testing"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/db"
)

// TestFreezeRescalesTimingAtSlotDurationChange: the last 12s slot freezes
// with the 12s timings, the first 9s slot rescales the global timing
// settings once and freezes with them.
func TestFreezeRescalesTimingAtSlotDurationChange(t *testing.T) {
	chainSvc := newStubChain()
	chainSvc.spec.ForkSchedule = []chain.ForkSchedule{{Fork: version.DataVersionHeze, Epoch: 5}}
	chainSvc.spec.ForkSlotDurations = map[version.DataVersion]time.Duration{version.DataVersionHeze: 9 * time.Second}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	defaults := config.DefaultConfig()
	defaults.ApplySlotDefaults(12000)

	cfg := *defaults
	cfg.EPBSEnabled = true
	cfg.Reveal.TimeMs = 5000

	store := db.NewDatabase(&db.Config{}, log)
	require.NoError(t, store.Init())

	settings, err := config.NewService(&cfg, defaults, map[string]bool{config.KeyRevealTimeMs: true}, store, log)
	require.NoError(t, err)
	settings.InitSlotTiming(config.SlotTiming{DurationMs: 12000})

	svc := NewPlanService(&cfg, chainSvc, log)
	svc.SetSettings(settings)

	last12s := svc.Freeze(159)
	require.Equal(t, int64(5000), last12s.Reveal.RevealTimeMs)
	require.Equal(t, int64(-3400), last12s.Build.BuildStartTimeMs)

	first9s := svc.Freeze(160)
	require.Equal(t, int64(3750), first9s.Reveal.RevealTimeMs)
	require.Equal(t, int64(-2550), first9s.Build.BuildStartTimeMs)
	require.Equal(t, config.SlotTiming{Slot: 160, DurationMs: 9000}, settings.SlotTiming())

	// Later slots of the segment do not scale again.
	require.Equal(t, int64(3750), svc.Freeze(200).Reveal.RevealTimeMs)
}

// TestSlotRuleValidateAcrossSlotDurations: a rule is validated against every
// slot duration within its epoch window.
func TestSlotRuleValidateAcrossSlotDurations(t *testing.T) {
	spec := newStubChain().spec
	spec.ForkSchedule = []chain.ForkSchedule{{Fork: version.DataVersionHeze, Epoch: 5}}
	spec.ForkSlotDurations = map[version.DataVersion]time.Duration{version.DataVersionHeze: 9 * time.Second}

	// 10.5s into the slot: within a 12s slot, beyond a 9s one.
	start, end := int64(10_000), int64(10_500)
	rule := func(from, to *uint64) *SlotRule {
		r := slot31Rule()
		r.Bid.BidStartTime = &start
		r.Bid.BidEndTime = &end
		r.FromEpoch, r.ToEpoch = from, to

		return r
	}

	epoch := func(e uint64) *uint64 { return &e }

	require.NoError(t, rule(nil, epoch(4)).Validate(spec))
	require.ErrorContains(t, rule(nil, epoch(5)).Validate(spec), "bid.bid_start_time")
	require.ErrorContains(t, rule(epoch(5), nil).Validate(spec), "bid.bid_start_time")
	require.ErrorContains(t, rule(nil, nil).Validate(spec), "bid.bid_start_time")
}
