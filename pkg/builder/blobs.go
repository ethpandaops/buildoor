package builder

import (
	"encoding/json"

	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
)

// BlobsBundle holds the blobs, KZG commitments and proofs produced alongside a
// payload, in beacon (deneb) types. The engine API bundle is converted to this
// once in the builder (see beaconBlobsBundleFromEngine) so downstream consumers
// use the beacon types directly instead of re-converting at every call site.
type BlobsBundle struct {
	Commitments []deneb.KZGCommitment `json:"commitments"`
	Proofs      []deneb.KZGProof      `json:"proofs"`
	Blobs       []deneb.Blob          `json:"blobs"`
}

// beaconBlobsBundleFromEngine converts the engine API blobs bundle into the
// beacon-typed bundle. Returns nil when src is nil. The KZG/blob element types
// are byte arrays of identical size on both sides, so each is a direct cast.
func beaconBlobsBundleFromEngine(src *engineall.BlobsBundle) *BlobsBundle {
	if src == nil {
		return nil
	}

	out := &BlobsBundle{
		Commitments: make([]deneb.KZGCommitment, len(src.Commitments)),
		Proofs:      make([]deneb.KZGProof, len(src.Proofs)),
		Blobs:       make([]deneb.Blob, len(src.Blobs)),
	}

	for i := range src.Commitments {
		out.Commitments[i] = deneb.KZGCommitment(src.Commitments[i])
	}

	for i := range src.Proofs {
		out.Proofs[i] = deneb.KZGProof(src.Proofs[i])
	}

	for i := range src.Blobs {
		out.Blobs[i] = deneb.Blob(src.Blobs[i])
	}

	return out
}

// BlobsAsBytes returns the blobs as raw byte slices for beacon submission.
// Nil-safe: returns nil for a nil bundle.
func (b *BlobsBundle) BlobsAsBytes() [][]byte {
	if b == nil {
		return nil
	}

	out := make([][]byte, len(b.Blobs))
	for i := range b.Blobs {
		out[i] = b.Blobs[i][:]
	}

	return out
}

// ProofsAsBytes returns the KZG proofs as raw byte slices for beacon submission.
// Nil-safe: returns nil for a nil bundle.
func (b *BlobsBundle) ProofsAsBytes() [][]byte {
	if b == nil {
		return nil
	}

	out := make([][]byte, len(b.Proofs))
	for i := range b.Proofs {
		out[i] = b.Proofs[i][:]
	}

	return out
}

// MarshalJSON renders the bundle as {commitments, proofs, blobs} hex arrays.
// Nil-safe: a nil bundle marshals to JSON null.
func (b *BlobsBundle) MarshalJSON() ([]byte, error) {
	if b == nil {
		return []byte("null"), nil
	}

	type alias BlobsBundle

	return json.Marshal((*alias)(b))
}
