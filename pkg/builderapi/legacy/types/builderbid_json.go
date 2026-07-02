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
	"encoding/json"
	"fmt"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/holiman/uint256"
)

// builderBidBellatrixJSON is the Bellatrix/Capella wire shape (the header
// marshals per its pinned version).
type builderBidBellatrixJSON struct {
	Header *eth2all.ExecutionPayloadHeader `json:"header"`
	Value  string                          `json:"value"`
	Pubkey phase0.BLSPubKey                `json:"pubkey"`
}

// builderBidDenebJSON is the Deneb wire shape.
type builderBidDenebJSON struct {
	Header             *eth2all.ExecutionPayloadHeader `json:"header"`
	BlobKZGCommitments []deneb.KZGCommitment           `json:"blob_kzg_commitments"`
	Value              string                          `json:"value"`
	Pubkey             phase0.BLSPubKey                `json:"pubkey"`
}

// builderBidElectraJSON is the Electra/Fulu wire shape.
type builderBidElectraJSON struct {
	Header             *eth2all.ExecutionPayloadHeader `json:"header"`
	BlobKZGCommitments []deneb.KZGCommitment           `json:"blob_kzg_commitments"`
	ExecutionRequests  *eth2all.ExecutionRequests      `json:"execution_requests"`
	Value              string                          `json:"value"`
	Pubkey             phase0.BLSPubKey                `json:"pubkey"`
}

// MarshalJSON implements json.Marshaler, emitting the field set of the
// active Version. Value is a decimal string per builder-specs.
func (b *BuilderBid) MarshalJSON() ([]byte, error) {
	if err := b.assertSupportedVersion(); err != nil {
		return nil, err
	}

	val := "0"
	if b.Value != nil {
		val = b.Value.Dec()
	}

	header := b.headerView()

	commitments := b.BlobKZGCommitments
	if commitments == nil {
		commitments = []deneb.KZGCommitment{}
	}

	switch b.Version {
	case version.DataVersionBellatrix, version.DataVersionCapella:
		return json.Marshal(&builderBidBellatrixJSON{
			Header: header,
			Value:  val,
			Pubkey: b.Pubkey,
		})
	case version.DataVersionDeneb:
		return json.Marshal(&builderBidDenebJSON{
			Header:             header,
			BlobKZGCommitments: commitments,
			Value:              val,
			Pubkey:             b.Pubkey,
		})
	default: // Electra/Fulu; other versions rejected above.
		return json.Marshal(&builderBidElectraJSON{
			Header:             header,
			BlobKZGCommitments: commitments,
			ExecutionRequests:  b.executionRequestsView(),
			Value:              val,
			Pubkey:             b.Pubkey,
		})
	}
}

// UnmarshalJSON implements json.Unmarshaler. The caller must set Version on
// b before calling so the fork-dependent fields decode against the matching
// schema.
func (b *BuilderBid) UnmarshalJSON(data []byte) error {
	if err := b.assertSupportedVersion(); err != nil {
		return err
	}

	var aux struct {
		Header             json.RawMessage       `json:"header"`
		BlobKZGCommitments []deneb.KZGCommitment `json:"blob_kzg_commitments"`
		ExecutionRequests  json.RawMessage       `json:"execution_requests"`
		Value              string                `json:"value"`
		Pubkey             phase0.BLSPubKey      `json:"pubkey"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	b.Header = nil
	if len(aux.Header) > 0 && string(aux.Header) != "null" {
		header := &eth2all.ExecutionPayloadHeader{Version: b.Version}
		if err := json.Unmarshal(aux.Header, header); err != nil {
			return fmt.Errorf("header: %w", err)
		}
		b.Header = header
	}

	b.BlobKZGCommitments = nil
	if b.Version >= version.DataVersionDeneb {
		b.BlobKZGCommitments = aux.BlobKZGCommitments
	}

	b.ExecutionRequests = nil
	if b.Version >= version.DataVersionElectra &&
		len(aux.ExecutionRequests) > 0 && string(aux.ExecutionRequests) != "null" {
		requests := &eth2all.ExecutionRequests{Version: b.Version}
		if err := json.Unmarshal(aux.ExecutionRequests, requests); err != nil {
			return fmt.Errorf("execution_requests: %w", err)
		}
		b.ExecutionRequests = requests
	}

	b.Value = nil
	if aux.Value != "" {
		v, err := uint256.FromDecimal(aux.Value)
		if err != nil {
			return fmt.Errorf("value: %w", err)
		}
		b.Value = v
	}

	b.Pubkey = aux.Pubkey

	return nil
}
