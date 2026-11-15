package auth

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/webui/handlers/auth/authtest"
)

// requireStatus runs Require for one feature and returns the status it
// answered with (200 when the request may proceed) and the error body.
func requireStatus(t *testing.T, handler *AuthHandler, header, feature string) (int, string) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/anything", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}

	rec := httptest.NewRecorder()

	token, ok := handler.Require(rec, req, feature)
	if ok {
		require.NotNil(t, token)
		require.Zero(t, rec.Body.Len(), "an allowed request must not be answered")

		return http.StatusOK, ""
	}

	require.Nil(t, token)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	return rec.Code, rec.Body.String()
}

func TestRequireFeature(t *testing.T) {
	provider := authtest.NewProvider(t)

	handler, err := NewAuthHandler(t.Context(), provider.URL(), "")
	require.NoError(t, err)
	require.Equal(t, DefaultServiceName, handler.Service())

	const (
		ok        = http.StatusOK
		forbidden = http.StatusForbidden
		denied    = http.StatusUnauthorized
	)

	// allowed lists the features a claim grants; every other one is 403.
	tests := []struct {
		name     string
		services string
		basic    int
		allowed  []string
	}{
		{name: "basic access only", services: "-all,+buildoor", basic: ok},
		{name: "one feature", services: "-all,+buildoor,+buildoor:plan", basic: ok, allowed: []string{FeaturePlan}},
		{name: "one feature without the bare directive", services: "-all,+buildoor:rpc", basic: ok, allowed: []string{FeatureRPC}},
		{name: "two features", services: "+buildoor:txpool:config", basic: ok, allowed: []string{FeatureTxPool, FeatureConfig}},
		{name: "every feature of the service", services: "+buildoor:all", basic: ok, allowed: Features},
		{name: "every feature of every service", services: "+all:all", basic: ok, allowed: Features},
		{
			name: "every feature but one", services: "+buildoor:all:-keys", basic: ok,
			allowed: []string{FeaturePlan, FeatureTxPool, FeatureConfig, FeatureRPC},
		},
		{name: "a wildcard", services: "+buildoor:k*", basic: ok, allowed: []string{FeatureKeys}},
		{name: "no services claim", services: "", basic: ok},
		{name: "every service, no feature", services: "+all", basic: ok},
		{name: "a feature of another service", services: "+spamoor:plan", basic: ok},
		{name: "another service only", services: "-all,+spamoor:all", basic: denied},
		{name: "the service denied after a grant", services: "+buildoor:all,-buildoor", basic: denied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := "Bearer " + provider.Token(tt.services)

			status, _ := requireStatus(t, handler, header, FeatureBasic)
			require.Equal(t, tt.basic, status, "basic access")

			for _, feature := range Features {
				want := forbidden

				switch {
				case tt.basic != ok:
					want = tt.basic
				case slices.Contains(tt.allowed, feature):
					want = ok
				}

				status, body := requireStatus(t, handler, header, feature)
				assert.Equal(t, want, status, "feature %s", feature)

				switch status {
				case forbidden:
					assert.JSONEq(t, `{"error":"forbidden: this login lacks the feature buildoor:`+feature+`"}`, body)
				case denied:
					assert.JSONEq(t, `{"error":"unauthorized"}`, body)
				}
			}
		})
	}

	t.Run("no token", func(t *testing.T) {
		for _, header := range []string{"", "Bearer not-a-token"} {
			for _, feature := range append([]string{FeatureBasic}, Features...) {
				status, body := requireStatus(t, handler, header, feature)
				assert.Equal(t, denied, status, "feature %s", feature)
				assert.JSONEq(t, `{"error":"unauthorized"}`, body)
			}
		}
	})
}

func TestRequireFeatureUsesTheConfiguredServiceName(t *testing.T) {
	provider := authtest.NewProvider(t)

	handler, err := NewAuthHandler(t.Context(), provider.URL(), "buildoor-2")
	require.NoError(t, err)

	status, body := requireStatus(t, handler, provider.Token("+buildoor:plan"), FeaturePlan)
	assert.Equal(t, http.StatusForbidden, status)
	assert.JSONEq(t, `{"error":"forbidden: this login lacks the feature buildoor-2:plan"}`, body)

	status, _ = requireStatus(t, handler, provider.Token("+buildoor-2:plan"), FeaturePlan)
	assert.Equal(t, http.StatusOK, status)

	status, _ = requireStatus(t, handler, provider.Token("+buildoor-2:plan"), FeatureKeys)
	assert.Equal(t, http.StatusForbidden, status)
}

func TestRequireFeatureInOpenMode(t *testing.T) {
	handler, err := NewAuthHandler(t.Context(), "", "")
	require.NoError(t, err)

	for _, feature := range append([]string{FeatureBasic}, Features...) {
		status, _ := requireStatus(t, handler, "", feature)
		assert.Equal(t, http.StatusOK, status, "feature %s", feature)
	}
}

func TestAuthorizeReturnsTheTokenWithItsVerdict(t *testing.T) {
	provider := authtest.NewProvider(t)

	handler, err := NewAuthHandler(t.Context(), provider.URL(), "")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+provider.Token("+buildoor:plan"))

	token, status := handler.Authorize(req, FeatureKeys)
	require.Equal(t, http.StatusForbidden, status)
	require.NotNil(t, token)

	claims, ok := TokenClaims(token)
	require.True(t, ok)
	assert.Equal(t, authtest.Subject, claims.Subject)
	assert.True(t, handler.HasFeature(token, FeaturePlan))
	assert.False(t, handler.HasFeature(nil, FeatureBasic))

	token, status = handler.Authorize(httptest.NewRequest(http.MethodGet, "/", nil), FeatureBasic)
	require.Equal(t, http.StatusUnauthorized, status)
	require.Nil(t, token)
}
