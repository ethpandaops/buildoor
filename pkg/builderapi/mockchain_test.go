package builderapi

import (
	"context"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// mockChainService is a minimal chain.Service for tests. It returns the
// configured genesis, fork version, and current fork; all other accessors
// return zero values. The default zero-valued genesis/fork version matches
// the zero-version domains the test signatures are built against; the default
// currentFork (DataVersionUnknown) is pre-Gloas, so legacy-dialect tests pass
// the fork guards while post-Gloas paths require setting currentFork.
type mockChainService struct {
	genesis     beacon.Genesis
	forkVersion phase0.Version
	currentFork version.DataVersion
}

var _ chain.Service = (*mockChainService)(nil)

func (m *mockChainService) Start(context.Context) error { return nil }
func (m *mockChainService) Stop() error                 { return nil }

func (m *mockChainService) GetChainSpec() *chain.ChainSpec { return nil }
func (m *mockChainService) GetGenesis() *beacon.Genesis    { return &m.genesis }

func (m *mockChainService) SlotToTime(phase0.Slot) time.Time { return time.Time{} }
func (m *mockChainService) TimeToSlot(time.Time) phase0.Slot { return 0 }
func (m *mockChainService) GetCurrentEpoch() phase0.Epoch    { return 0 }
func (m *mockChainService) GetCurrentSlot() phase0.Slot      { return 0 }

func (m *mockChainService) GetCurrentFork() version.DataVersion { return m.currentFork }
func (m *mockChainService) ActiveForkAtEpoch(phase0.Epoch) version.DataVersion {
	return m.currentFork
}
func (m *mockChainService) GetForkVersion() (phase0.Version, error)      { return m.forkVersion, nil }
func (m *mockChainService) GetEpochOfSlot(phase0.Slot) phase0.Epoch      { return 0 }
func (m *mockChainService) GetCurrentEpochStats() *chain.EpochStats      { return nil }
func (m *mockChainService) GetEpochStats(phase0.Epoch) *chain.EpochStats { return nil }

func (m *mockChainService) SubscribeEpochStats() *utils.Subscription[*chain.EpochStats] { return nil }
func (m *mockChainService) GetHeadVoteTracker() *chain.HeadVoteTracker                  { return nil }
func (m *mockChainService) GetFinalizedEpoch() phase0.Epoch                             { return 0 }

func (m *mockChainService) GetBuilderByIndex(uint64) *chain.BuilderInfo            { return nil }
func (m *mockChainService) GetBuilderByPubkey(phase0.BLSPubKey) *chain.BuilderInfo { return nil }
func (m *mockChainService) GetBuilders() []*chain.BuilderInfo                      { return nil }

func (m *mockChainService) GetValidatorPubkeyByIndex(phase0.ValidatorIndex) *phase0.BLSPubKey {
	return nil
}

func (m *mockChainService) RefreshBuilders(context.Context) error { return nil }
