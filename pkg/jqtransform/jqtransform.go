// Package jqtransform runs operator-supplied jq expressions against the JSON
// form of builder objects (execution payloads, bids, envelopes) so builders can
// apply arbitrary custom modifications the tool is not aware of. It wraps the
// pure-Go gojq interpreter (no I/O, context-cancellable) with a strict
// single-output contract and environment access disabled.
package jqtransform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/itchyny/gojq"
)

// ErrEmpty is returned when an expression produces no output (e.g. `empty` or a
// filter that selects nothing) — a transform must yield exactly one object.
var ErrEmpty = errors.New("jq expression produced no output")

// ErrMultiple is returned when an expression produces more than one output — a
// transform must yield exactly one object.
var ErrMultiple = errors.New("jq expression produced multiple outputs")

// Validate parses and compiles the expression, returning a descriptive error
// when it is not a valid jq program. Empty expressions are valid (no-op).
func Validate(expr string) error {
	if expr == "" {
		return nil
	}

	query, err := gojq.Parse(expr)
	if err != nil {
		return fmt.Errorf("parse jq expression: %w", err)
	}

	if _, err := compile(query); err != nil {
		return fmt.Errorf("compile jq expression: %w", err)
	}

	return nil
}

// Apply runs the expression against inputJSON (any JSON value) and returns the
// single transformed value marshaled back to JSON. An empty expression returns
// inputJSON unchanged. The context bounds execution time; a runaway expression
// (e.g. an unbounded generator) is cut off when ctx is cancelled.
func Apply(ctx context.Context, expr string, inputJSON []byte) ([]byte, error) {
	if expr == "" {
		return inputJSON, nil
	}

	query, err := gojq.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("parse jq expression: %w", err)
	}

	code, err := compile(query)
	if err != nil {
		return nil, fmt.Errorf("compile jq expression: %w", err)
	}

	var input any
	if err := json.Unmarshal(inputJSON, &input); err != nil {
		return nil, fmt.Errorf("decode transform input: %w", err)
	}

	iter := code.RunWithContext(ctx, input)

	first, ok := iter.Next()
	if !ok {
		return nil, ErrEmpty
	}

	if err, isErr := first.(error); isErr {
		if haltErr, isHalt := err.(*gojq.HaltError); isHalt && haltErr.Value() == nil {
			return nil, ErrEmpty
		}

		return nil, fmt.Errorf("evaluate jq expression: %w", err)
	}

	// A transform must be deterministic and single-valued; reject generators.
	if _, hasSecond := iter.Next(); hasSecond {
		return nil, ErrMultiple
	}

	out, err := json.Marshal(first)
	if err != nil {
		return nil, fmt.Errorf("encode transform output: %w", err)
	}

	return out, nil
}

// JSONObject is a value that can round-trip through JSON in both directions.
// The builder's fork-agnostic union types (execution payload, bid message,
// envelope message) satisfy it; their UnmarshalJSON requires the target's
// Version to be preset, so callers construct dst with the right Version.
type JSONObject interface {
	json.Marshaler
	json.Unmarshaler
}

// ApplyTyped applies the expression to src's JSON form and decodes the result
// into dst. dst must be constructed with the correct Version already set (the
// union types select their JSON view from Version). An empty expression copies
// src into dst unchanged. On any error dst is left partially written and must
// be discarded by the caller.
func ApplyTyped[T JSONObject](ctx context.Context, expr string, src, dst T) error {
	in, err := src.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshal transform source: %w", err)
	}

	out, err := Apply(ctx, expr, in)
	if err != nil {
		return err
	}

	if err := dst.UnmarshalJSON(out); err != nil {
		return fmt.Errorf("decode transform result into %T: %w", dst, err)
	}

	return nil
}

// compile builds the query with environment access disabled so expressions
// cannot read process state via `env` / `$ENV`; input functions are likewise
// absent (never wired), so `input`/`inputs` are unavailable.
func compile(query *gojq.Query) (*gojq.Code, error) {
	return gojq.Compile(query, gojq.WithEnvironLoader(func() []string { return nil }))
}
