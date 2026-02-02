// Package validators provides types and storage for Builder API validator registrations.
//
// This file embeds the builder-specs example for use in tests.
package validators

import _ "embed"

// BuilderSpecsExampleJSON is the official example from
// https://github.com/ethereum/builder-specs/blob/main/examples/bellatrix/signed_validator_registrations.json
// (request body is the array, not the {"value": [...]} wrapper).
//
//go:embed testdata/signed_validator_registrations.json
var BuilderSpecsExampleJSON []byte
