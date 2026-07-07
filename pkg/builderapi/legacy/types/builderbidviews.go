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

package types

import (
	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	"github.com/ethpandaops/go-eth2-client/spec/capella"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/electra"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/holiman/uint256"
)

// The per-fork builder-spec BuilderBid wire containers. They serve as the
// dynssz view schemas of the fork-agnostic BuilderBid/SignedBuilderBid and
// can be encoded directly through the runtime dynssz codec.

// BuilderBidBellatrix is the Bellatrix BuilderBid wire container.
type BuilderBidBellatrix struct {
	Header *bellatrix.ExecutionPayloadHeader
	Value  *uint256.Int     `ssz-type:"uint256"`
	Pubkey phase0.BLSPubKey `ssz-size:"48"`
}

// BuilderBidCapella is the Capella BuilderBid wire container.
type BuilderBidCapella struct {
	Header *capella.ExecutionPayloadHeader
	Value  *uint256.Int     `ssz-type:"uint256"`
	Pubkey phase0.BLSPubKey `ssz-size:"48"`
}

// BuilderBidDeneb is the Deneb BuilderBid wire container.
type BuilderBidDeneb struct {
	Header             *deneb.ExecutionPayloadHeader
	BlobKZGCommitments []deneb.KZGCommitment `dynssz-max:"MAX_BLOB_COMMITMENTS_PER_BLOCK" ssz-max:"4096" ssz-size:"?,48"`
	Value              *uint256.Int          `ssz-type:"uint256"`
	Pubkey             phase0.BLSPubKey      `ssz-size:"48"`
}

// BuilderBidElectra is the Electra BuilderBid wire container (also the Fulu
// schema).
type BuilderBidElectra struct {
	Header             *deneb.ExecutionPayloadHeader
	BlobKZGCommitments []deneb.KZGCommitment `dynssz-max:"MAX_BLOB_COMMITMENTS_PER_BLOCK" ssz-max:"4096" ssz-size:"?,48"`
	ExecutionRequests  *electra.ExecutionRequests
	Value              *uint256.Int     `ssz-type:"uint256"`
	Pubkey             phase0.BLSPubKey `ssz-size:"48"`
}

// SignedBuilderBidBellatrix is the Bellatrix SignedBuilderBid wire container.
type SignedBuilderBidBellatrix struct {
	Message   *BuilderBidBellatrix
	Signature phase0.BLSSignature `ssz-size:"96"`
}

// SignedBuilderBidCapella is the Capella SignedBuilderBid wire container.
type SignedBuilderBidCapella struct {
	Message   *BuilderBidCapella
	Signature phase0.BLSSignature `ssz-size:"96"`
}

// SignedBuilderBidDeneb is the Deneb SignedBuilderBid wire container.
type SignedBuilderBidDeneb struct {
	Message   *BuilderBidDeneb
	Signature phase0.BLSSignature `ssz-size:"96"`
}

// SignedBuilderBidElectra is the Electra SignedBuilderBid wire container
// (also the Fulu schema).
type SignedBuilderBidElectra struct {
	Message   *BuilderBidElectra
	Signature phase0.BLSSignature `ssz-size:"96"`
}
