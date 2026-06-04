package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/builderapi"
	"github.com/ethpandaops/buildoor/pkg/config"
)

func TestGetBuilderPreferences_NotEnabled(t *testing.T) {
	// No builder API service wired → 404.
	h := NewAPIHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/buildoor/builder-preferences", nil)
	rec := httptest.NewRecorder()
	h.GetBuilderPreferences(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestGetBuilderPreferences_ReturnsEntries(t *testing.T) {
	cfg := &config.BuilderAPIConfig{Port: 0}
	srv := builderapi.NewServer(cfg, logrus.New(), nil, nil, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})

	var pk phase0.BLSPubKey
	pk[0] = 0xab
	srv.GetBuilderPreferencesStore().Set(pk, 5_000_000_000)

	// builderSvc (2nd arg) nil so the event stream manager does not start.
	h := NewAPIHandler(nil, nil, nil, nil, nil, nil, srv, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/buildoor/builder-preferences", nil)
	rec := httptest.NewRecorder()
	h.GetBuilderPreferences(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp BuilderPreferencesResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Preferences, 1)
	assert.Equal(t, "0x"+hex.EncodeToString(pk[:]), resp.Preferences[0].ValidatorPubkey)
	assert.Equal(t, uint64(5_000_000_000), resp.Preferences[0].MaxExecutionPayment)
}
