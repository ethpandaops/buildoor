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
	"errors"
	"fmt"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	dynssz "github.com/pk910/dynamic-ssz"
	"github.com/pk910/dynamic-ssz/sszutils"
)

// SignedBuilderBid is the fork-agnostic signed builder bid carried in the
// getHeader response data (Bellatrix through Fulu). The message's wire shape
// follows Version. Callers must set Version before unmarshaling.
type SignedBuilderBid struct {
	Version   version.DataVersion
	Message   *BuilderBid
	Signature phase0.BLSSignature
}

// viewType returns the fork-specific schema type pointer used as the view
// descriptor for the active Version.
func (s *SignedBuilderBid) viewType() (any, error) {
	switch s.Version {
	case version.DataVersionBellatrix:
		return (*SignedBuilderBidBellatrix)(nil), nil
	case version.DataVersionCapella:
		return (*SignedBuilderBidCapella)(nil), nil
	case version.DataVersionDeneb:
		return (*SignedBuilderBidDeneb)(nil), nil
	case version.DataVersionElectra,
		version.DataVersionFulu:
		// Fulu reuses the Electra builder bid schema unchanged.
		return (*SignedBuilderBidElectra)(nil), nil
	default:
		return nil, fmt.Errorf("SignedBuilderBid: unsupported version %s", s.Version)
	}
}

// populateVersion sets Version and propagates it to the nested versionable
// children.
func (s *SignedBuilderBid) populateVersion(v version.DataVersion) {
	s.Version = v

	if s.Message != nil {
		s.Message.populateVersion(v)
	}
}

// MarshalSSZDyn marshals the signed bid using the view that matches Version.
func (s *SignedBuilderBid) MarshalSSZDyn(ds sszutils.DynamicSpecs, buf []byte) ([]byte, error) {
	view, err := s.viewType()
	if err != nil {
		return nil, err
	}

	m, ok := any(s).(sszutils.DynamicViewMarshaler)
	if !ok {
		return nil, errors.New("SignedBuilderBid: generated SSZ code missing")
	}

	fn := m.MarshalSSZDynView(view)
	if fn == nil {
		return nil, fmt.Errorf("SignedBuilderBid: no view marshaler for version %s", s.Version)
	}

	return fn(ds, buf)
}

// SizeSSZDyn returns the SSZ size of the signed bid for the active Version.
func (s *SignedBuilderBid) SizeSSZDyn(ds sszutils.DynamicSpecs) int {
	view, err := s.viewType()
	if err != nil {
		return 0
	}

	sz, ok := any(s).(sszutils.DynamicViewSizer)
	if !ok {
		return 0
	}

	fn := sz.SizeSSZDynView(view)
	if fn == nil {
		return 0
	}

	return fn(ds)
}

// UnmarshalSSZDyn decodes the signed bid from the view that matches Version.
func (s *SignedBuilderBid) UnmarshalSSZDyn(ds sszutils.DynamicSpecs, buf []byte) error {
	view, err := s.viewType()
	if err != nil {
		return err
	}

	u, ok := any(s).(sszutils.DynamicViewUnmarshaler)
	if !ok {
		return errors.New("SignedBuilderBid: generated SSZ code missing")
	}

	fn := u.UnmarshalSSZDynView(view)
	if fn == nil {
		return fmt.Errorf("SignedBuilderBid: no view unmarshaler for version %s", s.Version)
	}

	if err := fn(ds, buf); err != nil {
		return err
	}

	s.populateVersion(s.Version)

	return nil
}

// HashTreeRootWithDyn computes the SSZ hash tree root using the active
// Version's view.
func (s *SignedBuilderBid) HashTreeRootWithDyn(ds sszutils.DynamicSpecs, hh sszutils.HashWalker) error {
	view, err := s.viewType()
	if err != nil {
		return err
	}

	h, ok := any(s).(sszutils.DynamicViewHashRoot)
	if !ok {
		return errors.New("SignedBuilderBid: generated SSZ code missing")
	}

	fn := h.HashTreeRootWithDynView(view)
	if fn == nil {
		return fmt.Errorf("SignedBuilderBid: no view hasher for version %s", s.Version)
	}

	return fn(ds, hh)
}

// MarshalSSZ implements the fastssz.Marshaler interface.
func (s *SignedBuilderBid) MarshalSSZ() ([]byte, error) {
	ds := dynssz.GetGlobalDynSsz()

	return s.MarshalSSZDyn(ds, make([]byte, 0, s.SizeSSZDyn(ds)))
}

// MarshalSSZTo implements the fastssz.Marshaler interface.
func (s *SignedBuilderBid) MarshalSSZTo(dst []byte) ([]byte, error) {
	return s.MarshalSSZDyn(dynssz.GetGlobalDynSsz(), dst)
}

// UnmarshalSSZ implements the fastssz.Unmarshaler interface.
func (s *SignedBuilderBid) UnmarshalSSZ(buf []byte) error {
	return s.UnmarshalSSZDyn(dynssz.GetGlobalDynSsz(), buf)
}

// SizeSSZ implements the fastssz.Marshaler interface.
func (s *SignedBuilderBid) SizeSSZ() int {
	return s.SizeSSZDyn(dynssz.GetGlobalDynSsz())
}

// HashTreeRoot implements the fastssz.HashRoot interface.
func (s *SignedBuilderBid) HashTreeRoot() ([32]byte, error) {
	return dynssz.GetGlobalDynSsz().HashTreeRoot(s)
}

// HashTreeRootWith implements the fastssz.HashRoot interface.
func (s *SignedBuilderBid) HashTreeRootWith(hh sszutils.HashWalker) error {
	return s.HashTreeRootWithDyn(dynssz.GetGlobalDynSsz(), hh)
}

// signedBuilderBidJSON is the wire shape of the signed bid (identical across
// forks; the message's shape follows its version).
type signedBuilderBidJSON struct {
	Message   *BuilderBid         `json:"message"`
	Signature phase0.BLSSignature `json:"signature"`
}

// MarshalJSON implements json.Marshaler, emitting the message with the field
// set of the active Version.
func (s *SignedBuilderBid) MarshalJSON() ([]byte, error) {
	if _, err := s.viewType(); err != nil {
		return nil, err
	}

	message := s.Message
	if message != nil && message.Version != s.Version {
		m := *message
		m.Version = s.Version
		message = &m
	}

	return json.Marshal(&signedBuilderBidJSON{
		Message:   message,
		Signature: s.Signature,
	})
}

// UnmarshalJSON implements json.Unmarshaler. The caller must set Version on
// s before calling so the message decodes against the matching schema.
func (s *SignedBuilderBid) UnmarshalJSON(data []byte) error {
	if _, err := s.viewType(); err != nil {
		return err
	}

	var aux struct {
		Message   json.RawMessage     `json:"message"`
		Signature phase0.BLSSignature `json:"signature"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	s.Message = nil
	if len(aux.Message) > 0 && string(aux.Message) != "null" {
		message := &BuilderBid{Version: s.Version}
		if err := json.Unmarshal(aux.Message, message); err != nil {
			return fmt.Errorf("message: %w", err)
		}
		s.Message = message
	}

	s.Signature = aux.Signature

	return nil
}
