package lifecycle

import (
	"context"
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuilderSystemContractAddresses(t *testing.T) {
	assert.Equal(t,
		common.HexToAddress("0x0000bff46984e3725691fa540a8c7589300d8282"),
		BuilderDepositContractAddress,
	)
	assert.Equal(t,
		common.HexToAddress("0x000064d678505ad48f8ccb093bc65613800e8282"),
		BuilderExitContractAddress,
	)
}

func TestFakeExponential(t *testing.T) {
	// Reference values from the EIP-7002 fee mechanism: factor=1, denominator=17,
	// approximating floor(e^(excess/17)).
	tests := []struct {
		excess int64
		want   string
	}{
		{0, "1"},
		{1, "1"},
		{17, "2"}, // ~e
		{100, "357"},
		{170, "22019"}, // ~e^10
	}

	for _, tt := range tests {
		got := fakeExponential(big.NewInt(minRequestFee), big.NewInt(tt.excess), big.NewInt(feeUpdateFraction))
		assert.Equal(t, tt.want, got.String(), "excess=%d", tt.excess)
	}
}

func TestBuildBuilderDepositCalldata(t *testing.T) {
	pubkey := make([]byte, 48)
	for i := range pubkey {
		pubkey[i] = 0xAA
	}

	var wc [32]byte
	wc[0] = 0xB0

	sig := make([]byte, 96)
	for i := range sig {
		sig[i] = 0xCC
	}

	const amountGwei = uint64(32_000_000_000)

	data, err := BuildBuilderDepositCalldata(pubkey, wc[:], amountGwei, sig)
	require.NoError(t, err)
	require.Len(t, data, 184, "deposit calldata must be 184 bytes")

	assert.Equal(t, pubkey, data[0:48], "pubkey at [0:48]")
	assert.Equal(t, wc[:], data[48:80], "withdrawal credentials at [48:80]")
	assert.Equal(t, amountGwei, binary.BigEndian.Uint64(data[80:88]), "amount (big-endian gwei) at [80:88]")
	assert.Equal(t, sig, data[88:184], "signature at [88:184]")
}

func TestBuildBuilderDepositCalldataRejectsBadLengths(t *testing.T) {
	var wc [32]byte
	sig := make([]byte, 96)

	_, err := BuildBuilderDepositCalldata(make([]byte, 47), wc[:], 1, sig)
	assert.Error(t, err, "short pubkey rejected")

	_, err = BuildBuilderDepositCalldata(make([]byte, 48), make([]byte, 31), 1, sig)
	assert.Error(t, err, "short withdrawal credentials rejected")

	_, err = BuildBuilderDepositCalldata(make([]byte, 48), wc[:], 1, make([]byte, 95))
	assert.Error(t, err, "short signature rejected")
}

func TestBuildBuilderExitCalldata(t *testing.T) {
	pubkey := make([]byte, 48)
	for i := range pubkey {
		pubkey[i] = 0xBB
	}

	data, err := BuildBuilderExitCalldata(pubkey)
	require.NoError(t, err)
	require.Len(t, data, 48, "exit calldata must be 48 bytes")
	assert.Equal(t, pubkey, data)

	_, err = BuildBuilderExitCalldata(make([]byte, 47))
	assert.Error(t, err, "short pubkey rejected")
}

func TestWithdrawalCredentials(t *testing.T) {
	addr := common.HexToAddress("0x1122334455667788990011223344556677889900")

	tests := []struct {
		name       string
		creds      [32]byte
		wantPrefix byte
	}{
		{"builder uses 0xB0 prefix", BuilderWithdrawalCredentials(addr), 0xB0},
		{"validator uses 0xB0 prefix", ValidatorWithdrawalCredentials(addr), 0xB0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantPrefix, tt.creds[0], "withdrawal prefix")

			for i := 1; i < 12; i++ {
				assert.Equal(t, byte(0x00), tt.creds[i], "zero padding at byte %d", i)
			}

			assert.Equal(t, addr.Bytes(), tt.creds[12:], "address in last 20 bytes")
		})
	}
}

// fakeStorageReader returns a fixed 32-byte slot value.
type fakeStorageReader struct {
	value []byte
	err   error
}

func (f fakeStorageReader) GetStorageAt(_ context.Context, _ common.Address, _ common.Hash) ([]byte, error) {
	return f.value, f.err
}

func slotBytes(v *big.Int) []byte {
	b := make([]byte, 32)
	v.FillBytes(b)

	return b
}

func TestReadQueueFee(t *testing.T) {
	ctx := context.Background()

	// Pre-fork excess inhibitor -> not active.
	fee, active, err := ReadQueueFee(ctx, fakeStorageReader{value: slotBytes(excessInhibitor)}, BuilderDepositContractAddress)
	require.NoError(t, err)
	assert.False(t, active, "inhibitor means contract not active")
	assert.Nil(t, fee)

	// Active contract with excess=0 -> fee priced queueFeeHeadroom slots ahead.
	fee, active, err = ReadQueueFee(ctx, fakeStorageReader{value: slotBytes(big.NewInt(0))}, BuilderDepositContractAddress)
	require.NoError(t, err)
	assert.True(t, active)

	want := fakeExponential(big.NewInt(minRequestFee), big.NewInt(queueFeeHeadroom), big.NewInt(feeUpdateFraction))
	assert.Equal(t, want.String(), fee.String(), "fee includes headroom over excess")
}
