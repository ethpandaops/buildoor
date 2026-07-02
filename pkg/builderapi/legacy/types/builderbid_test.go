// Copyright © 2026 Attestant Limited.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package types_test

import (
	"encoding/json"
	"testing"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/capella"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/electra"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/holiman/uint256"
	dynssz "github.com/pk910/dynamic-ssz"
	"github.com/stretchr/testify/require"

	legacytypes "github.com/ethpandaops/buildoor/pkg/builderapi/legacy/types"
)

// testAgnosticHeader builds a fork-agnostic execution payload header with
// both base-fee representations filled consistently.
func testAgnosticHeader() *eth2all.ExecutionPayloadHeader {
	return &eth2all.ExecutionPayloadHeader{
		ParentHash:       phase0.Hash32{0x01},
		FeeRecipient:     bellatrix.ExecutionAddress{0x02},
		StateRoot:        phase0.Root{0x03},
		ReceiptsRoot:     phase0.Root{0x04},
		LogsBloom:        [256]byte{0x05},
		PrevRandao:       [32]byte{0x06},
		BlockNumber:      7,
		GasLimit:         30_000_000,
		GasUsed:          21_000,
		Timestamp:        1_600_000_000,
		ExtraData:        []byte{0x08},
		BaseFeePerGasLE:  [32]byte{0x09},
		BaseFeePerGas:    uint256.NewInt(9),
		BlockHash:        phase0.Hash32{0x0a},
		TransactionsRoot: phase0.Root{0x0b},
		WithdrawalsRoot:  phase0.Root{0x0c},
		BlobGasUsed:      131072,
		ExcessBlobGas:    262144,
	}
}

// testDenebHeader builds the Deneb view of testAgnosticHeader.
func testDenebHeader() *deneb.ExecutionPayloadHeader {
	return &deneb.ExecutionPayloadHeader{
		ParentHash:       phase0.Hash32{0x01},
		FeeRecipient:     bellatrix.ExecutionAddress{0x02},
		StateRoot:        phase0.Root{0x03},
		ReceiptsRoot:     phase0.Root{0x04},
		LogsBloom:        [256]byte{0x05},
		PrevRandao:       [32]byte{0x06},
		BlockNumber:      7,
		GasLimit:         30_000_000,
		GasUsed:          21_000,
		Timestamp:        1_600_000_000,
		ExtraData:        []byte{0x08},
		BaseFeePerGas:    uint256.NewInt(9),
		BlockHash:        phase0.Hash32{0x0a},
		TransactionsRoot: phase0.Root{0x0b},
		WithdrawalsRoot:  phase0.Root{0x0c},
		BlobGasUsed:      131072,
		ExcessBlobGas:    262144,
	}
}

func testCommitments() []deneb.KZGCommitment {
	return []deneb.KZGCommitment{{0x17}, {0x18}}
}

func testElectraRequests() *electra.ExecutionRequests {
	return &electra.ExecutionRequests{
		Deposits: []*electra.DepositRequest{},
		Withdrawals: []*electra.WithdrawalRequest{{
			SourceAddress:   bellatrix.ExecutionAddress{0x21},
			ValidatorPubkey: phase0.BLSPubKey{0x22},
			Amount:          23,
		}},
		Consolidations: []*electra.ConsolidationRequest{},
	}
}

var (
	testValue     = uint256.NewInt(1_500_000_000_000_000)
	testPubkey    = phase0.BLSPubKey{0x31}
	testSignature = phase0.BLSSignature{0x32}
)

// testAgnosticSignedBid builds the fork-agnostic signed bid for a version.
func testAgnosticSignedBid(v version.DataVersion) *legacytypes.SignedBuilderBid {
	bid := &legacytypes.BuilderBid{
		Version: v,
		Header:  testAgnosticHeader(),
		Value:   testValue,
		Pubkey:  testPubkey,
	}

	if v >= version.DataVersionDeneb {
		bid.BlobKZGCommitments = testCommitments()
	}

	if v >= version.DataVersionElectra {
		reqs := testElectraRequests()
		bid.ExecutionRequests = &eth2all.ExecutionRequests{
			Version:        v,
			Deposits:       reqs.Deposits,
			Withdrawals:    reqs.Withdrawals,
			Consolidations: reqs.Consolidations,
		}
	}

	return &legacytypes.SignedBuilderBid{
		Version:   v,
		Message:   bid,
		Signature: testSignature,
	}
}

// signedBuilderBidTests enumerates the per-fork view instances the agnostic
// SignedBuilderBid must be wire-compatible with.
func signedBuilderBidTests() []struct {
	name    string
	version version.DataVersion
	view    any
} {
	bellatrixView := &legacytypes.SignedBuilderBidBellatrix{
		Message: &legacytypes.BuilderBidBellatrix{
			Header: &bellatrix.ExecutionPayloadHeader{
				ParentHash:       phase0.Hash32{0x01},
				FeeRecipient:     bellatrix.ExecutionAddress{0x02},
				StateRoot:        [32]byte{0x03},
				ReceiptsRoot:     [32]byte{0x04},
				LogsBloom:        [256]byte{0x05},
				PrevRandao:       [32]byte{0x06},
				BlockNumber:      7,
				GasLimit:         30_000_000,
				GasUsed:          21_000,
				Timestamp:        1_600_000_000,
				ExtraData:        []byte{0x08},
				BaseFeePerGasLE:  [32]byte{0x09},
				BlockHash:        phase0.Hash32{0x0a},
				TransactionsRoot: phase0.Root{0x0b},
			},
			Value:  testValue,
			Pubkey: testPubkey,
		},
		Signature: testSignature,
	}

	capellaView := &legacytypes.SignedBuilderBidCapella{
		Message: &legacytypes.BuilderBidCapella{
			Header: &capella.ExecutionPayloadHeader{
				ParentHash:       phase0.Hash32{0x01},
				FeeRecipient:     bellatrix.ExecutionAddress{0x02},
				StateRoot:        [32]byte{0x03},
				ReceiptsRoot:     [32]byte{0x04},
				LogsBloom:        [256]byte{0x05},
				PrevRandao:       [32]byte{0x06},
				BlockNumber:      7,
				GasLimit:         30_000_000,
				GasUsed:          21_000,
				Timestamp:        1_600_000_000,
				ExtraData:        []byte{0x08},
				BaseFeePerGasLE:  [32]byte{0x09},
				BlockHash:        phase0.Hash32{0x0a},
				TransactionsRoot: phase0.Root{0x0b},
				WithdrawalsRoot:  phase0.Root{0x0c},
			},
			Value:  testValue,
			Pubkey: testPubkey,
		},
		Signature: testSignature,
	}

	denebView := &legacytypes.SignedBuilderBidDeneb{
		Message: &legacytypes.BuilderBidDeneb{
			Header:             testDenebHeader(),
			BlobKZGCommitments: testCommitments(),
			Value:              testValue,
			Pubkey:             testPubkey,
		},
		Signature: testSignature,
	}

	electraView := &legacytypes.SignedBuilderBidElectra{
		Message: &legacytypes.BuilderBidElectra{
			Header:             testDenebHeader(),
			BlobKZGCommitments: testCommitments(),
			ExecutionRequests:  testElectraRequests(),
			Value:              testValue,
			Pubkey:             testPubkey,
		},
		Signature: testSignature,
	}

	return []struct {
		name    string
		version version.DataVersion
		view    any
	}{
		{name: "Bellatrix", version: version.DataVersionBellatrix, view: bellatrixView},
		{name: "Capella", version: version.DataVersionCapella, view: capellaView},
		{name: "Deneb", version: version.DataVersionDeneb, view: denebView},
		{name: "Electra", version: version.DataVersionElectra, view: electraView},
		// Fulu reuses the Electra builder bid schema.
		{name: "Fulu", version: version.DataVersionFulu, view: electraView},
	}
}

// TestSignedBuilderBidSSZWireCompat verifies the agnostic type's generated
// view codecs produce byte-identical SSZ and the same hash tree root as the
// runtime-dynssz encoding of the per-fork view container, and that SSZ
// round-trips losslessly through the agnostic type.
func TestSignedBuilderBidSSZWireCompat(t *testing.T) {
	ds := dynssz.GetGlobalDynSsz()

	for _, test := range signedBuilderBidTests() {
		t.Run(test.name, func(t *testing.T) {
			expected, err := ds.MarshalSSZ(test.view)
			require.NoError(t, err)

			agnostic := testAgnosticSignedBid(test.version)

			got, err := agnostic.MarshalSSZ()
			require.NoError(t, err)
			require.Equal(t, expected, got, "agnostic SSZ differs from per-fork view SSZ")

			expectedRoot, err := ds.HashTreeRoot(test.view)
			require.NoError(t, err)

			gotRoot, err := agnostic.HashTreeRoot()
			require.NoError(t, err)
			require.Equal(t, expectedRoot, gotRoot,
				"agnostic hash tree root differs from per-fork view root")

			// Round-trip through UnmarshalSSZ with Version pre-set.
			rt := &legacytypes.SignedBuilderBid{Version: test.version}
			require.NoError(t, rt.UnmarshalSSZ(expected))

			rtSSZ, err := rt.MarshalSSZ()
			require.NoError(t, err)
			require.Equal(t, expected, rtSSZ,
				"round-tripped SSZ differs from per-fork view SSZ")

			// Version must propagate to nested versionable children.
			require.NotNil(t, rt.Message)
			require.Equal(t, test.version, rt.Message.Version)
		})
	}
}

// TestBuilderBidHashTreeRoot verifies the bid message root (the signing root
// input) matches the runtime-dynssz root of the per-fork view message.
func TestBuilderBidHashTreeRoot(t *testing.T) {
	ds := dynssz.GetGlobalDynSsz()

	views := map[string]any{
		"Bellatrix": signedBuilderBidTests()[0].view.(*legacytypes.SignedBuilderBidBellatrix).Message,
		"Capella":   signedBuilderBidTests()[1].view.(*legacytypes.SignedBuilderBidCapella).Message,
		"Deneb":     signedBuilderBidTests()[2].view.(*legacytypes.SignedBuilderBidDeneb).Message,
		"Electra":   signedBuilderBidTests()[3].view.(*legacytypes.SignedBuilderBidElectra).Message,
	}
	versions := map[string]version.DataVersion{
		"Bellatrix": version.DataVersionBellatrix,
		"Capella":   version.DataVersionCapella,
		"Deneb":     version.DataVersionDeneb,
		"Electra":   version.DataVersionElectra,
	}

	for name, view := range views {
		t.Run(name, func(t *testing.T) {
			expectedRoot, err := ds.HashTreeRoot(view)
			require.NoError(t, err)

			gotRoot, err := testAgnosticSignedBid(versions[name]).Message.HashTreeRoot()
			require.NoError(t, err)
			require.Equal(t, expectedRoot, gotRoot)
		})
	}
}

// TestSignedBuilderBidJSONShape verifies the JSON wire shape follows the
// active version's field set and round-trips losslessly.
func TestSignedBuilderBidJSONShape(t *testing.T) {
	for _, test := range signedBuilderBidTests() {
		t.Run(test.name, func(t *testing.T) {
			agnostic := testAgnosticSignedBid(test.version)

			data, err := json.Marshal(agnostic)
			require.NoError(t, err)

			var decoded struct {
				Message   map[string]json.RawMessage `json:"message"`
				Signature string                     `json:"signature"`
			}
			require.NoError(t, json.Unmarshal(data, &decoded))

			_, hasHeader := decoded.Message["header"]
			require.True(t, hasHeader, "bid must carry header")
			_, hasValue := decoded.Message["value"]
			require.True(t, hasValue, "bid must carry value")
			_, hasPubkey := decoded.Message["pubkey"]
			require.True(t, hasPubkey, "bid must carry pubkey")

			_, hasCommitments := decoded.Message["blob_kzg_commitments"]
			require.Equal(t, test.version >= version.DataVersionDeneb, hasCommitments,
				"blob_kzg_commitments presence must follow the fork")

			_, hasRequests := decoded.Message["execution_requests"]
			require.Equal(t, test.version >= version.DataVersionElectra, hasRequests,
				"execution_requests presence must follow the fork")

			// Round-trip through UnmarshalJSON with Version pre-set.
			rt := &legacytypes.SignedBuilderBid{Version: test.version}
			require.NoError(t, json.Unmarshal(data, rt))

			rtJSON, err := json.Marshal(rt)
			require.NoError(t, err)
			require.Equal(t, string(data), string(rtJSON),
				"round-tripped JSON differs")
		})
	}
}

// TestBuilderBidUnsupportedVersion verifies unsupported versions are
// rejected with a clear error.
func TestBuilderBidUnsupportedVersion(t *testing.T) {
	for _, v := range []version.DataVersion{
		version.DataVersionUnknown,
		version.DataVersionPhase0,
		version.DataVersionAltair,
		version.DataVersionGloas,
	} {
		bid := &legacytypes.BuilderBid{Version: v}

		_, err := json.Marshal(bid)
		require.ErrorContains(t, err, "unsupported version")

		_, err = bid.MarshalSSZ()
		require.ErrorContains(t, err, "unsupported version")

		signed := &legacytypes.SignedBuilderBid{Version: v, Message: bid}

		_, err = json.Marshal(signed)
		require.ErrorContains(t, err, "unsupported version")

		_, err = signed.MarshalSSZ()
		require.ErrorContains(t, err, "unsupported version")
	}
}
