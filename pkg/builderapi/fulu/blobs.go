package fulu

import (
	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
)

// This file centralizes conversions from the engine API blobs bundle into the
// shapes the consensus-layer submission paths need. The engine bundle is kept
// as-is on the payload; conversion happens once, here, at the CL edge.

// CommitmentsToDeneb returns the bundle's KZG commitments as deneb commitments.
// Returns nil when bundle is nil.
func CommitmentsToDeneb(bundle *engineall.BlobsBundle) []deneb.KZGCommitment {
	if bundle == nil {
		return nil
	}

	out := make([]deneb.KZGCommitment, len(bundle.Commitments))
	for i := range bundle.Commitments {
		out[i] = deneb.KZGCommitment(bundle.Commitments[i])
	}

	return out
}

// ProofsToDeneb returns the bundle's KZG proofs as deneb proofs.
// Returns nil when bundle is nil.
func ProofsToDeneb(bundle *engineall.BlobsBundle) []deneb.KZGProof {
	if bundle == nil {
		return nil
	}

	out := make([]deneb.KZGProof, len(bundle.Proofs))
	for i := range bundle.Proofs {
		out[i] = deneb.KZGProof(bundle.Proofs[i])
	}

	return out
}

// BlobsToDeneb returns the bundle's blobs as deneb blobs.
// Returns nil when bundle is nil.
func BlobsToDeneb(bundle *engineall.BlobsBundle) []deneb.Blob {
	if bundle == nil {
		return nil
	}

	out := make([]deneb.Blob, len(bundle.Blobs))
	for i := range bundle.Blobs {
		out[i] = deneb.Blob(bundle.Blobs[i])
	}

	return out
}

// BlobsAsBytes returns the bundle's blobs as raw byte slices for beacon
// submission. Returns nil when bundle is nil.
func BlobsAsBytes(bundle *engineall.BlobsBundle) [][]byte {
	if bundle == nil {
		return nil
	}

	out := make([][]byte, len(bundle.Blobs))
	for i := range bundle.Blobs {
		out[i] = bundle.Blobs[i][:]
	}

	return out
}

// ProofsAsBytes returns the bundle's KZG proofs as raw byte slices for beacon
// submission. Returns nil when bundle is nil.
func ProofsAsBytes(bundle *engineall.BlobsBundle) [][]byte {
	if bundle == nil {
		return nil
	}

	out := make([][]byte, len(bundle.Proofs))
	for i := range bundle.Proofs {
		out[i] = bundle.Proofs[i][:]
	}

	return out
}
