package builderapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	apiv1 "github.com/attestantio/go-eth2-client/api/v1"
	"github.com/attestantio/go-eth2-client/spec/bellatrix"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/builderapi/validators"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

func TestRegisterValidators_BuilderSpecsExample(t *testing.T) {
	// Uses the official builder-specs example from validators/testdata/signed_validator_registrations.json
	cfg := &config.BuilderAPIConfig{Port: 0}
	log := logrus.New()
	srv := NewServer(cfg, log, nil)

	req := httptest.NewRequest(http.MethodPost, "/eth/v1/builder/validators", bytes.NewReader(validators.BuilderSpecsExampleJSON))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	// Builder-specs example uses a placeholder signature; verification fails → 400.
	assert.Equal(t, http.StatusBadRequest, rec.Code, "POST /eth/v1/builder/validators with builder-specs example (invalid sig) should return 400")
}

func TestRegisterValidators_EmptyArray(t *testing.T) {
	cfg := &config.BuilderAPIConfig{Port: 0}
	log := logrus.New()
	srv := NewServer(cfg, log, nil)

	req := httptest.NewRequest(http.MethodPost, "/eth/v1/builder/validators", bytes.NewReader([]byte("[]")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 0, srv.validatorsStore.Len())
}

func TestRegisterValidators_InvalidJSON(t *testing.T) {
	cfg := &config.BuilderAPIConfig{Port: 0}
	log := logrus.New()
	srv := NewServer(cfg, log, nil)

	req := httptest.NewRequest(http.MethodPost, "/eth/v1/builder/validators", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRegisterValidators_ValidSignature(t *testing.T) {
	// Use a test BLS key to create a valid signed registration.
	// Generated minimal key that herumi accepts (all zeros except last byte for valid scalar).
	testPrivkey := "0x0000000000000000000000000000000000000000000000000000000000000001"
	blsSigner, err := signer.NewBLSSigner(testPrivkey)
	require.NoError(t, err)

	var feeRecipient bellatrix.ExecutionAddress
	for i := range feeRecipient {
		feeRecipient[i] = byte(i)
	}
	msg := &apiv1.ValidatorRegistration{
		FeeRecipient: feeRecipient,
		GasLimit:     30_000_000,
		Timestamp:    time.Unix(100, 0),
		Pubkey:       blsSigner.PublicKey(),
	}

	messageRoot, err := msg.HashTreeRoot()
	require.NoError(t, err)
	var root phase0.Root
	copy(root[:], messageRoot[:])

	var zeroVersion phase0.Version
	var zeroRoot phase0.Root
	domain := signer.ComputeDomain(signer.DomainApplicationBuilder, zeroVersion, zeroRoot)
	signingRoot := signer.ComputeSigningRoot(root, domain)
	sig, err := blsSigner.Sign(signingRoot[:])
	require.NoError(t, err)

	reg := &apiv1.SignedValidatorRegistration{
		Message:   msg,
		Signature: sig,
	}
	require.True(t, validators.VerifyRegistration(reg), "test registration must verify")

	body, err := json.Marshal([]*apiv1.SignedValidatorRegistration{reg})
	require.NoError(t, err)

	cfg := &config.BuilderAPIConfig{Port: 0}
	log := logrus.New()
	srv := NewServer(cfg, log, nil)

	req := httptest.NewRequest(http.MethodPost, "/eth/v1/builder/validators", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, srv.validatorsStore.Len())
	stored := srv.validatorsStore.Get(blsSigner.PublicKey())
	require.NotNil(t, stored)
	assert.Equal(t, msg.GasLimit, stored.Message.GasLimit)
}

func TestRegisterValidators_MissingContentType(t *testing.T) {
	cfg := &config.BuilderAPIConfig{Port: 0}
	log := logrus.New()
	srv := NewServer(cfg, log, nil)

	req := httptest.NewRequest(http.MethodPost, "/eth/v1/builder/validators", bytes.NewReader([]byte("[]")))
	// no Content-Type
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
}
