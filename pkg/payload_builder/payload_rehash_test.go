package payload_builder

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/trie"
	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth-engine-client/spec/paris"
	"github.com/ethpandaops/go-eth-engine-client/spec/prague"
	"github.com/ethpandaops/go-eth-engine-client/spec/shanghai"
	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"
	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rehashTestPayload(t *testing.T) (*engineall.ExecutionPayload, []prague.ExecutionRequest, common.Hash) {
	t.Helper()

	p := &engineall.ExecutionPayload{
		Version:         enginev.DataVersionAmsterdam,
		ParentHash:      paris.Hash32{0x01},
		FeeRecipient:    paris.Address{0x02},
		StateRoot:       paris.Hash32{0x03},
		ReceiptsRoot:    paris.Hash32{0x04},
		PrevRandao:      paris.Hash32{0x05},
		BlockNumber:     100,
		GasLimit:        60_000_000,
		GasUsed:         21_000,
		Timestamp:       1_700_000_000,
		ExtraData:       []byte("buildoor"),
		BaseFeePerGas:   uint256.NewInt(7),
		BlobGasUsed:     131072,
		ExcessBlobGas:   0,
		BlockAccessList: []byte{0xc0},
		SlotNumber:      42,
		Withdrawals: []*shanghai.Withdrawal{
			{Index: 1, ValidatorIndex: 2, Address: paris.Address{0x06}, Amount: 32},
		},
	}
	requests := []prague.ExecutionRequest{append([]byte{0x00}, make([]byte, 192)...)}
	parentRoot := common.Hash{0x07}

	header, err := buildHeaderFromPayload(p, parentRoot, requests)
	require.NoError(t, err)
	p.BlockHash = paris.Hash32(header.Hash())

	return p, requests, parentRoot
}

func TestRehashBeaconPayload_Unmodified(t *testing.T) {
	p, requests, parentRoot := rehashTestPayload(t)

	got, err := RehashBeaconPayload(beaconPayloadFromEngine(p, version.DataVersionGloas),
		p.Version, requests, parentRoot)
	require.NoError(t, err)
	assert.Equal(t, common.Hash(p.BlockHash), got)
}

func TestRehashBeaconPayload_Modified(t *testing.T) {
	p, requests, parentRoot := rehashTestPayload(t)

	beaconPayload := beaconPayloadFromEngine(p, version.DataVersionGloas)
	beaconPayload.StateRoot = phase0.Root{0xff}
	beaconPayload.BlockAccessList = []byte{0xc1, 0x80}

	got, err := RehashBeaconPayload(beaconPayload, p.Version, requests, parentRoot)
	require.NoError(t, err)

	p.StateRoot = paris.Hash32{0xff}
	p.BlockAccessList = []byte{0xc1, 0x80}
	want, err := buildHeaderFromPayload(p, parentRoot, requests)
	require.NoError(t, err)

	assert.Equal(t, want.Hash(), got)
	assert.NotEqual(t, common.Hash(p.BlockHash), got)
}

func TestRawTransactionsRootMatchesTypedRoot(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	signer := types.LatestSignerForChainID(big.NewInt(1))
	to := common.Address{0x42}

	legacy, err := types.SignNewTx(key, signer, &types.LegacyTx{
		Nonce: 0, GasPrice: big.NewInt(1), Gas: 21000, To: &to, Value: big.NewInt(1),
	})
	require.NoError(t, err)

	dynamic, err := types.SignNewTx(key, signer, &types.DynamicFeeTx{
		ChainID: big.NewInt(1), Nonce: 1, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2),
		Gas: 21000, To: &to, Value: big.NewInt(1),
	})
	require.NoError(t, err)

	typed := types.Transactions{legacy, dynamic}
	raw := make(rawTransactions, len(typed))

	for i, tx := range typed {
		encoded, err := tx.MarshalBinary()
		require.NoError(t, err)

		raw[i] = encoded
	}

	assert.Equal(t,
		types.DeriveSha(typed, trie.NewStackTrie(nil)),
		types.DeriveSha(raw, trie.NewStackTrie(nil)),
		"the raw-bytes root must equal the decoded transactions' root")
}

func TestRehashBeaconPayload_UndecodableTransaction(t *testing.T) {
	p, requests, parentRoot := rehashTestPayload(t)

	// A transform inserting garbage still yields a payload with a consistent
	// hash, so the EL rejects the transaction rather than the block hash.
	beacon := beaconPayloadFromEngine(p, version.DataVersionGloas)
	beacon.Transactions = append(beacon.Transactions, bellatrix.Transaction{0x02, 0xde, 0xad})

	got, err := RehashBeaconPayload(beacon, p.Version, requests, parentRoot)
	require.NoError(t, err)
	assert.NotEqual(t, common.Hash(p.BlockHash), got)
}
