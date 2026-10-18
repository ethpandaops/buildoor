package payload_builder

import (
	"context"
	"crypto/sha256"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth-engine-client/spec/cancun"
	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"
)

// blobPoolEngine serves engine_getBlobs from a map, all-or-nothing like V2.
// The per-transaction requests run concurrently, hence the atomic counter.
type blobPoolEngine struct {
	EngineClient

	pool  map[common.Hash]*engineall.BlobAndProof
	calls atomic.Int32
}

func (e *blobPoolEngine) GetBlobsAgnostic(
	_ context.Context,
	request *engineall.GetBlobsRequest,
) (*engineall.GetBlobsResponse, error) {
	e.calls.Add(1)

	resp := &engineall.GetBlobsResponse{Version: request.Version}

	for _, hash := range request.BlobVersionedHashes {
		entry := e.pool[common.Hash(hash)]
		if entry == nil {
			return &engineall.GetBlobsResponse{Version: request.Version}, nil
		}

		resp.BlobsAndProofs = append(resp.BlobsAndProofs, entry)
	}

	return resp, nil
}

// testBlob returns a valid blob distinguished by seed and its versioned hash.
func testBlob(t *testing.T, seed byte) (kzg4844.Blob, common.Hash) {
	t.Helper()

	var blob kzg4844.Blob
	blob[31] = seed // the last byte of the first field element keeps it canonical

	commitment, err := kzg4844.BlobToCommitment(&blob)
	require.NoError(t, err)

	return blob, common.Hash(kzg4844.CalcBlobHashV1(sha256.New(), &commitment))
}

func blobTxWithHashes(nonce uint64, hashes ...common.Hash) *types.Transaction {
	return types.NewTx(&types.BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      nonce,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(1),
		Gas:        21000,
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(1),
		BlobHashes: hashes,
	})
}

func poolEntry(blob kzg4844.Blob) *engineall.BlobAndProof {
	return &engineall.BlobAndProof{
		Version: enginev.DataVersionOsaka,
		Blob:    cancun.Blob(blob),
		Proofs:  make([]cancun.KZGProof, kzg4844.CellProofsPerBlob),
	}
}

func TestEngineBlobSourceCompletesWhatThePoolHolds(t *testing.T) {
	blobA, hashA := testBlob(t, 1)
	blobB, hashB := testBlob(t, 2)
	_, hashMissing := testBlob(t, 3)
	_, hashWrong := testBlob(t, 4)

	complete := blobTxWithHashes(0, hashA, hashB)
	missing := blobTxWithHashes(1, hashMissing)
	wrong := blobTxWithHashes(2, hashWrong)

	engine := &blobPoolEngine{pool: map[common.Hash]*engineall.BlobAndProof{
		hashA:     poolEntry(blobA),
		hashB:     poolEntry(blobB),
		hashWrong: poolEntry(blobA), // a blob that does not match the hash
	}}

	source, ok := newEngineBlobSource(engine, enginev.DataVersionOsaka)
	require.True(t, ok)

	sidecars, err := source.BlobSidecars(context.Background(), []*types.Transaction{complete, missing, wrong})
	require.NoError(t, err)

	require.Len(t, sidecars, 1)

	sidecar := sidecars[complete.Hash()]
	require.NotNil(t, sidecar)
	require.Equal(t, types.BlobSidecarVersion1, sidecar.Version)
	require.Len(t, sidecar.Blobs, 2)
	require.Len(t, sidecar.Proofs, 2*kzg4844.CellProofsPerBlob)
	require.Equal(t, []common.Hash{hashA, hashB}, sidecar.BlobHashes(), "commitments match the versioned hashes")

	require.Equal(t, int32(4), engine.calls.Load(), "one batch, then one request per transaction")
}

func TestEngineBlobSourceOneRequestWhenComplete(t *testing.T) {
	blobA, hashA := testBlob(t, 1)
	blobB, hashB := testBlob(t, 2)

	engine := &blobPoolEngine{pool: map[common.Hash]*engineall.BlobAndProof{
		hashA: poolEntry(blobA),
		hashB: poolEntry(blobB),
	}}

	source, ok := newEngineBlobSource(engine, enginev.DataVersionOsaka)
	require.True(t, ok)

	txs := []*types.Transaction{blobTxWithHashes(0, hashA), blobTxWithHashes(1, hashB)}

	sidecars, err := source.BlobSidecars(context.Background(), txs)
	require.NoError(t, err)
	require.Len(t, sidecars, 2)
	require.Equal(t, int32(1), engine.calls.Load())
}

func TestEngineBlobSourceVersion(t *testing.T) {
	for payload, want := range map[enginev.DataVersion]enginev.DataVersion{
		enginev.DataVersionCancun: enginev.DataVersionCancun,
		enginev.DataVersionPrague: enginev.DataVersionCancun,
		enginev.DataVersionOsaka:  enginev.DataVersionOsaka,
		enginev.DataVersionBogota: enginev.DataVersionOsaka,
	} {
		source, ok := newEngineBlobSource(nil, payload)
		require.True(t, ok, payload.String())
		require.Equal(t, want, source.version, payload.String())
	}

	_, ok := newEngineBlobSource(nil, enginev.DataVersionShanghai)
	require.False(t, ok)
}
