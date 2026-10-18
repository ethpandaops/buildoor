package payload_builder

import (
	"encoding/json"
	"sync"

	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
)

// BlobsBundle holds the blobs, KZG commitments and proofs produced alongside a
// payload, in beacon (deneb) types. The engine API bundle is converted to this
// once in the builder (see beaconBlobsBundleFromEngine) so downstream consumers
// use the beacon types directly instead of re-converting at every call site.
//
// The blobs and proofs are only needed while the payload can still be
// unblinded or revealed: the payload cache releases them a few slots later
// (see blobRetentionSlots) and keeps the commitments, which bids and counts
// read for as long as the payload is cached. Read them through Blobs/Proofs.
type BlobsBundle struct {
	Commitments []deneb.KZGCommitment

	mu       sync.RWMutex
	proofs   []deneb.KZGProof
	blobs    []deneb.Blob
	released bool
}

// NewBlobsBundle creates a bundle.
func NewBlobsBundle(commitments []deneb.KZGCommitment, proofs []deneb.KZGProof, blobs []deneb.Blob) *BlobsBundle {
	return &BlobsBundle{Commitments: commitments, proofs: proofs, blobs: blobs}
}

// beaconBlobsBundleFromEngine converts the engine API blobs bundle into the
// beacon-typed bundle. Returns nil when src is nil. The KZG/blob element types
// are byte arrays of identical size on both sides, so each is a direct cast.
func beaconBlobsBundleFromEngine(src *engineall.BlobsBundle) *BlobsBundle {
	if src == nil {
		return nil
	}

	out := NewBlobsBundle(
		make([]deneb.KZGCommitment, len(src.Commitments)),
		make([]deneb.KZGProof, len(src.Proofs)),
		make([]deneb.Blob, len(src.Blobs)),
	)

	for i := range src.Commitments {
		out.Commitments[i] = deneb.KZGCommitment(src.Commitments[i])
	}

	for i := range src.Proofs {
		out.proofs[i] = deneb.KZGProof(src.Proofs[i])
	}

	for i := range src.Blobs {
		out.blobs[i] = deneb.Blob(src.Blobs[i])
	}

	return out
}

// Blobs returns the blobs (nil once released). Nil-safe.
func (b *BlobsBundle) Blobs() []deneb.Blob {
	if b == nil {
		return nil
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	return b.blobs
}

// Proofs returns the KZG proofs (nil once released). Nil-safe.
func (b *BlobsBundle) Proofs() []deneb.KZGProof {
	if b == nil {
		return nil
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	return b.proofs
}

// Released reports that the blobs and proofs were dropped (the payload is
// too old to be unblinded or revealed). Nil-safe.
func (b *BlobsBundle) Released() bool {
	if b == nil {
		return false
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	return b.released
}

// release drops the blobs and proofs; the commitments stay. Slices already
// handed out stay valid for their holders.
func (b *BlobsBundle) release() {
	if b == nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.blobs) == 0 && len(b.proofs) == 0 {
		return
	}

	b.blobs, b.proofs, b.released = nil, nil, true
}

// BlobsAsBytes returns the blobs as raw byte slices for beacon submission.
// Nil-safe: returns nil for a nil bundle.
func (b *BlobsBundle) BlobsAsBytes() [][]byte {
	blobs := b.Blobs()

	out := make([][]byte, len(blobs))
	for i := range blobs {
		out[i] = blobs[i][:]
	}

	return out
}

// ProofsAsBytes returns the KZG proofs as raw byte slices for beacon submission.
// Nil-safe: returns nil for a nil bundle.
func (b *BlobsBundle) ProofsAsBytes() [][]byte {
	proofs := b.Proofs()

	out := make([][]byte, len(proofs))
	for i := range proofs {
		out[i] = proofs[i][:]
	}

	return out
}

// MarshalJSON renders the bundle as {commitments, proofs, blobs} hex arrays
// (empty arrays, never null). Nil-safe: a nil bundle marshals to JSON null.
func (b *BlobsBundle) MarshalJSON() ([]byte, error) {
	if b == nil {
		return []byte("null"), nil
	}

	out := struct {
		Commitments []deneb.KZGCommitment `json:"commitments"`
		Proofs      []deneb.KZGProof      `json:"proofs"`
		Blobs       []deneb.Blob          `json:"blobs"`
	}{
		Commitments: b.Commitments,
		Proofs:      b.Proofs(),
		Blobs:       b.Blobs(),
	}

	if out.Commitments == nil {
		out.Commitments = []deneb.KZGCommitment{}
	}

	if out.Proofs == nil {
		out.Proofs = []deneb.KZGProof{}
	}

	if out.Blobs == nil {
		out.Blobs = []deneb.Blob{}
	}

	return json.Marshal(&out)
}
