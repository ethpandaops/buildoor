package builderapi

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gloasauth "github.com/ethpandaops/buildoor/pkg/builderapi/gloas"
	gloastypes "github.com/ethpandaops/buildoor/pkg/builderapi/gloas/types"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

const (
	testValidatorPrivkey = "0x0000000000000000000000000000000000000000000000000000000000000001"
	testOtherPrivkey     = "0x0000000000000000000000000000000000000000000000000000000000000002"
	testBuilderURL       = "https://builder.example.com"
)

// signBuilderPrefsRequest builds a JSON BuilderPreferencesRequestV1 whose auth is
// signed by s over (builderURL, slot) using DOMAIN_REQUEST_AUTH at the given
// genesis fork version (GVR=zero per spec).
func signBuilderPrefsRequest(
	t *testing.T,
	s *signer.BLSSigner,
	builderURL string,
	slot phase0.Slot,
	maxPayment phase0.Gwei,
	genesisForkVersion phase0.Version,
) []byte {
	t.Helper()

	auth := &gloastypes.RequestAuthV1{
		Data: []byte(builderURL),
		Slot: slot,
	}
	root, err := auth.HashTreeRoot()
	require.NoError(t, err)

	domain := signer.ComputeDomain(gloasauth.DomainRequestAuth, genesisForkVersion, phase0.Root{})
	sig, err := s.SignWithDomain(phase0.Root(root), domain)
	require.NoError(t, err)

	req := &gloastypes.BuilderPreferencesRequestV1{
		Preferences: &gloastypes.BuilderPreferencesV1{MaxExecutionPayment: maxPayment},
		Auth: &gloastypes.SignedRequestAuthV1{
			Message:   auth,
			Signature: sig,
		},
	}
	body, err := json.Marshal(req)
	require.NoError(t, err)
	return body
}

func TestBuilderPreferencesStore(t *testing.T) {
	store := NewBuilderPreferencesStore()

	var pk1, pk2 phase0.BLSPubKey
	pk1[0] = 1
	pk2[0] = 2

	// Absent → GetOrDefault is 0, Get reports not found.
	assert.Equal(t, phase0.Gwei(0), store.GetOrDefault(pk1))
	_, ok := store.Get(pk1)
	assert.False(t, ok)

	// Set then read back.
	store.Set(pk1, 100)
	got, ok := store.Get(pk1)
	require.True(t, ok)
	assert.Equal(t, phase0.Gwei(100), got)
	assert.Equal(t, phase0.Gwei(100), store.GetOrDefault(pk1))

	// Overwrite keeps only the latest value.
	store.Set(pk1, 250)
	got, _ = store.Get(pk1)
	assert.Equal(t, phase0.Gwei(250), got)

	store.Set(pk2, 7)

	// GetAll returns a snapshot of all entries.
	all := store.GetAll()
	require.Len(t, all, 2)
	assert.Equal(t, phase0.Gwei(250), all[pk1])
	assert.Equal(t, phase0.Gwei(7), all[pk2])

	// The snapshot is a copy — mutating it must not affect the store.
	all[pk1] = 999
	got, _ = store.Get(pk1)
	assert.Equal(t, phase0.Gwei(250), got)
}

func TestSubmitBuilderPreferences_Success(t *testing.T) {
	gfv := phase0.Version{}
	blsSigner, err := signer.NewBLSSigner(testValidatorPrivkey)
	require.NoError(t, err)

	cfg := &config.BuilderAPIConfig{BuilderURL: testBuilderURL}
	srv := NewServer(cfg, logrus.New(), nil, nil, nil, gfv, phase0.Version{}, phase0.Root{})
	srv.SetEnabled(true)

	body := signBuilderPrefsRequest(t, blsSigner, testBuilderURL, 100, 5_000_000_000, gfv)
	pk := blsSigner.PublicKey()
	url := "/eth/v1/builder/builder_preferences/0x" + hex.EncodeToString(pk[:])

	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)
	got, ok := srv.builderPrefsStore.Get(pk)
	require.True(t, ok, "preference should be stored after successful auth")
	assert.Equal(t, phase0.Gwei(5_000_000_000), got)
}

func TestSubmitBuilderPreferences_SuccessSSZ(t *testing.T) {
	gfv := phase0.Version{}
	blsSigner, err := signer.NewBLSSigner(testValidatorPrivkey)
	require.NoError(t, err)

	cfg := &config.BuilderAPIConfig{BuilderURL: testBuilderURL}
	srv := NewServer(cfg, logrus.New(), nil, nil, nil, gfv, phase0.Version{}, phase0.Root{})
	srv.SetEnabled(true)

	// Build the same signed request as the JSON path, but submit it SSZ-encoded.
	jsonBody := signBuilderPrefsRequest(t, blsSigner, testBuilderURL, 100, 5_000_000_000, gfv)
	var prefsReq gloastypes.BuilderPreferencesRequestV1
	require.NoError(t, json.Unmarshal(jsonBody, &prefsReq))
	sszBody, err := prefsReq.MarshalSSZ()
	require.NoError(t, err)

	pk := blsSigner.PublicKey()
	url := "/eth/v1/builder/builder_preferences/0x" + hex.EncodeToString(pk[:])
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(sszBody))
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)
	got, ok := srv.builderPrefsStore.Get(pk)
	require.True(t, ok, "preference should be stored after successful SSZ-decoded auth")
	assert.Equal(t, phase0.Gwei(5_000_000_000), got)
}

func TestSubmitBuilderPreferences_MalformedSSZ(t *testing.T) {
	cfg := &config.BuilderAPIConfig{BuilderURL: testBuilderURL}
	srv := NewServer(cfg, logrus.New(), nil, nil, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})
	srv.SetEnabled(true)

	url := "/eth/v1/builder/builder_preferences/0x" + hex.EncodeToString(make([]byte, 48))
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader([]byte{0x01, 0x02, 0x03}))
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSubmitBuilderPreferences_UnknownContentType(t *testing.T) {
	cfg := &config.BuilderAPIConfig{BuilderURL: testBuilderURL}
	srv := NewServer(cfg, logrus.New(), nil, nil, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})
	srv.SetEnabled(true)

	url := "/eth/v1/builder/builder_preferences/0x" + hex.EncodeToString(make([]byte, 48))
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
}

func TestSubmitBuilderPreferences_LatestOverwrites(t *testing.T) {
	gfv := phase0.Version{}
	blsSigner, err := signer.NewBLSSigner(testValidatorPrivkey)
	require.NoError(t, err)

	cfg := &config.BuilderAPIConfig{BuilderURL: testBuilderURL}
	srv := NewServer(cfg, logrus.New(), nil, nil, nil, gfv, phase0.Version{}, phase0.Root{})
	srv.SetEnabled(true)
	pk := blsSigner.PublicKey()
	url := "/eth/v1/builder/builder_preferences/0x" + hex.EncodeToString(pk[:])

	for _, v := range []phase0.Gwei{100, 250} {
		body := signBuilderPrefsRequest(t, blsSigner, testBuilderURL, 100, v, gfv)
		req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		require.Equal(t, http.StatusAccepted, rec.Code)
	}

	got, _ := srv.builderPrefsStore.Get(pk)
	assert.Equal(t, phase0.Gwei(250), got, "only the latest preference should be retained")
}

func TestSubmitBuilderPreferences_WrongBuilderURL(t *testing.T) {
	gfv := phase0.Version{}
	blsSigner, err := signer.NewBLSSigner(testValidatorPrivkey)
	require.NoError(t, err)

	cfg := &config.BuilderAPIConfig{BuilderURL: testBuilderURL}
	srv := NewServer(cfg, logrus.New(), nil, nil, nil, gfv, phase0.Version{}, phase0.Root{})
	srv.SetEnabled(true)

	// Validly signed, but for a different builder URL than this builder's.
	body := signBuilderPrefsRequest(t, blsSigner, "https://other-builder.example.com", 100, 5_000_000_000, gfv)
	pk := blsSigner.PublicKey()
	url := "/eth/v1/builder/builder_preferences/0x" + hex.EncodeToString(pk[:])

	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	_, ok := srv.builderPrefsStore.Get(pk)
	assert.False(t, ok, "preference must not be stored when builder_url does not match")
}

func TestSubmitBuilderPreferences_BadSignature(t *testing.T) {
	gfv := phase0.Version{}
	validator, err := signer.NewBLSSigner(testValidatorPrivkey)
	require.NoError(t, err)
	other, err := signer.NewBLSSigner(testOtherPrivkey)
	require.NoError(t, err)

	cfg := &config.BuilderAPIConfig{BuilderURL: testBuilderURL}
	srv := NewServer(cfg, logrus.New(), nil, nil, nil, gfv, phase0.Version{}, phase0.Root{})
	srv.SetEnabled(true)

	// Signed by `other` (correct builder URL), but submitted under `validator`'s pubkey.
	body := signBuilderPrefsRequest(t, other, testBuilderURL, 100, 5_000_000_000, gfv)
	pk := validator.PublicKey()
	url := "/eth/v1/builder/builder_preferences/0x" + hex.EncodeToString(pk[:])

	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	_, ok := srv.builderPrefsStore.Get(pk)
	assert.False(t, ok, "preference must not be stored when signature verification fails")
}

func TestSubmitBuilderPreferences_NoBuilderURLConfigured(t *testing.T) {
	gfv := phase0.Version{}
	blsSigner, err := signer.NewBLSSigner(testValidatorPrivkey)
	require.NoError(t, err)

	cfg := &config.BuilderAPIConfig{} // BuilderURL empty
	srv := NewServer(cfg, logrus.New(), nil, nil, nil, gfv, phase0.Version{}, phase0.Root{})
	srv.SetEnabled(true)

	body := signBuilderPrefsRequest(t, blsSigner, testBuilderURL, 100, 5_000_000_000, gfv)
	pk := blsSigner.PublicKey()
	url := "/eth/v1/builder/builder_preferences/0x" + hex.EncodeToString(pk[:])

	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestSubmitBuilderPreferences_InvalidJSON(t *testing.T) {
	cfg := &config.BuilderAPIConfig{BuilderURL: testBuilderURL}
	srv := NewServer(cfg, logrus.New(), nil, nil, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})
	srv.SetEnabled(true)

	url := "/eth/v1/builder/builder_preferences/0x" + hex.EncodeToString(make([]byte, 48))
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSubmitBuilderPreferences_MissingContentType(t *testing.T) {
	cfg := &config.BuilderAPIConfig{BuilderURL: testBuilderURL}
	srv := NewServer(cfg, logrus.New(), nil, nil, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})
	srv.SetEnabled(true)

	url := "/eth/v1/builder/builder_preferences/0x" + hex.EncodeToString(make([]byte, 48))
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader([]byte("{}")))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
}

func TestSubmitBuilderPreferences_Disabled(t *testing.T) {
	cfg := &config.BuilderAPIConfig{BuilderURL: testBuilderURL}
	srv := NewServer(cfg, logrus.New(), nil, nil, nil, phase0.Version{}, phase0.Version{}, phase0.Root{})
	// not enabled

	url := "/eth/v1/builder/builder_preferences/0x" + hex.EncodeToString(make([]byte, 48))
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
