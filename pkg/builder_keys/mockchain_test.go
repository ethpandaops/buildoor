package builder_keys

import (
	"context"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// stubChainService is a minimal chain.Service exposing a fixed builder registry
// and finality, which is all the registry reads (shape copied from
// pkg/payload_bidder/mockchain_test.go).
type stubChainService struct {
	builders       []*chain.BuilderInfo
	currentEpoch   phase0.Epoch
	finalizedEpoch phase0.Epoch
	genesis        beacon.Genesis

	epochStatsDispatch utils.Dispatcher[*chain.EpochStats]
}

var _ chain.Service = (*stubChainService)(nil)

func (m *stubChainService) Start(context.Context) error { return nil }
func (m *stubChainService) Stop() error                 { return nil }

func (m *stubChainService) GetChainSpec() *chain.ChainSpec {
	return &chain.ChainSpec{SecondsPerSlot: 12 * time.Second, SlotsPerEpoch: 32}
}
func (m *stubChainService) GetGenesis() *beacon.Genesis { return &m.genesis }

func (m *stubChainService) SlotToTime(phase0.Slot) time.Time { return time.Time{} }
func (m *stubChainService) TimeToSlot(time.Time) phase0.Slot { return 0 }
func (m *stubChainService) GetCurrentEpoch() phase0.Epoch    { return m.currentEpoch }
func (m *stubChainService) GetCurrentSlot() phase0.Slot      { return 0 }
func (m *stubChainService) GetFinalizedEpoch() phase0.Epoch  { return m.finalizedEpoch }
func (m *stubChainService) GetCurrentFork() version.DataVersion {
	return version.DataVersionGloas
}

func (m *stubChainService) ActiveForkAtEpoch(phase0.Epoch) version.DataVersion {
	return version.DataVersionGloas
}
func (m *stubChainService) GetForkVersion() (phase0.Version, error) { return phase0.Version{}, nil }

func (m *stubChainService) GetEpochOfSlot(slot phase0.Slot) phase0.Epoch {
	return phase0.Epoch(uint64(slot) / 32)
}

func (m *stubChainService) GetCurrentEpochStats() *chain.EpochStats      { return nil }
func (m *stubChainService) GetEpochStats(phase0.Epoch) *chain.EpochStats { return nil }

func (m *stubChainService) SubscribeEpochStats() *utils.Subscription[*chain.EpochStats] {
	return m.epochStatsDispatch.Subscribe(4, false)
}

func (m *stubChainService) GetHeadVoteTracker() *chain.HeadVoteTracker { return nil }
func (m *stubChainService) GetHeadTracker() *chain.HeadTracker         { return nil }

func (m *stubChainService) GetBuilderByIndex(index uint64) *chain.BuilderInfo {
	for _, info := range m.builders {
		if info.Index == index {
			return info
		}
	}

	return nil
}

func (m *stubChainService) GetBuilderByPubkey(pubkey phase0.BLSPubKey) *chain.BuilderInfo {
	for _, info := range m.builders {
		if info.Pubkey == pubkey {
			return info
		}
	}

	return nil
}

func (m *stubChainService) GetBuilders() []*chain.BuilderInfo { return m.builders }

func (m *stubChainService) GetValidatorPubkeyByIndex(phase0.ValidatorIndex) *phase0.BLSPubKey {
	return nil
}

func (m *stubChainService) RefreshBuilders(context.Context) error { return nil }
