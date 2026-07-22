package jqtransform

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{name: "empty is a valid no-op", expr: ""},
		{name: "identity", expr: "."},
		{name: "field assignment", expr: `.gas_limit = "42"`},
		{name: "pipe and object merge", expr: `. + {extra: 1}`},
		{name: "syntax error", expr: ".foo |", wantErr: true},
		{name: "unknown function", expr: "no_such_func(.)", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.expr)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestApply(t *testing.T) {
	ctx := context.Background()
	input := []byte(`{"gas_limit":"30000000","value":"5"}`)

	tests := []struct {
		name    string
		expr    string
		want    string
		wantErr error
	}{
		{name: "empty passes through", expr: "", want: `{"gas_limit":"30000000","value":"5"}`},
		{name: "identity", expr: ".", want: `{"gas_limit":"30000000","value":"5"}`},
		{
			name: "override a field",
			expr: `.gas_limit = "60000000"`,
			want: `{"gas_limit":"60000000","value":"5"}`,
		},
		{
			name: "add a field",
			expr: `. + {builder_index: "7"}`,
			want: `{"builder_index":"7","gas_limit":"30000000","value":"5"}`,
		},
		{name: "empty output rejected", expr: "empty", wantErr: ErrEmpty},
		{name: "multiple outputs rejected", expr: ".[]", wantErr: ErrMultiple},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Apply(ctx, tt.expr, input)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)

			// Compare as decoded values so key ordering is irrelevant.
			var got, want any
			require.NoError(t, json.Unmarshal(out, &got))
			require.NoError(t, json.Unmarshal([]byte(tt.want), &want))
			assert.Equal(t, want, got)
		})
	}
}

func TestApplyRuntimeErrorSurfaces(t *testing.T) {
	// Indexing a string with a field is a runtime type error in jq.
	_, err := Apply(context.Background(), `.value.foo`, []byte(`{"value":"5"}`))
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrEmpty)
	assert.NotErrorIs(t, err, ErrMultiple)
}

func TestApplyContextCancellation(t *testing.T) {
	// An unbounded generator must be cut off by the context rather than run
	// forever; reducing it into an array keeps it single-output until cancelled.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := Apply(ctx, `[repeat(1)]`, []byte(`{}`))
	require.Error(t, err)
}

func TestApplyEnvironmentIsBlocked(t *testing.T) {
	t.Setenv("JQ_SECRET", "leaked")

	out, err := Apply(context.Background(), `.env = env.JQ_SECRET`, []byte(`{}`))
	require.NoError(t, err)
	// env is empty (loader disabled), so the secret is not exposed.
	assert.JSONEq(t, `{"env":null}`, string(out))
}
