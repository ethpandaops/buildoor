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

// Package types contains the fork-agnostic builder-spec wire containers of
// the legacy (pre-Gloas) Builder API dialect. The builder-spec types are not
// part of go-eth2-client (and go-builder-client is intentionally not
// imported), so they are defined here on top of the fork-agnostic spec/all
// component types, following the spec/all view pattern.
package types

import (
	"errors"
	"fmt"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/holiman/uint256"
	dynssz "github.com/pk910/dynamic-ssz"
	"github.com/pk910/dynamic-ssz/sszutils"
)

// BuilderBid is the fork-agnostic builder-spec BuilderBid message served in
// getHeader responses, covering Bellatrix through Fulu. The fields present
// on the wire depend on Version:
//   - Bellatrix/Capella: header, value, pubkey
//   - Deneb:             adds blob_kzg_commitments
//   - Electra/Fulu:      adds execution_requests
type BuilderBid struct {
	Version            version.DataVersion
	Header             *eth2all.ExecutionPayloadHeader
	BlobKZGCommitments []deneb.KZGCommitment      // Deneb onwards
	ExecutionRequests  *eth2all.ExecutionRequests // Electra onwards
	Value              *uint256.Int               // wei as uint256 in spec
	Pubkey             phase0.BLSPubKey
}

// viewType returns the fork-specific schema type pointer used as the view
// descriptor for the active Version. Pre-Bellatrix has no builder API and
// Gloas+ replaces it with payload bids, so those versions are rejected.
func (b *BuilderBid) viewType() (any, error) {
	switch b.Version {
	case version.DataVersionBellatrix:
		return (*BuilderBidBellatrix)(nil), nil
	case version.DataVersionCapella:
		return (*BuilderBidCapella)(nil), nil
	case version.DataVersionDeneb:
		return (*BuilderBidDeneb)(nil), nil
	case version.DataVersionElectra,
		version.DataVersionFulu:
		// Fulu reuses the Electra builder bid schema unchanged.
		return (*BuilderBidElectra)(nil), nil
	default:
		return nil, fmt.Errorf("BuilderBid: unsupported version %s", b.Version)
	}
}

// assertSupportedVersion rejects versions without a builder bid wire type.
func (b *BuilderBid) assertSupportedVersion() error {
	_, err := b.viewType()

	return err
}

// headerView returns a copy of the header pinned to the bid's Version so its
// fork-agnostic codecs select the schema matching the bid.
func (b *BuilderBid) headerView() *eth2all.ExecutionPayloadHeader {
	if b.Header == nil {
		return nil
	}

	header := *b.Header
	header.Version = b.Version

	return &header
}

// executionRequestsView returns a copy of the execution requests pinned to
// the bid's Version, defaulting to empty requests when unset.
func (b *BuilderBid) executionRequestsView() *eth2all.ExecutionRequests {
	requests := eth2all.ExecutionRequests{}
	if b.ExecutionRequests != nil {
		requests = *b.ExecutionRequests
	}

	requests.Version = b.Version

	return &requests
}

// populateVersion sets Version and propagates it to the nested versionable
// children.
func (b *BuilderBid) populateVersion(v version.DataVersion) {
	b.Version = v

	if b.Header != nil {
		b.Header.Version = v
	}

	if b.ExecutionRequests != nil {
		b.ExecutionRequests.Version = v
	}
}

// MarshalSSZDyn marshals the bid using the view that matches Version.
func (b *BuilderBid) MarshalSSZDyn(ds sszutils.DynamicSpecs, buf []byte) ([]byte, error) {
	view, err := b.viewType()
	if err != nil {
		return nil, err
	}

	m, ok := any(b).(sszutils.DynamicViewMarshaler)
	if !ok {
		return nil, errors.New("BuilderBid: generated SSZ code missing")
	}

	fn := m.MarshalSSZDynView(view)
	if fn == nil {
		return nil, fmt.Errorf("BuilderBid: no view marshaler for version %s", b.Version)
	}

	return fn(ds, buf)
}

// SizeSSZDyn returns the SSZ size of the bid for the active Version.
func (b *BuilderBid) SizeSSZDyn(ds sszutils.DynamicSpecs) int {
	view, err := b.viewType()
	if err != nil {
		return 0
	}

	sz, ok := any(b).(sszutils.DynamicViewSizer)
	if !ok {
		return 0
	}

	fn := sz.SizeSSZDynView(view)
	if fn == nil {
		return 0
	}

	return fn(ds)
}

// UnmarshalSSZDyn decodes the bid from the view that matches Version.
func (b *BuilderBid) UnmarshalSSZDyn(ds sszutils.DynamicSpecs, buf []byte) error {
	view, err := b.viewType()
	if err != nil {
		return err
	}

	u, ok := any(b).(sszutils.DynamicViewUnmarshaler)
	if !ok {
		return errors.New("BuilderBid: generated SSZ code missing")
	}

	fn := u.UnmarshalSSZDynView(view)
	if fn == nil {
		return fmt.Errorf("BuilderBid: no view unmarshaler for version %s", b.Version)
	}

	if err := fn(ds, buf); err != nil {
		return err
	}

	b.populateVersion(b.Version)

	return nil
}

// HashTreeRootWithDyn computes the SSZ hash tree root using the active
// Version's view.
func (b *BuilderBid) HashTreeRootWithDyn(ds sszutils.DynamicSpecs, hh sszutils.HashWalker) error {
	view, err := b.viewType()
	if err != nil {
		return err
	}

	h, ok := any(b).(sszutils.DynamicViewHashRoot)
	if !ok {
		return errors.New("BuilderBid: generated SSZ code missing")
	}

	fn := h.HashTreeRootWithDynView(view)
	if fn == nil {
		return fmt.Errorf("BuilderBid: no view hasher for version %s", b.Version)
	}

	return fn(ds, hh)
}

// MarshalSSZ implements the fastssz.Marshaler interface.
func (b *BuilderBid) MarshalSSZ() ([]byte, error) {
	ds := dynssz.GetGlobalDynSsz()

	return b.MarshalSSZDyn(ds, make([]byte, 0, b.SizeSSZDyn(ds)))
}

// MarshalSSZTo implements the fastssz.Marshaler interface.
func (b *BuilderBid) MarshalSSZTo(dst []byte) ([]byte, error) {
	return b.MarshalSSZDyn(dynssz.GetGlobalDynSsz(), dst)
}

// UnmarshalSSZ implements the fastssz.Unmarshaler interface.
func (b *BuilderBid) UnmarshalSSZ(buf []byte) error {
	return b.UnmarshalSSZDyn(dynssz.GetGlobalDynSsz(), buf)
}

// SizeSSZ implements the fastssz.Marshaler interface.
func (b *BuilderBid) SizeSSZ() int {
	return b.SizeSSZDyn(dynssz.GetGlobalDynSsz())
}

// HashTreeRoot implements the fastssz.HashRoot interface (used for signing).
func (b *BuilderBid) HashTreeRoot() ([32]byte, error) {
	return dynssz.GetGlobalDynSsz().HashTreeRoot(b)
}

// HashTreeRootWith implements the fastssz.HashRoot interface.
func (b *BuilderBid) HashTreeRootWith(hh sszutils.HashWalker) error {
	return b.HashTreeRootWithDyn(dynssz.GetGlobalDynSsz(), hh)
}
