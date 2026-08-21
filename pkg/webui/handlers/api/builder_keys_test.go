package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/builder_keys"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers/auth"
)

const testEntryPrivkey = "3f2b8e1c9d4a6f70b5c8e2a1d7943f6058ac2be91d3f5074a6b8c2e1d9f30475"

func keysTestHandler(t *testing.T, target uint64) *APIHandler {
	t.Helper()

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	keys := config.BuilderKeysConfig{TargetCount: target, DiscoveryGap: 1, MaxIndex: 32}

	cfg := config.DefaultConfig()
	cfg.BuilderKeys = keys

	defaults := config.DefaultConfig()
	defaults.BuilderKeys = keys

	// The settings service owns cfg, so it must be built before the registry
	// reads the resolved values.
	settingsSvc, err := config.NewService(cfg, defaults, map[string]bool{}, 0,
		db.NewDatabase(&db.Config{}, log), log)
	require.NoError(t, err)

	registry, err := builder_keys.NewRegistry(config.NewStaticService(cfg), testEntryPrivkey, log)
	require.NoError(t, err)
	registry.Refresh()

	authHandler, err := auth.NewAuthHandler(t.Context(), "")
	require.NoError(t, err)

	return &APIHandler{authHandler: authHandler, keys: registry, settingsSvc: settingsSvc}
}

func TestGetBuilderKeysWithoutRegistry(t *testing.T) {
	h := &APIHandler{}

	rec := httptest.NewRecorder()
	h.GetBuilderKeys(rec, httptest.NewRequest(http.MethodGet, "/api/buildoor/builder-keys", nil))

	require.Equal(t, http.StatusNotFound, rec.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Contains(t, body["error"], "builder key registry")
}

// The key set is readable without lifecycle management: the keys are derived
// either way, only mutating them needs the manager.
func TestGetBuilderKeysWithoutLifecycle(t *testing.T) {
	h := keysTestHandler(t, 3)

	rec := httptest.NewRecorder()
	h.GetBuilderKeys(rec, httptest.NewRequest(http.MethodGet, "/api/buildoor/builder-keys", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	var resp BuilderKeysResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Keys, 3)
	require.Equal(t, uint64(3), resp.Aggregate.Target)
	require.Equal(t, uint64(3), resp.Settings.TargetCount)

	for keyIndex, state := range resp.Keys {
		require.EqualValues(t, keyIndex, state.KeyIndex)
		require.NotEmpty(t, state.PubkeyHex)
		require.Equal(t, builder_keys.StatusUnused, state.Status)
	}
}

func TestMutatingBuilderKeyEndpointsRequireLifecycle(t *testing.T) {
	h := keysTestHandler(t, 2)

	handlers := map[string]http.HandlerFunc{
		"deposit": h.DepositBuilderKey,
		"topup":   h.TopupBuilderKey,
		"exit":    h.ExitBuilderKey,
	}

	for action, handler := range handlers {
		t.Run(action, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/buildoor/builder-keys/1/"+action, nil)
			req = mux.SetURLVars(req, map[string]string{"index": "1"})

			rec := httptest.NewRecorder()
			handler(rec, req)

			require.Equal(t, http.StatusNotFound, rec.Code)
		})
	}
}

func TestResolveKeyParamRejectsBadIndices(t *testing.T) {
	h := keysTestHandler(t, 2)

	tests := []struct {
		name  string
		index string
	}{
		{name: "not a number", index: "abc"},
		{name: "above the derivation cap", index: "99"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/buildoor/builder-keys/x/topup", nil)
			req = mux.SetURLVars(req, map[string]string{"index": test.index})

			rec := httptest.NewRecorder()
			require.Nil(t, h.resolveKeyParam(rec, req))
			require.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}
