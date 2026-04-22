package fulu

import (
	"encoding/binary"
	"testing"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
)

func TestParseExecutionRequests_Empty(t *testing.T) {
	result, err := ParseExecutionRequests(nil)
	require.NoError(t, err)
	assert.Nil(t, result.Deposits)
	assert.Nil(t, result.Withdrawals)
	assert.Nil(t, result.Consolidations)
}

func TestParseExecutionRequests_SingleDeposit(t *testing.T) {
	// Build a 192-byte deposit: pubkey(48) + creds(32) + amount(8 LE) + sig(96) + index(8 LE)
	deposit := make([]byte, depositRequestSize)
	// Fill pubkey with 0x01
	for i := 0; i < 48; i++ {
		deposit[i] = 0x01
	}
	// Fill withdrawal credentials with 0x02
	for i := 48; i < 80; i++ {
		deposit[i] = 0x02
	}
	// Amount = 32000000000 (32 ETH in Gwei)
	binary.LittleEndian.PutUint64(deposit[80:88], 32_000_000_000)
	// Fill signature with 0x03
	for i := 88; i < 184; i++ {
		deposit[i] = 0x03
	}
	// Index = 42
	binary.LittleEndian.PutUint64(deposit[184:192], 42)

	entry := append([]byte{depositRequestType}, deposit...)

	result, err := ParseExecutionRequests(engine.ExecutionRequests{hexutil.Bytes(entry)})
	require.NoError(t, err)
	require.Len(t, result.Deposits, 1)

	d := result.Deposits[0]

	var expectedPubkey phase0.BLSPubKey
	for i := range expectedPubkey {
		expectedPubkey[i] = 0x01
	}
	assert.Equal(t, expectedPubkey, d.Pubkey)

	expectedCreds := make([]byte, 32)
	for i := range expectedCreds {
		expectedCreds[i] = 0x02
	}
	assert.Equal(t, expectedCreds, d.WithdrawalCredentials)
	assert.Equal(t, phase0.Gwei(32_000_000_000), d.Amount)

	var expectedSig phase0.BLSSignature
	for i := range expectedSig {
		expectedSig[i] = 0x03
	}
	assert.Equal(t, expectedSig, d.Signature)
	assert.Equal(t, uint64(42), d.Index)
}

func TestParseExecutionRequests_MultipleDeposits(t *testing.T) {
	d1 := make([]byte, depositRequestSize)
	d1[0] = 0xAA
	binary.LittleEndian.PutUint64(d1[184:192], 1)

	d2 := make([]byte, depositRequestSize)
	d2[0] = 0xBB
	binary.LittleEndian.PutUint64(d2[184:192], 2)

	data := append(d1, d2...)
	entry := append([]byte{depositRequestType}, data...)

	result, err := ParseExecutionRequests(engine.ExecutionRequests{hexutil.Bytes(entry)})
	require.NoError(t, err)
	require.Len(t, result.Deposits, 2)
	assert.Equal(t, uint64(1), result.Deposits[0].Index)
	assert.Equal(t, uint64(2), result.Deposits[1].Index)
}

func TestParseExecutionRequests_SingleWithdrawal(t *testing.T) {
	// 76 bytes: sourceAddress(20) + validatorPubkey(48) + amount(8 LE)
	w := make([]byte, withdrawalRequestSize)
	for i := 0; i < 20; i++ {
		w[i] = 0xAA
	}
	for i := 20; i < 68; i++ {
		w[i] = 0xBB
	}
	binary.LittleEndian.PutUint64(w[68:76], 1_000_000_000)

	entry := append([]byte{withdrawalRequestType}, w...)

	result, err := ParseExecutionRequests(engine.ExecutionRequests{hexutil.Bytes(entry)})
	require.NoError(t, err)
	require.Len(t, result.Withdrawals, 1)

	wr := result.Withdrawals[0]

	var expectedAddr bellatrix.ExecutionAddress
	for i := range expectedAddr {
		expectedAddr[i] = 0xAA
	}
	assert.Equal(t, expectedAddr, wr.SourceAddress)

	var expectedPubkey phase0.BLSPubKey
	for i := range expectedPubkey {
		expectedPubkey[i] = 0xBB
	}
	assert.Equal(t, expectedPubkey, wr.ValidatorPubkey)
	assert.Equal(t, phase0.Gwei(1_000_000_000), wr.Amount)
}

func TestParseExecutionRequests_SingleConsolidation(t *testing.T) {
	// 116 bytes: sourceAddress(20) + sourcePubkey(48) + targetPubkey(48)
	c := make([]byte, consolidationRequestSize)
	for i := 0; i < 20; i++ {
		c[i] = 0x11
	}
	for i := 20; i < 68; i++ {
		c[i] = 0x22
	}
	for i := 68; i < 116; i++ {
		c[i] = 0x33
	}

	entry := append([]byte{consolidationRequestType}, c...)

	result, err := ParseExecutionRequests(engine.ExecutionRequests{hexutil.Bytes(entry)})
	require.NoError(t, err)
	require.Len(t, result.Consolidations, 1)

	cr := result.Consolidations[0]

	var expectedAddr bellatrix.ExecutionAddress
	for i := range expectedAddr {
		expectedAddr[i] = 0x11
	}
	assert.Equal(t, expectedAddr, cr.SourceAddress)

	var expectedSource phase0.BLSPubKey
	for i := range expectedSource {
		expectedSource[i] = 0x22
	}
	assert.Equal(t, expectedSource, cr.SourcePubkey)

	var expectedTarget phase0.BLSPubKey
	for i := range expectedTarget {
		expectedTarget[i] = 0x33
	}
	assert.Equal(t, expectedTarget, cr.TargetPubkey)
}

func TestParseExecutionRequests_AllThreeTypes(t *testing.T) {
	deposit := make([]byte, depositRequestSize)
	binary.LittleEndian.PutUint64(deposit[184:192], 99)

	withdrawal := make([]byte, withdrawalRequestSize)
	binary.LittleEndian.PutUint64(withdrawal[68:76], 500)

	consolidation := make([]byte, consolidationRequestSize)
	consolidation[0] = 0xFF

	raw := engine.ExecutionRequests{
		hexutil.Bytes(append([]byte{depositRequestType}, deposit...)),
		hexutil.Bytes(append([]byte{withdrawalRequestType}, withdrawal...)),
		hexutil.Bytes(append([]byte{consolidationRequestType}, consolidation...)),
	}

	result, err := ParseExecutionRequests(raw)
	require.NoError(t, err)
	require.Len(t, result.Deposits, 1)
	require.Len(t, result.Withdrawals, 1)
	require.Len(t, result.Consolidations, 1)
	assert.Equal(t, uint64(99), result.Deposits[0].Index)
	assert.Equal(t, phase0.Gwei(500), result.Withdrawals[0].Amount)
}

func TestParseExecutionRequests_TypePrefixOnly(t *testing.T) {
	// Entry with only type prefix and no data should be skipped
	raw := engine.ExecutionRequests{
		hexutil.Bytes{depositRequestType},
		hexutil.Bytes{withdrawalRequestType},
		hexutil.Bytes{consolidationRequestType},
	}

	result, err := ParseExecutionRequests(raw)
	require.NoError(t, err)
	assert.Nil(t, result.Deposits)
	assert.Nil(t, result.Withdrawals)
	assert.Nil(t, result.Consolidations)
}

func TestParseExecutionRequests_EmptyEntry(t *testing.T) {
	raw := engine.ExecutionRequests{hexutil.Bytes{}}
	_, err := ParseExecutionRequests(raw)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty entry")
}

func TestParseExecutionRequests_UnknownType(t *testing.T) {
	raw := engine.ExecutionRequests{hexutil.Bytes{0xFF, 0x01}}
	_, err := ParseExecutionRequests(raw)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown type 0xff")
}

func TestParseExecutionRequests_InvalidDepositLength(t *testing.T) {
	// 100 bytes is not divisible by 192
	data := make([]byte, 100)
	entry := append([]byte{depositRequestType}, data...)
	raw := engine.ExecutionRequests{hexutil.Bytes(entry)}
	_, err := ParseExecutionRequests(raw)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not divisible by")
}

func TestParseExecutionRequests_InvalidWithdrawalLength(t *testing.T) {
	data := make([]byte, 50) // not divisible by 76
	entry := append([]byte{withdrawalRequestType}, data...)
	raw := engine.ExecutionRequests{hexutil.Bytes(entry)}
	_, err := ParseExecutionRequests(raw)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not divisible by")
}

func TestParseExecutionRequests_InvalidConsolidationLength(t *testing.T) {
	data := make([]byte, 50) // not divisible by 116
	entry := append([]byte{consolidationRequestType}, data...)
	raw := engine.ExecutionRequests{hexutil.Bytes(entry)}
	_, err := ParseExecutionRequests(raw)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not divisible by")
}
