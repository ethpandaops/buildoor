package txpool

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/config"
)

// fakeSidecars completes the transactions in available and records what it
// was asked for.
type fakeSidecars struct {
	available map[common.Hash]bool
	asked     []common.Hash
}

func (f *fakeSidecars) BlobSidecars(_ context.Context, txs []*types.Transaction) (map[common.Hash]*types.BlobTxSidecar, error) {
	out := make(map[common.Hash]*types.BlobTxSidecar, len(txs))

	for _, tx := range txs {
		f.asked = append(f.asked, tx.Hash())

		if f.available[tx.Hash()] {
			blobs := len(tx.BlobHashes())
			out[tx.Hash()] = types.NewBlobTxSidecar(types.BlobSidecarVersion1,
				make([]kzg4844.Blob, blobs), make([]kzg4844.Commitment, blobs), make([]kzg4844.Proof, blobs*kzg4844.CellProofsPerBlob))
		}
	}

	return out, nil
}

// signBlobTx signs a blob transaction as txpool_content lists it: without
// its sidecar.
func signBlobTx(t *testing.T, acc account, nonce uint64, tip int64, blobs int) *types.Transaction {
	t.Helper()

	hashes := make([]common.Hash, blobs)
	for i := range hashes {
		hashes[i] = common.Hash{0x01, byte(nonce), byte(i), acc.addr[0], acc.addr[1]}
	}

	tx, err := types.SignNewTx(acc.key, types.LatestSignerForChainID(testChainID), &types.BlobTx{
		ChainID:    uint256.MustFromBig(testChainID),
		Nonce:      nonce,
		GasTipCap:  uint256.NewInt(uint64(tip)),
		GasFeeCap:  uint256.NewInt(2_000_000_000 + uint64(tip)),
		Gas:        21000,
		To:         common.Address{0x42},
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(10),
		BlobHashes: hashes,
	})
	require.NoError(t, err)

	return tx
}

func TestSelectFillCompletesBlobTransactions(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	completed := newAccount(t) // its blobs are in the EL blob pool
	missing := newAccount(t)   // its blobs are not
	large := newAccount(t)     // exceeds the blob budget
	plain := newAccount(t)

	for _, acc := range []account{completed, missing, large, plain} {
		fund(el, acc, 0, 100)
	}

	completedTx := signBlobTx(t, completed, 0, 5_000_000_000, 1)
	missingTx := signBlobTx(t, missing, 0, 4_000_000_000, 1)
	missingNext, _ := signTx(t, missing, txOpts{nonce: 1, tip: 4_000_000_000, feeCap: 6_000_000_000})
	largeTx := signBlobTx(t, large, 0, 3_000_000_000, 2)
	plainTx, _ := signTx(t, plain, txOpts{nonce: 0})

	source := &fakeSidecars{available: map[common.Hash]bool{completedTx.Hash(): true, largeTx.Hash(): true}}

	sel, err := pool.Select(context.Background(), &SelectParams{
		ParentHash:     common.Hash{0xaa},
		GasLimit:       30_000_000,
		MaxBlobs:       2,
		IncludeBlobTxs: true,
		BlobEncoding:   config.BlobEncodingNetwork,
		Fill:           []*types.Transaction{completedTx, missingTx, missingNext, largeTx, plainTx},
		FillSidecars:   source,
	})
	require.NoError(t, err)

	require.Equal(t, []common.Hash{completedTx.Hash(), missingTx.Hash()}, source.asked,
		"only the selected blob transactions are completed")

	require.Len(t, sel.Fill, 2)
	require.Equal(t, completedTx.Hash(), sel.Fill[0].Hash)
	require.NotNil(t, sel.Fill[0].Tx.BlobTxSidecar(), "the sidecar is attached")
	require.Equal(t, plainTx.Hash(), sel.Fill[1].Hash)

	require.Equal(t, 1, sel.FillBlobs)
	require.Equal(t, uint64(2*21000), sel.FillGasSum)
	require.Equal(t, 1, sel.FillSkipped[SkipBlobFull])
	require.Equal(t, 1, sel.FillSkipped[SkipBlobUnavailable])
	require.Equal(t, 1, sel.FillSkipped[SkipNonceGap], "the sender's next transaction follows the dropped one")

	// The network form carries the sidecar.
	var decoded types.Transaction
	require.NoError(t, decoded.UnmarshalBinary(sel.Txs[0]))
	require.NotNil(t, decoded.BlobTxSidecar())
}

func TestSelectFillBlobTransactionsBySource(t *testing.T) {
	el := newFakeEL()
	pool, _, _ := newTestPool(t, el)

	acc := newAccount(t)
	fund(el, acc, 0, 100)
	blobTx := signBlobTx(t, acc, 0, 5_000_000_000, 1)

	params := func(encoding string, source SidecarSource) *SelectParams {
		return &SelectParams{
			ParentHash:     common.Hash{0xaa},
			GasLimit:       30_000_000,
			MaxBlobs:       6,
			IncludeBlobTxs: true,
			BlobEncoding:   encoding,
			Fill:           []*types.Transaction{blobTx},
			FillSidecars:   source,
		}
	}

	// The network encoding cannot carry a blob transaction without a source.
	sel, err := pool.Select(context.Background(), params(config.BlobEncodingNetwork, nil))
	require.NoError(t, err)
	require.Empty(t, sel.Fill)
	require.Equal(t, 1, sel.FillSkipped[SkipBlobUnsupported])

	// The canonical encoding strips the sidecar anyway: no source needed.
	sel, err = pool.Select(context.Background(), params(config.BlobEncodingCanonical, nil))
	require.NoError(t, err)
	require.Len(t, sel.Fill, 1)
	require.Equal(t, 1, sel.FillBlobs)
}
