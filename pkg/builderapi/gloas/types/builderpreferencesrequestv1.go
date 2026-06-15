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

	"github.com/goccy/go-yaml"
)

// BuilderPreferencesRequestV1 is the body submitted to a builder via the
// submitBuilderPreferences API. The Auth.Message.Data identifies the
// intended builder so the builder can reject preferences that were not
// destined for it.
type BuilderPreferencesRequestV1 struct {
	Preferences *BuilderPreferencesV1
	Auth        *SignedRequestAuthV1
}

// String returns a string version of the structure.
func (b *BuilderPreferencesRequestV1) String() string {
	data, err := yaml.Marshal(b)
	if err != nil {
		return fmt.Sprintf("ERR: %v", err)
	}

	return string(data)
}
