package builder

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"

	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
)

func TestNewPayloadBuilder_UseProposerFeeRecipient(t *testing.T) {
	// NewPayloadBuilder with useProposerFeeRecipient false: builder uses own fee recipient.
	b := NewPayloadBuilder(
		nil,
		nil,
		common.HexToAddress("0x1111"),
		100,
		logrus.New(),
		false,
		nil,
	)
	assert.NotNil(t, b)
	assert.False(t, b.useProposerFeeRecipient)

	// NewPayloadBuilder with useProposerFeeRecipient true: BuildPayloadFromAttributes will use attrs.SuggestedFeeRecipient.
	b2 := NewPayloadBuilder(
		nil,
		nil,
		common.HexToAddress("0x2222"),
		100,
		logrus.New(),
		true,
		nil,
	)
	assert.NotNil(t, b2)
	assert.True(t, b2.useProposerFeeRecipient)
}

func TestNewPayloadBuilder_AcceptsNilClients(t *testing.T) {
	// Constructor allows nil clients (used in tests); actual build will fail if they're nil.
	_ = NewPayloadBuilder(nil, (*engine.Client)(nil), common.Address{}, 0, logrus.New(), false, nil)
}
