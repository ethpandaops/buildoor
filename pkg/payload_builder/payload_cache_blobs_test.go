package payload_builder

import (
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

func blobPayload(slot phase0.Slot, hash byte) *Payload {
	return &Payload{
		Attributes:  &beacon.PayloadAttributesEvent{ProposalSlot: slot},
		BlockHash:   phase0.Hash32{hash},
		BlobsBundle: NewBlobsBundle([]deneb.KZGCommitment{{1}}, []deneb.KZGProof{{2}}, []deneb.Blob{{3}}),
	}
}

func TestPayloadCacheReleasesOldBlobs(t *testing.T) {
	cache := NewPayloadCache(DefaultCacheSize)

	old := blobPayload(100, 1)
	cache.Store(old)

	replaced := blobPayload(100, 2)
	cache.Store(replaced) // supersedes old

	recent := blobPayload(105, 3)
	cache.Store(recent)
	require.False(t, old.BlobsBundle.Released(), "within the retention window")

	cache.Store(blobPayload(100+blobRetentionSlots+1, 4))

	for _, payload := range []*Payload{old, replaced} {
		require.True(t, payload.BlobsBundle.Released())
		require.Nil(t, payload.BlobsBundle.Blobs())
		require.Len(t, payload.BlobsBundle.Commitments, 1, "commitments stay for bids and counts")
	}

	require.False(t, recent.BlobsBundle.Released())
	require.Len(t, recent.BlobsBundle.Blobs(), 1)
	require.Equal(t, replaced, cache.GetByBlockHash(phase0.Hash32{2}), "the payload itself stays cached")
}
