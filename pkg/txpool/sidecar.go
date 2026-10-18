package txpool

import (
	"fmt"

	"github.com/ethereum/go-ethereum/core/types"
	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"

	"github.com/ethpandaops/buildoor/pkg/chain"
)

// normalizeSidecar checks a blob transaction's sidecar against its versioned
// hashes and brings it into the proof format the next block needs: from Osaka
// on blobs carry cell proofs (sidecar version 1), so a version 0 sidecar is
// converted once here instead of in every selection. Other transactions pass
// through unchanged.
func (p *Pool) normalizeSidecar(tx *types.Transaction) (*types.Transaction, error) {
	sidecar := tx.BlobTxSidecar()
	if tx.Type() != types.BlobTxType || sidecar == nil {
		return tx, nil
	}

	if err := sidecar.ValidateBlobCommitmentHashes(tx.BlobHashes()); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBlobSidecar, err)
	}

	if sidecar.Version != types.BlobSidecarVersion0 || !p.CellProofsActive() {
		return tx, nil
	}

	converted := sidecar.Copy()
	if err := converted.ToV1(); err != nil {
		return nil, fmt.Errorf("%w: computing cell proofs: %w", ErrBlobSidecar, err)
	}

	return tx.WithBlobTxSidecar(converted), nil
}

// CellProofsActive reports whether the next slot's block is Osaka or later,
// where blob sidecars carry cell proofs.
func (p *Pool) CellProofsActive() bool {
	if p.chainSvc == nil {
		return false
	}

	fork := p.chainSvc.ActiveForkAtEpoch(p.chainSvc.GetEpochOfSlot(p.chainSvc.GetCurrentSlot() + 1))

	engineVersion, err := chain.EngineVersion(fork)

	return err == nil && engineVersion >= enginev.DataVersionOsaka
}

// sidecarFits reports whether a transaction can be carried in the network
// encoding of a block that does (cellProofs) or does not use cell proofs. A
// sidecar admitted before the fork transition can have the other format.
func sidecarFits(tx *types.Transaction, cellProofs bool) bool {
	sidecar := tx.BlobTxSidecar()
	if tx.Type() != types.BlobTxType || sidecar == nil {
		return true
	}

	if cellProofs {
		return sidecar.Version == types.BlobSidecarVersion1
	}

	return sidecar.Version == types.BlobSidecarVersion0
}
