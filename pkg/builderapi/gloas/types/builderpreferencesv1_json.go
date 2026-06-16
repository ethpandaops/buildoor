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
	"strconv"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/pkg/errors"
)

// builderPreferencesV1JSON is the spec representation of the struct.
type builderPreferencesV1JSON struct {
	MaxExecutionPayment string `json:"max_execution_payment"`
}

// MarshalJSON implements json.Marshaler.
func (b *BuilderPreferencesV1) MarshalJSON() ([]byte, error) {
	return json.Marshal(&builderPreferencesV1JSON{
		MaxExecutionPayment: fmt.Sprintf("%d", b.MaxExecutionPayment),
	})
}

// UnmarshalJSON implements json.Unmarshaler.
func (b *BuilderPreferencesV1) UnmarshalJSON(input []byte) error {
	var data builderPreferencesV1JSON
	if err := json.Unmarshal(input, &data); err != nil {
		return errors.Wrap(err, "invalid JSON")
	}

	if data.MaxExecutionPayment == "" {
		return errors.New("max execution payment missing")
	}
	maxExecutionPayment, err := strconv.ParseUint(data.MaxExecutionPayment, 10, 64)
	if err != nil {
		return errors.Wrap(err, "invalid max execution payment")
	}
	b.MaxExecutionPayment = phase0.Gwei(maxExecutionPayment)

	return nil
}
