package builder

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"

	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
)

func TestNewPayloadBuilder(t *testing.T) {
	b := NewPayloadBuilder(
		nil,
		nil,
		common.HexToAddress("0x1111"),
		100,
		logrus.New(),
		nil,
		nil,
		nil,
		nil,
	)
	assert.NotNil(t, b)
	assert.Equal(t, common.HexToAddress("0x1111"), b.feeRecipient)
}

func TestNewPayloadBuilder_AcceptsNilClients(t *testing.T) {
	// Constructor allows nil clients (used in tests); actual build will fail if they're nil.
	_ = NewPayloadBuilder(nil, (*engine.Client)(nil), common.Address{}, 0, logrus.New(), nil, nil, nil, nil)
}
