package builderapi

import (
	"encoding/json"
	"testing"

	buildergloas "github.com/attestantio/go-builder-client/api/gloas"
	// attphase0 is the attestantio phase0 used by go-builder-client's builder-API
	// types; imported here ONLY to populate buildergloas struct fields in tests.
	attphase0 "github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleSignedRequestAuth() *buildergloas.SignedRequestAuthV1 {
	var sig attphase0.BLSSignature
	sig[0] = 0xaa
	return &buildergloas.SignedRequestAuthV1{
		Message:   &buildergloas.RequestAuthV1{Data: []byte("https://builder.example.com"), Slot: 42},
		Signature: sig,
	}
}

func TestParseSignedRequestAuth_JSONAndSSZRoundTrip(t *testing.T) {
	orig := sampleSignedRequestAuth()

	jsonBody, err := json.Marshal(orig)
	require.NoError(t, err)
	sszBody, err := orig.MarshalSSZ()
	require.NoError(t, err)

	fromJSON, err := parseSignedRequestAuth(jsonBody, "application/json")
	require.NoError(t, err)
	fromSSZ, err := parseSignedRequestAuth(sszBody, "application/octet-stream")
	require.NoError(t, err)

	// Content-Type parameters are tolerated.
	fromJSONParam, err := parseSignedRequestAuth(jsonBody, "application/json; charset=utf-8")
	require.NoError(t, err)

	for _, got := range []*buildergloas.SignedRequestAuthV1{fromJSON, fromSSZ, fromJSONParam} {
		require.NotNil(t, got.Message)
		assert.Equal(t, orig.Message.Slot, got.Message.Slot)
		assert.Equal(t, orig.Message.Data, got.Message.Data)
		assert.Equal(t, orig.Signature, got.Signature)
	}
}

func TestParseSignedRequestAuth_Errors(t *testing.T) {
	ssz, err := sampleSignedRequestAuth().MarshalSSZ()
	require.NoError(t, err)

	// Unknown / empty Content-Type -> errUnsupportedContentType.
	_, err = parseSignedRequestAuth(ssz, "text/plain")
	require.ErrorIs(t, err, errUnsupportedContentType)
	_, err = parseSignedRequestAuth(ssz, "")
	require.ErrorIs(t, err, errUnsupportedContentType)

	// Malformed bodies -> decode error (not the content-type sentinel).
	_, err = parseSignedRequestAuth([]byte("not json"), "application/json")
	require.Error(t, err)
	assert.NotErrorIs(t, err, errUnsupportedContentType)
	_, err = parseSignedRequestAuth([]byte{0x01}, "application/octet-stream")
	require.Error(t, err)
	assert.NotErrorIs(t, err, errUnsupportedContentType)
}

func TestParseBuilderPreferencesRequest_SSZRoundTrip(t *testing.T) {
	orig := &buildergloas.BuilderPreferencesRequestV1{
		Preferences: &buildergloas.BuilderPreferencesV1{MaxExecutionPayment: 5_000_000_000},
		Auth:        sampleSignedRequestAuth(),
	}

	ssz, err := orig.MarshalSSZ()
	require.NoError(t, err)

	got, err := parseBuilderPreferencesRequest(ssz, "application/octet-stream")
	require.NoError(t, err)
	require.NotNil(t, got.Preferences)
	assert.Equal(t, orig.Preferences.MaxExecutionPayment, got.Preferences.MaxExecutionPayment)
	require.NotNil(t, got.Auth)
	assert.Equal(t, orig.Auth.Message.Slot, got.Auth.Message.Slot)

	_, err = parseBuilderPreferencesRequest(ssz, "")
	require.ErrorIs(t, err, errUnsupportedContentType)
}
