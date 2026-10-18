package txpool

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/config"
)

// signSidecarBlobTx signs a one-blob transaction carrying a version 0
// sidecar; a wrongHash commits the transaction to another blob.
func signSidecarBlobTx(t *testing.T, acc account, nonce uint64, wrongHash bool) []byte {
	t.Helper()

	var blob kzg4844.Blob
	blob[31] = byte(nonce + 1)

	commitment, err := kzg4844.BlobToCommitment(&blob)
	require.NoError(t, err)

	proof, err := kzg4844.ComputeBlobProof(&blob, commitment)
	require.NoError(t, err)

	hash := common.Hash(kzg4844.CalcBlobHashV1(sha256.New(), &commitment))
	if wrongHash {
		hash[31] ^= 0xff
	}

	tx, err := types.SignNewTx(acc.key, types.LatestSignerForChainID(testChainID), &types.BlobTx{
		ChainID:    uint256.MustFromBig(testChainID),
		Nonce:      nonce,
		GasTipCap:  uint256.NewInt(1_000_000_000),
		GasFeeCap:  uint256.NewInt(2_000_000_000),
		Gas:        21000,
		To:         common.Address{0x42},
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(10),
		BlobHashes: []common.Hash{hash},
		Sidecar: types.NewBlobTxSidecar(types.BlobSidecarVersion0,
			[]kzg4844.Blob{blob}, []kzg4844.Commitment{commitment}, []kzg4844.Proof{proof}),
	})
	require.NoError(t, err)

	raw, err := tx.MarshalBinary()
	require.NoError(t, err)

	return raw
}

func TestAddNormalizesBlobSidecars(t *testing.T) {
	tests := []struct {
		name        string
		fork        version.DataVersion
		wantVersion byte
		wantProofs  int
	}{
		{name: "cell proofs computed from Osaka on", fork: version.DataVersionFulu,
			wantVersion: types.BlobSidecarVersion1, wantProofs: kzg4844.CellProofsPerBlob},
		{name: "blob proofs kept before Osaka", fork: version.DataVersionElectra,
			wantVersion: types.BlobSidecarVersion0, wantProofs: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool, _, chainSvc := newTestPool(t, newFakeEL())
			chainSvc.fork = tt.fork

			hash, err := pool.Add(signSidecarBlobTx(t, newAccount(t), 0, false))
			require.NoError(t, err)

			sidecar := pool.Get(hash).Tx.BlobTxSidecar()
			require.NotNil(t, sidecar)
			require.Equal(t, tt.wantVersion, sidecar.Version)
			require.Len(t, sidecar.Proofs, tt.wantProofs)
		})
	}
}

func TestAddRejectsMismatchedSidecar(t *testing.T) {
	pool, _, _ := newTestPool(t, newFakeEL())

	_, err := pool.Add(signSidecarBlobTx(t, newAccount(t), 0, true))
	require.ErrorIs(t, err, ErrBlobSidecar)
}

func TestSelectSkipsSidecarOfTheOtherFork(t *testing.T) {
	el := newFakeEL()
	pool, _, chainSvc := newTestPool(t, el)
	chainSvc.fork = version.DataVersionElectra // admitted before the transition: version 0

	acc := newAccount(t)
	fund(el, acc, 0, 100)

	_, err := pool.Add(signSidecarBlobTx(t, acc, 0, false))
	require.NoError(t, err)

	params := func(cellProofs bool) *SelectParams {
		return &SelectParams{
			ParentHash:     common.Hash{0xaa},
			GasLimit:       30_000_000,
			MaxBlobs:       6,
			IncludeBlobTxs: true,
			BlobEncoding:   config.BlobEncodingNetwork,
			CellProofs:     cellProofs,
		}
	}

	sel, err := pool.Select(context.Background(), params(false))
	require.NoError(t, err)
	require.Len(t, sel.Selected, 1)

	sel, err = pool.Select(context.Background(), params(true))
	require.NoError(t, err)
	require.Empty(t, sel.Selected)
	require.Equal(t, 1, sel.Skipped[SkipBlobSidecarVersion])
}
