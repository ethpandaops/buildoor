package payload_builder

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

func TestNewPayloadBuilder(t *testing.T) {
	b := NewPayloadBuilder(
		nil,
		nil,
		nil,
		nil,
		nil,
		common.HexToAddress("0x1111"),
		&config.Config{PayloadBuildTime: 100},
		logrus.New(),
		nil,
	)
	assert.NotNil(t, b)
	assert.Equal(t, common.HexToAddress("0x1111"), b.feeRecipient)
}

func TestNewPayloadBuilder_AcceptsNilClients(t *testing.T) {
	// Constructor allows nil clients (used in tests); actual build will fail if they're nil.
	_ = NewPayloadBuilder(nil, nil, nil, nil, nil, common.Address{}, &config.Config{}, logrus.New(), nil)
}

// staticResolver answers every slot with the same proposer settings.
type staticResolver struct {
	settings ProposerSettings
}

func (r staticResolver) ResolveProposerSettings(phase0.Slot, phase0.ValidatorIndex) (ProposerSettings, bool) {
	return r.settings, true
}

func TestBuildFeeRecipients(t *testing.T) {
	wallet := common.HexToAddress("0xb0b0")
	proposer := common.HexToAddress("0xa1a1")
	suggested := common.HexToAddress("0xc2c2")

	attrs := &beacon.PayloadAttributesEvent{SuggestedFeeRecipient: suggested, TargetGasLimit: 60_000_000}
	resolved := []ProposerSettingsResolver{staticResolver{ProposerSettings{FeeRecipient: proposer}}}

	tests := []struct {
		name         string
		fork         version.DataVersion
		wallet       common.Address
		resolvers    []ProposerSettingsResolver
		wantProposer common.Address
		wantCoinbase common.Address
	}{
		{
			name:         "gloas builds for the wallet, the bid pays the proposer",
			fork:         version.DataVersionGloas,
			wallet:       wallet,
			resolvers:    resolved,
			wantProposer: proposer,
			wantCoinbase: wallet,
		},
		{
			name:         "gloas without preferences pays the suggested fee recipient",
			fork:         version.DataVersionGloas,
			wallet:       wallet,
			wantProposer: suggested,
			wantCoinbase: wallet,
		},
		{
			name:         "gloas without a wallet builds for the proposer",
			fork:         version.DataVersionGloas,
			resolvers:    resolved,
			wantProposer: proposer,
			wantCoinbase: proposer,
		},
		{
			name:         "pre-gloas builds for the registered fee recipient",
			fork:         version.DataVersionFulu,
			wallet:       wallet,
			resolvers:    resolved,
			wantProposer: proposer,
			wantCoinbase: proposer,
		},
		{
			name:         "pre-gloas without a registration uses the suggested fee recipient",
			fork:         version.DataVersionFulu,
			wallet:       wallet,
			wantProposer: suggested,
			wantCoinbase: suggested,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewPayloadBuilder(nil, nil, nil, nil, nil, tt.wallet, &config.Config{}, logrus.New(), tt.resolvers)

			proposerFeeRecipient, targetGasLimit := b.resolveProposerSettings(attrs, tt.fork)
			assert.Equal(t, tt.wantProposer, proposerFeeRecipient)
			assert.Equal(t, tt.wantCoinbase, b.payloadCoinbase(tt.fork, proposerFeeRecipient))

			if tt.fork >= version.DataVersionGloas {
				assert.Equal(t, uint64(60_000_000), targetGasLimit, "the attributes fill the unannounced target")
			} else {
				assert.Zero(t, targetGasLimit)
			}
		})
	}
}
