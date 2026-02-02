// Package fulu provides Fulu (builder-specs) bid types for the Builder API.
package fulu

import (
	"encoding/json"

	"github.com/attestantio/go-eth2-client/spec/deneb"
	"github.com/attestantio/go-eth2-client/spec/electra"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/holiman/uint256"
	"github.com/pk910/dynamic-ssz/hasher"
	"github.com/pk910/dynamic-ssz/sszutils"
)

// BuilderBid is the Fulu/Electra builder bid message (header, blob_kzg_commitments, execution_requests, value, pubkey).
type BuilderBid struct {
	Header             *deneb.ExecutionPayloadHeader `json:"header"`
	BlobKZGCommitments []deneb.KZGCommitment         `json:"blob_kzg_commitments"`
	ExecutionRequests  *electra.ExecutionRequests    `json:"execution_requests"`
	Value              *uint256.Int                  `json:"value"` // wei as uint256 in spec
	Pubkey             phase0.BLSPubKey              `json:"pubkey"`
}

// SignedBuilderBid is the signed Fulu builder bid (getHeader response data).
type SignedBuilderBid struct {
	Message   *BuilderBid         `json:"message"`
	Signature phase0.BLSSignature `json:"signature"`
}

// HashTreeRoot returns the SSZ hash tree root of the BuilderBid (for signing).
func (b *BuilderBid) HashTreeRoot() ([32]byte, error) {
	var root [32]byte
	err := hasher.WithDefaultHasher(func(hh sszutils.HashWalker) error {
		err := b.HashTreeRootWith(hh)
		if err != nil {
			return err
		}
		root, err = hh.HashRoot()
		return err
	})
	return root, err
}

// HashTreeRootWith writes the BuilderBid SSZ tree into the hasher.
func (b *BuilderBid) HashTreeRootWith(hh sszutils.HashWalker) error {
	if b == nil {
		b = &BuilderBid{}
	}
	idx := hh.Index()

	// Field #0: header
	if b.Header != nil {
		if err := b.Header.HashTreeRootWith(hh); err != nil {
			return err
		}
	} else {
		hh.PutBytes(make([]byte, 32))
	}

	// Field #1: blob_kzg_commitments (list of KZGCommitment, max 4096)
	{
		t := b.BlobKZGCommitments
		if t == nil {
			t = []deneb.KZGCommitment{}
		}
		vlen := uint64(len(t))
		if vlen > 4096 {
			return sszutils.ErrListTooBig
		}
		idx2 := hh.Index()
		for i := range t {
			hh.PutBytes(t[i][:48])
		}
		hh.MerkleizeWithMixin(idx2, vlen, sszutils.CalculateLimit(4096, vlen, 32))
	}

	// Field #2: execution_requests
	if b.ExecutionRequests != nil {
		if err := b.ExecutionRequests.HashTreeRootWith(hh); err != nil {
			return err
		}
	} else {
		empty := &electra.ExecutionRequests{}
		if err := empty.HashTreeRootWith(hh); err != nil {
			return err
		}
	}

	// Field #3: value (uint256, 32 bytes)
	{
		val := b.Value
		if val == nil {
			val = new(uint256.Int)
		}
		valRoot, err := val.HashTreeRoot()
		if err != nil {
			return err
		}
		hh.AppendBytes32(valRoot[:])
	}

	// Field #4: pubkey (48 bytes)
	hh.PutBytes(b.Pubkey[:48])

	hh.Merkleize(idx)
	return nil
}

// GetHeaderResponse is the JSON response for getHeader: { "version": "fulu", "data": SignedBuilderBid }.
type GetHeaderResponse struct {
	Version string            `json:"version"`
	Data    *SignedBuilderBid `json:"data"`
}

// builderBidJSON is used for JSON marshal/unmarshal so value is a decimal string per builder-specs.
type builderBidJSON struct {
	Header             *deneb.ExecutionPayloadHeader `json:"header"`
	BlobKZGCommitments []deneb.KZGCommitment         `json:"blob_kzg_commitments"`
	ExecutionRequests  *electra.ExecutionRequests    `json:"execution_requests"`
	Value              string                        `json:"value"`
	Pubkey             phase0.BLSPubKey              `json:"pubkey"`
}

// MarshalJSON implements json.Marshaler for BuilderBid.
func (b *BuilderBid) MarshalJSON() ([]byte, error) {
	val := "0"
	if b.Value != nil {
		val = b.Value.Dec()
	}
	return json.Marshal(&builderBidJSON{
		Header:             b.Header,
		BlobKZGCommitments: b.BlobKZGCommitments,
		ExecutionRequests:  b.ExecutionRequests,
		Value:              val,
		Pubkey:             b.Pubkey,
	})
}

// UnmarshalJSON implements json.Unmarshaler for BuilderBid.
func (b *BuilderBid) UnmarshalJSON(data []byte) error {
	var aux builderBidJSON
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	b.Header = aux.Header
	b.BlobKZGCommitments = aux.BlobKZGCommitments
	b.ExecutionRequests = aux.ExecutionRequests
	b.Pubkey = aux.Pubkey
	if aux.Value != "" {
		v, err := uint256.FromDecimal(aux.Value)
		if err != nil {
			return err
		}
		b.Value = v
	}
	return nil
}
