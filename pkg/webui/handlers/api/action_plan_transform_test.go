package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/webui/handlers/auth"
)

// openTransformHandler is an API handler in open mode (no auth provider),
// where every request counts as logged in.
func openTransformHandler(t *testing.T) *APIHandler {
	t.Helper()

	authHandler, err := auth.NewAuthHandler(t.Context(), "")
	require.NoError(t, err)

	return &APIHandler{authHandler: authHandler} // nil resultTracker → template sample path
}

func postTransform(t *testing.T, body string) *TestTransformResponse {
	t.Helper()

	h := openTransformHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/action-plan/test-transform",
		bytes.NewBufferString(body))
	rec := httptest.NewRecorder()

	h.TestTransform(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp TestTransformResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	return &resp
}

func TestTestTransformEndpoint(t *testing.T) {
	t.Run("identity on template returns input unchanged", func(t *testing.T) {
		resp := postTransform(t, `{"target":"bid","expression":"."}`)
		assert.Empty(t, resp.Error)
		assert.Equal(t, "template", resp.InputSource)
		assert.JSONEq(t, resp.Input, resp.Output)
	})

	t.Run("field override is reflected in the output", func(t *testing.T) {
		resp := postTransform(t, `{"target":"bid","expression":".gas_limit = \"99\""}`)
		require.Empty(t, resp.Error)

		var out map[string]any
		require.NoError(t, json.Unmarshal([]byte(resp.Output), &out))
		assert.Equal(t, "99", out["gas_limit"])
	})

	t.Run("payload template exposes payload fields", func(t *testing.T) {
		resp := postTransform(t, `{"target":"payload","expression":".block_hash"}`)
		require.Empty(t, resp.Error)
		assert.Contains(t, string(resp.Output), "0x")
	})

	t.Run("envelope template nests the payload", func(t *testing.T) {
		resp := postTransform(t, `{"target":"envelope","expression":".payload.gas_limit"}`)
		require.Empty(t, resp.Error)
		assert.Equal(t, `"30000000"`, string(resp.Output))
	})

	t.Run("parse error is reported in the body, not as HTTP error", func(t *testing.T) {
		resp := postTransform(t, `{"target":"bid","expression":".gas_limit |"}`)
		assert.NotEmpty(t, resp.Error)
		assert.Empty(t, resp.Output)
	})

	t.Run("runtime error is reported", func(t *testing.T) {
		resp := postTransform(t, `{"target":"bid","expression":".gas_limit.nope"}`)
		assert.NotEmpty(t, resp.Error)
	})
}

func TestTestTransformRejectsUnknownTarget(t *testing.T) {
	h := openTransformHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/x",
		bytes.NewBufferString(`{"target":"nope","expression":"."}`))
	rec := httptest.NewRecorder()

	h.TestTransform(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
