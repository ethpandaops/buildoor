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

// Package types holds the Gloas-fork Builder API request/response types
// (RequestAuth, SignedRequestAuth, BuilderPreferences, BuilderPreferencesRequest).
// They were vendored from go-builder-client so buildoor owns them directly, and
// use buildoor's ethpandaops/go-eth2-client phase0 types. The _ssz.go files are
// generated; see generate.go.
package types

import (
	"fmt"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/goccy/go-yaml"
)

// RequestAuthV1 is used by a proposer to authenticate a bid request to a specific
// builder. The proposer signs over a generic data field (set to the builder's
// URL) and the slot to prevent other builders from replaying the request to
// learn the builder's valuation, and to prevent DOS attempts from competing
// parties.
type RequestAuthV1 struct {
	Data []byte `ssz-max:"4096"`
	Slot phase0.Slot
}

// String returns a string version of the structure.
func (r *RequestAuthV1) String() string {
	data, err := yaml.Marshal(r)
	if err != nil {
		return fmt.Sprintf("ERR: %v", err)
	}

	return string(data)
}
