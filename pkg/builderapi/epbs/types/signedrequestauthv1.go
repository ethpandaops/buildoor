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
	"fmt"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/goccy/go-yaml"
)

// SignedRequestAuthV1 wraps a RequestAuthV1 with the proposer's signature over the
// hash tree root of the message. It is sent in the body of getExecutionPayloadBid
// and submitBuilderPreferences requests so that builders can authenticate the
// requesting validator.
type SignedRequestAuthV1 struct {
	Message   *RequestAuthV1
	Signature phase0.BLSSignature `ssz-size:"96"`
}

// String returns a string version of the structure.
func (s *SignedRequestAuthV1) String() string {
	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Sprintf("ERR: %v", err)
	}

	return string(data)
}
