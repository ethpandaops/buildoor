package payload_builder

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// stubChainService provides the minimal chain.Service surface used by the
// plan service in these tests.
type stubChainService struct {
	chain.Service

	spec *chain.ChainSpec
}

func (s *stubChainService) GetChainSpec() *chain.ChainSpec { return s.spec }
func (s *stubChainService) GetCurrentSlot() phase0.Slot    { return 100 }
func (s *stubChainService) GetEpochOfSlot(slot phase0.Slot) phase0.Epoch {
	return phase0.Epoch(uint64(slot) / s.spec.SlotsPerEpoch)
}
func (s *stubChainService) ActiveForkAtEpoch(_ phase0.Epoch) version.DataVersion {
	return version.DataVersionGloas
}
func (s *stubChainService) SubscribeEpochStats() *utils.Subscription[*chain.EpochStats] {
	return (&utils.Dispatcher[*chain.EpochStats]{}).Subscribe(1, false)
}

func newSkipTestService(t *testing.T, cfg *config.Config) (*Service, *action_plan.PlanService) {
	t.Helper()

	chainSvc := &stubChainService{spec: &chain.ChainSpec{
		SecondsPerSlot: 12 * time.Second,
		SlotsPerEpoch:  32,
	}}

	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	planSvc := action_plan.NewPlanService(cfg, chainSvc, log)

	svc, err := NewService(cfg, nil, chainSvc, planSvc, nil, common.Address{}, log)
	require.NoError(t, err)

	return svc, planSvc
}

func TestFireBuildSkippedDedupesPerSlot(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.EPBSEnabled = true

	svc, planSvc := newSkipTestService(t, cfg)

	sub := svc.SubscribeBuildSkipped(4, false)
	defer sub.Unsubscribe()

	// Schedule-skipped slot with an active consumer → one event, deduped.
	cfg.Schedule.Mode = config.ScheduleModeNextN
	cfg.Schedule.NextN = 0

	frozen := planSvc.Freeze(500)
	require.False(t, frozen.Build.Build)

	svc.fireBuildSkipped(500, frozen.Build)
	svc.fireBuildSkipped(500, frozen.Build)

	select {
	case event := <-sub.Channel():
		require.Equal(t, phase0.Slot(500), event.Slot)
		require.Equal(t, action_plan.BuildSkipReasonSchedule, event.Reason)
	case <-time.After(time.Second):
		t.Fatal("expected a build skipped event")
	}

	select {
	case <-sub.Channel():
		t.Fatal("skip event must be deduped per slot")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestFireBuildSkippedSilentWithoutPlanInvolvement(t *testing.T) {
	cfg := config.DefaultConfig() // everything disabled, no consumers

	svc, planSvc := newSkipTestService(t, cfg)

	sub := svc.SubscribeBuildSkipped(4, false)
	defer sub.Unsubscribe()

	frozen := planSvc.Freeze(600)
	require.False(t, frozen.Build.Build)
	require.False(t, frozen.Build.PlanInvolved)

	svc.fireBuildSkipped(600, frozen.Build)

	select {
	case <-sub.Channel():
		t.Fatal("no event expected when neither plan nor consumer is involved")
	case <-time.After(50 * time.Millisecond):
	}
}
