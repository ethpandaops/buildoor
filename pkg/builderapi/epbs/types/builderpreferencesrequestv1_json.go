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

	"github.com/pkg/errors"
)

// builderPreferencesRequestV1JSON is the spec representation of the struct.
type builderPreferencesRequestV1JSON struct {
	Preferences *BuilderPreferencesV1 `json:"preferences"`
	Auth        *SignedRequestAuthV1  `json:"auth"`
}

// MarshalJSON implements json.Marshaler.
func (b *BuilderPreferencesRequestV1) MarshalJSON() ([]byte, error) {
	return json.Marshal(&builderPreferencesRequestV1JSON{
		Preferences: b.Preferences,
		Auth:        b.Auth,
	})
}

// UnmarshalJSON implements json.Unmarshaler.
func (b *BuilderPreferencesRequestV1) UnmarshalJSON(input []byte) error {
	var data builderPreferencesRequestV1JSON
	if err := json.Unmarshal(input, &data); err != nil {
		return errors.Wrap(err, "invalid JSON")
	}

	if data.Preferences == nil {
		return errors.New("preferences missing")
	}
	b.Preferences = data.Preferences

	if data.Auth == nil {
		return errors.New("auth missing")
	}
	b.Auth = data.Auth

	return nil
}
