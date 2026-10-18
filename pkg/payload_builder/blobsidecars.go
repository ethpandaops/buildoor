package payload_builder

import (
	"context"
	"crypto/sha256"
	"fmt"
	"runtime"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth-engine-client/spec/paris"
	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"
)

// engineBlobSource completes the EL mempool blob transactions of a custom
// block's EL fill with their sidecars. txpool_content lists them without
// their blobs; the EL's blob pool still holds them and serves them through
// engine_getBlobs, the call beacon nodes use for blob recovery. The KZG
// commitments are not part of that answer: they are computed from the blobs
// and checked against the transaction's versioned hashes.
type engineBlobSource struct {
	engine EngineClient
	// version is the engine_getBlobs version: cancun (V1, one proof per
	// blob, sidecar version 0) or osaka (V2, cell proofs, sidecar version 1).
	version enginev.DataVersion
}

// newEngineBlobSource returns the blob source for a payload of engine
// version payloadVersion; ok is false before Cancun.
func newEngineBlobSource(engine EngineClient, payloadVersion enginev.DataVersion) (*engineBlobSource, bool) {
	switch {
	case payloadVersion >= enginev.DataVersionOsaka:
		return &engineBlobSource{engine: engine, version: enginev.DataVersionOsaka}, true
	case payloadVersion >= enginev.DataVersionCancun:
		return &engineBlobSource{engine: engine, version: enginev.DataVersionCancun}, true
	default:
		return nil, false
	}
}

// BlobSidecars implements txpool.SidecarSource. All blobs are asked for in
// one request first; engine_getBlobs answers all-or-nothing, so when one
// blob is missing every transaction is asked for alone and only the
// incomplete ones are left out.
func (s *engineBlobSource) BlobSidecars(
	ctx context.Context,
	txs []*types.Transaction,
) (map[common.Hash]*types.BlobTxSidecar, error) {
	hashes := make([]common.Hash, 0, len(txs))
	for _, tx := range txs {
		hashes = append(hashes, tx.BlobHashes()...)
	}

	batch, err := s.getBlobs(ctx, hashes)
	if err != nil {
		return nil, err
	}

	sidecars := make([]*types.BlobTxSidecar, len(txs))
	limit := make(chan struct{}, runtime.NumCPU())

	var wg sync.WaitGroup

	offset := 0

	for i, tx := range txs {
		count := len(tx.BlobHashes())

		var entries []*engineall.BlobAndProof
		if batch != nil {
			entries = batch[offset : offset+count]
		}

		offset += count

		wg.Add(1)

		go func() {
			defer wg.Done()

			limit <- struct{}{}
			defer func() { <-limit }()

			if entries == nil {
				own, err := s.getBlobs(ctx, tx.BlobHashes())
				if err != nil || own == nil {
					return
				}

				entries = own
			}

			sidecar, err := s.sidecar(tx, entries)
			if err != nil {
				return
			}

			sidecars[i] = sidecar
		}()
	}

	wg.Wait()

	out := make(map[common.Hash]*types.BlobTxSidecar, len(txs))

	for i, tx := range txs {
		if sidecars[i] != nil {
			out[tx.Hash()] = sidecars[i]
		}
	}

	return out, nil
}

// getBlobs asks the EL blob pool for the blobs of hashes. It returns nil
// when the EL lacks any of them (or is syncing).
func (s *engineBlobSource) getBlobs(ctx context.Context, hashes []common.Hash) ([]*engineall.BlobAndProof, error) {
	request := &engineall.GetBlobsRequest{
		Version:             s.version,
		BlobVersionedHashes: make([]paris.Hash32, len(hashes)),
	}

	for i, hash := range hashes {
		request.BlobVersionedHashes[i] = paris.Hash32(hash)
	}

	resp, err := s.engine.GetBlobsAgnostic(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("engine_getBlobs: %w", err)
	}

	if resp == nil || len(resp.BlobsAndProofs) != len(hashes) {
		return nil, nil
	}

	for _, entry := range resp.BlobsAndProofs {
		if entry == nil {
			return nil, nil
		}
	}

	return resp.BlobsAndProofs, nil
}

// sidecar assembles a transaction's sidecar from its blob pool entries.
func (s *engineBlobSource) sidecar(tx *types.Transaction, entries []*engineall.BlobAndProof) (*types.BlobTxSidecar, error) {
	hashes := tx.BlobHashes()
	if len(entries) != len(hashes) {
		return nil, fmt.Errorf("%d blobs for %d versioned hashes", len(entries), len(hashes))
	}

	sidecarVersion := types.BlobSidecarVersion0
	proofsPerBlob := 1

	if s.version >= enginev.DataVersionOsaka {
		sidecarVersion = types.BlobSidecarVersion1
		proofsPerBlob = kzg4844.CellProofsPerBlob
	}

	blobs := make([]kzg4844.Blob, len(entries))
	commitments := make([]kzg4844.Commitment, len(entries))
	proofs := make([]kzg4844.Proof, 0, len(entries)*proofsPerBlob)

	for i, entry := range entries {
		blobs[i] = kzg4844.Blob(entry.Blob)

		commitment, err := kzg4844.BlobToCommitment(&blobs[i])
		if err != nil {
			return nil, fmt.Errorf("blob %d: %w", i, err)
		}

		if common.Hash(kzg4844.CalcBlobHashV1(sha256.New(), &commitment)) != hashes[i] {
			return nil, fmt.Errorf("blob %d does not match its versioned hash", i)
		}

		commitments[i] = commitment

		if sidecarVersion == types.BlobSidecarVersion1 {
			if len(entry.Proofs) != proofsPerBlob {
				return nil, fmt.Errorf("blob %d: %d cell proofs", i, len(entry.Proofs))
			}

			for _, proof := range entry.Proofs {
				proofs = append(proofs, kzg4844.Proof(proof))
			}
		} else {
			proofs = append(proofs, kzg4844.Proof(entry.Proof))
		}
	}

	return types.NewBlobTxSidecar(sidecarVersion, blobs, commitments, proofs), nil
}
