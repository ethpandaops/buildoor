package payload_bidder

import (
	"encoding/hex"
	"testing"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	dynssz "github.com/pk910/dynamic-ssz"
	"github.com/stretchr/testify/require"
)

func TestGloasBidUsesBoundedCommitmentsListRoot(t *testing.T) {
	decode := func(value string) []byte {
		decoded, err := hex.DecodeString(value)
		require.NoError(t, err)
		return decoded
	}
	root := func(value string) (result phase0.Root) {
		copy(result[:], decode(value))
		return result
	}

	bid := &eth2all.ExecutionPayloadBid{
		Version:               version.DataVersionGloas,
		ParentBlockHash:       phase0.Hash32(root("882d22ed3a133fd42ba744661121f750e5bfa32fbdd5f08025f2356ff2d56962")),
		ParentBlockRoot:       root("9474a47b8c7f35d85a5f59b6fb25e17471aa37ee228ecc3292d0e8551e84fa6b"),
		BlockHash:             phase0.Hash32(root("7396438bc6390d1ef45bd0b842cfad8c3e782abbfb5381e6d9def8cef3d15a91")),
		PrevRandao:            root("a86033297239e33101fccfaca4217a0ee291d957dbd86ab46211f70b53c0e0c5"),
		FeeRecipient:          bellatrix.ExecutionAddress(decode("8943545177806ed17b9f23f0a21ee5948ecaa776")),
		GasLimit:              150000000,
		BuilderIndex:          gloas.BuilderIndex(0),
		Slot:                  180,
		Value:                 100000,
		ExecutionPayment:      0,
		BlobKZGCommitments:    nil,
		ExecutionRequestsRoot: root("87b69a306c8e430d0857f7c4ac5e27cecffa1108d43c2e5df7388056fea7a423"),
	}

	actual, err := hashGloasBid(bid)
	require.NoError(t, err)
	require.Equal(t,
		"05443306d810e015f5c790dc4be85203a1b7d1c9e4e5819e711c8b730500a598",
		hex.EncodeToString(actual[:]),
	)
}

func TestGloasExecutionRequestsUseBoundedListRoots(t *testing.T) {
	actual, err := hashGloasExecutionRequests(&eth2all.ExecutionRequests{
		Version: version.DataVersionGloas,
	})
	require.NoError(t, err)
	require.Equal(t,
		"3a02ed03ab3f2af7ef0e6ee1a44326479d2ab1d2f03613dd1ec0a11fcf37af96",
		hex.EncodeToString(actual[:]),
	)
}

func TestGloasEnvelopeUsesBoundedNestedListRoots(t *testing.T) {
	actual, err := hashGloasEnvelope(&eth2all.ExecutionPayloadEnvelope{
		Version:           version.DataVersionGloas,
		Payload:           &eth2all.ExecutionPayload{Version: version.DataVersionGloas},
		ExecutionRequests: &eth2all.ExecutionRequests{Version: version.DataVersionGloas},
		BuilderIndex:      gloas.BuilderIndex(0),
		BeaconBlockRoot:   phase0.Root{0x11},
	})
	require.NoError(t, err)
	require.Equal(t,
		"743eaf4d36852670927ebaa1cd7893ce86b0da831ad2da818bcb5dd740e1e546",
		hex.EncodeToString(actual[:]),
	)
}

func TestGloasSigningViewsUseConnectedPresetLimits(t *testing.T) {
	dynssz.SetGlobalSpecs(nil)
	t.Cleanup(func() { dynssz.SetGlobalSpecs(nil) })

	t.Run("bid commitments", func(t *testing.T) {
		bid := &eth2all.ExecutionPayloadBid{Version: version.DataVersionGloas}
		mainnetRoot, err := hashGloasBid(bid)
		require.NoError(t, err)

		dynssz.SetGlobalSpecs(map[string]any{
			"MAX_BLOB_COMMITMENTS_PER_BLOCK": uint64(32),
		})
		presetRoot, err := hashGloasBid(bid)
		require.NoError(t, err)
		require.NotEqual(t, mainnetRoot, presetRoot)
	})

	t.Run("execution requests", func(t *testing.T) {
		dynssz.SetGlobalSpecs(nil)
		requests := &eth2all.ExecutionRequests{Version: version.DataVersionGloas}
		mainnetRoot, err := hashGloasExecutionRequests(requests)
		require.NoError(t, err)

		dynssz.SetGlobalSpecs(map[string]any{
			"MAX_DEPOSIT_REQUESTS_PER_PAYLOAD": uint64(16),
		})
		presetRoot, err := hashGloasExecutionRequests(requests)
		require.NoError(t, err)
		require.NotEqual(t, mainnetRoot, presetRoot)
	})

	t.Run("envelope withdrawals", func(t *testing.T) {
		dynssz.SetGlobalSpecs(nil)
		envelope := &eth2all.ExecutionPayloadEnvelope{
			Version:           version.DataVersionGloas,
			Payload:           &eth2all.ExecutionPayload{Version: version.DataVersionGloas},
			ExecutionRequests: &eth2all.ExecutionRequests{Version: version.DataVersionGloas},
		}
		mainnetRoot, err := hashGloasEnvelope(envelope)
		require.NoError(t, err)

		dynssz.SetGlobalSpecs(map[string]any{
			"MAX_WITHDRAWALS_PER_PAYLOAD": uint64(4),
		})
		presetRoot, err := hashGloasEnvelope(envelope)
		require.NoError(t, err)
		require.NotEqual(t, mainnetRoot, presetRoot)
	})
}
