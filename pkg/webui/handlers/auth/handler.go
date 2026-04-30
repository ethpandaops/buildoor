// Package auth bridges the buildoor web UI to a remote authenticatoor
// service. When --auth-provider-url is configured, tokens are validated
// against that service's JWKS. When it's not set, the API runs open —
// buildoor is typically deployed in a restricted environment, so this is
// a deliberate choice for the operator.
package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ethpandaops/service-authenticatoor/pkg/auth"
)

// AuthHandler validates incoming bearer tokens. When verifier is nil the
// API is treated as open (no authentication required); CheckAuthToken
// always returns a non-nil token.
type AuthHandler struct {
	verifier auth.Verifier // nil → open mode
}

// NewAuthHandler returns a handler. When authProviderURL is empty the
// returned handler operates in open mode (no token verification, all
// calls allowed). When set, it bootstraps a JWKS verifier from the
// service's OIDC discovery doc, falling back to <url>/jwks.json.
func NewAuthHandler(ctx context.Context, authProviderURL string) (*AuthHandler, error) {
	authProviderURL = strings.TrimRight(authProviderURL, "/")
	if authProviderURL == "" {
		return &AuthHandler{}, nil
	}

	expectedIssuer := authProviderURL
	jwksURL := authProviderURL + "/jwks.json"
	if disc, err := auth.FetchDiscovery(ctx, http.DefaultClient, authProviderURL); err == nil {
		expectedIssuer = disc.Issuer
		jwksURL = disc.JWKSURI
	}

	verifier, err := auth.NewJWKSVerifier(ctx, auth.VerifierConfig{
		JWKSURL:          jwksURL,
		ExpectedIssuer:   expectedIssuer,
		ExpectedAudience: parentZone(authProviderURL),
	})
	if err != nil {
		return nil, fmt.Errorf("auth: build verifier: %w", err)
	}

	return &AuthHandler{verifier: verifier}, nil
}

// IsOpen reports whether this handler is running in open mode (no auth
// provider configured).
func (h *AuthHandler) IsOpen() bool {
	return h.verifier == nil
}

// parentZone returns the parent DNS zone of a URL's host, used as the
// default expected audience: "https://auth.foo.example" → "foo.example".
func parentZone(rawURL string) string {
	for _, p := range []string{"https://", "http://"} {
		if strings.HasPrefix(rawURL, p) {
			rawURL = rawURL[len(p):]
			break
		}
	}
	if i := strings.IndexByte(rawURL, '/'); i >= 0 {
		rawURL = rawURL[:i]
	}
	if i := strings.IndexByte(rawURL, ':'); i >= 0 {
		rawURL = rawURL[:i]
	}
	if i := strings.IndexByte(rawURL, '.'); i > 0 {
		return rawURL[i+1:]
	}
	return rawURL
}
