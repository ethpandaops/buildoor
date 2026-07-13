package payload_bidder

import (
	"context"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/holiman/uint256"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// stubChainService is a minimal chain.Service for tests with controllable
// slot timing, current fork, and genesis (shape copied from
// pkg/builderapi/mockchain_test.go).
type stubChainService struct {
	genesisTime  time.Time
	slotDuration time.Duration
	currentFork  version.DataVersion
	genesis      beacon.Genesis
	currentSlot  func() phase0.Slot

	epochStatsDispatch utils.Dispatcher[*chain.EpochStats]
}

var _ chain.Service = (*stubChainService)(nil)

func (m *stubChainService) Start(context.Context) error { return nil }
func (m *stubChainService) Stop() error                 { return nil }

func (m *stubChainService) GetChainSpec() *chain.ChainSpec {
	return &chain.ChainSpec{SecondsPerSlot: m.slotDuration, SlotsPerEpoch: 32}
}
func (m *stubChainService) GetGenesis() *beacon.Genesis { return &m.genesis }

func (m *stubChainService) SlotToTime(slot phase0.Slot) time.Time {
	return m.genesisTime.Add(time.Duration(slot) * m.slotDuration) //nolint:gosec // test helper
}

func (m *stubChainService) TimeToSlot(t time.Time) phase0.Slot {
	return phase0.Slot(t.Sub(m.genesisTime) / m.slotDuration) //nolint:gosec // test helper
}

func (m *stubChainService) GetCurrentEpoch() phase0.Epoch { return 0 }
func (m *stubChainService) GetCurrentSlot() phase0.Slot {
	if m.currentSlot != nil {
		return m.currentSlot()
	}

	return 0
}

func (m *stubChainService) GetCurrentFork() version.DataVersion { return m.currentFork }
func (m *stubChainService) ActiveForkAtEpoch(phase0.Epoch) version.DataVersion {
	return m.currentFork
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
func (m *stubChainService) GetFinalizedEpoch() phase0.Epoch            { return 0 }

func (m *stubChainService) GetBuilderByIndex(uint64) *chain.BuilderInfo            { return nil }
func (m *stubChainService) GetBuilderByPubkey(phase0.BLSPubKey) *chain.BuilderInfo { return nil }
func (m *stubChainService) GetBuilders() []*chain.BuilderInfo                      { return nil }

func (m *stubChainService) GetValidatorPubkeyByIndex(phase0.ValidatorIndex) *phase0.BLSPubKey {
	return nil
}

func (m *stubChainService) RefreshBuilders(context.Context) error { return nil }

// newTestBuilderSvc creates a real payload_builder.Service (stats + payload
// cache work without Start).
func newTestBuilderSvc(chainSvc chain.Service) *payload_builder.Service {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	svc, err := payload_builder.NewService(&config.Config{}, nil, chainSvc, nil, common.Address{}, log)
	if err != nil {
		panic(err)
	}

	return svc
}

// newTestPayload builds a minimal Gloas-version payload sufficient for
// envelope signing and inclusion tracking.
func newTestPayload(slot phase0.Slot, blockHash phase0.Hash32, blockValueWei *big.Int) *payload_builder.Payload {
	return &payload_builder.Payload{
		Attributes: &beacon.PayloadAttributesEvent{ProposalSlot: slot},
		ExecutionPayload: &eth2all.ExecutionPayload{
			Version:       version.DataVersionGloas,
			BaseFeePerGas: uint256.NewInt(7),
			BlockHash:     blockHash,
		},
		BlockHash:  blockHash,
		BlockValue: blockValueWei,
		ReadyAt:    time.Now(),
	}
}
