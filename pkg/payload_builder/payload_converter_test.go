package payload_builder

import (
	"testing"

	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBeaconPayloadFromEngine_BaseFeeRepresentations verifies both base-fee
// forms of the agnostic payload are filled: the uint256 (Deneb+ wire) and the
// little-endian bytes (Bellatrix/Capella wire). Pre-Deneb views serialize
// BaseFeePerGasLE, so leaving it zero would put a zero base fee on the wire.
func TestBeaconPayloadFromEngine_BaseFeeRepresentations(t *testing.T) {
	baseFee := uint256.NewInt(0).SetBytes([]byte{0x01, 0x02, 0x03})

	out := beaconPayloadFromEngine(&engineall.ExecutionPayload{
		Version:       enginev.DataVersionShanghai,
		BaseFeePerGas: baseFee,
	}, version.DataVersionCapella)

	require.NotNil(t, out.BaseFeePerGas)
	assert.True(t, baseFee.Eq(out.BaseFeePerGas))

	// LE form: big-endian 0x010203 → bytes 0x03,0x02,0x01 at the front.
	assert.Equal(t, byte(0x03), out.BaseFeePerGasLE[0])
	assert.Equal(t, byte(0x02), out.BaseFeePerGasLE[1])
	assert.Equal(t, byte(0x01), out.BaseFeePerGasLE[2])
	for i := 3; i < 32; i++ {
		assert.Equal(t, byte(0), out.BaseFeePerGasLE[i])
	}
}
