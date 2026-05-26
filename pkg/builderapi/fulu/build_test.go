package fulu

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

func TestBuildSignedBuilderBid_NilEvent(t *testing.T) {
	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)
	pk := blsSigner.PublicKey()

	var genesisForkVersion phase0.Version // zero version
	var genesisValidatorsRoot phase0.Root // zero root

	bid, err := BuildSignedBuilderBid(nil, pk, blsSigner, 0, genesisForkVersion, genesisValidatorsRoot)
	require.NoError(t, err)
	assert.Nil(t, bid)
}

func TestBuildSignedBuilderBid_NoSubsidy(t *testing.T) {
	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)
	pk := blsSigner.PublicKey()

	blockValue := new(big.Int).SetUint64(1_000_000_000_000_000) // 0.001 ETH in wei
	event := minimalPayloadReadyEvent(t, blockValue)

	var genesisForkVersion phase0.Version // zero version
	var genesisValidatorsRoot phase0.Root // zero root

	bid, err := BuildSignedBuilderBid(event, pk, blsSigner, 0, genesisForkVersion, genesisValidatorsRoot)
	require.NoError(t, err)
	require.NotNil(t, bid)
	require.NotNil(t, bid.Message)
	require.NotNil(t, bid.Message.Value)
	assert.True(t, bid.Message.Value.IsUint64())
	assert.Equal(t, blockValue.Uint64(), bid.Message.Value.Uint64(), "bid value should equal block value when subsidy is 0")
}

func TestBuildSignedBuilderBid_SubsidyAdded(t *testing.T) {
	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)
	pk := blsSigner.PublicKey()

	blockValue := new(big.Int).SetUint64(500_000_000_000_000) // 0.0005 ETH in wei
	subsidy := uint64(1_000_000)                              // 0.001 ETH subsidy in gwei
	event := minimalPayloadReadyEvent(t, blockValue)

	var genesisForkVersion phase0.Version // zero version
	var genesisValidatorsRoot phase0.Root // zero root

	bid, err := BuildSignedBuilderBid(event, pk, blsSigner, subsidy, genesisForkVersion, genesisValidatorsRoot)
	require.NoError(t, err)
	require.NotNil(t, bid)
	require.NotNil(t, bid.Message)
	require.NotNil(t, bid.Message.Value)
	assert.True(t, bid.Message.Value.IsUint64())
	expected := new(big.Int).Add(blockValue, new(big.Int).SetUint64(subsidy*1_000_000_000))
	assert.Equal(t, expected.Uint64(), bid.Message.Value.Uint64(),
		"bid value should be block_value_wei + subsidy_gwei_converted_to_wei")
}

func minimalPayloadReadyEvent(t *testing.T, blockValue *big.Int) *builder.PayloadReadyEvent {
	t.Helper()
	// ExecutionPayloadHeaderFromEngine needs ParentHash, FeeRecipient, StateRoot, ReceiptsRoot,
	// LogsBloom (256 bytes), PrevRandao (32 bytes), Transactions (can be nil for root), Withdrawals (can be nil).
	payload := &engine.ExecutionPayload{
		ParentHash:    common.Hash{1, 2, 3},
		FeeRecipient:  common.Address{},
		StateRoot:     common.Hash{},
		ReceiptsRoot:  common.Hash{},
		BlockNumber:   1,
		GasLimit:      30_000_000,
		GasUsed:       0,
		Timestamp:     1,
		BlockHash:     common.Hash{4, 5, 6},
		Transactions:  nil,
		Withdrawals:   nil,
		BlobGasUsed:   0,
		ExcessBlobGas: 0,
	}
	// LogsBloom and PrevRandao must have correct length for SSZ/header.
	for i := range payload.LogsBloom {
		payload.LogsBloom[i] = 0
	}
	for i := range payload.PrevRandao {
		payload.PrevRandao[i] = 0
	}
	return &builder.PayloadReadyEvent{
		Slot:       1,
		BlockHash:  phase0.Hash32(payload.BlockHash),
		Payload:    payload,
		BlockValue: blockValue,
	}
}
