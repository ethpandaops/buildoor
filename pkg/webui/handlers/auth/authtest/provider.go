// Package authtest provides a stand-in authenticatoor for tests: a local
// discovery document and JWKS, and tokens minted with a chosen "services"
// claim, so a test verifies real tokens the way a deployment does.
package authtest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ethpandaops/service-authenticatoor/pkg/issuer"
)

// Subject is the subject and email of the tokens Token mints.
const Subject = "dev@example.org"

// Provider is a stand-in authenticatoor serving the discovery document and
// the JWKS of one signing key.
type Provider struct {
	t      *testing.T
	server *httptest.Server
	key    *rsa.PrivateKey
}

// NewProvider starts a provider that lives as long as the test.
func NewProvider(t *testing.T) *Provider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	p := &Provider{t: t, key: key}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   p.server.URL,
			"jwks_uri": p.server.URL + "/jwks.json",
		})
	})
	mux.HandleFunc("/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		jwks, jwksErr := p.issuer("").JWKS()
		if jwksErr != nil {
			http.Error(w, jwksErr.Error(), http.StatusInternalServerError)

			return
		}

		_, _ = w.Write(jwks)
	})

	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)

	return p
}

// URL is the provider's base URL: what --auth-provider-url is set to.
func (p *Provider) URL() string {
	return p.server.URL
}

// audience is the audience buildoor expects of this provider's tokens: the
// parent DNS zone of the provider's host.
func (p *Provider) audience() string {
	parsed, err := url.Parse(p.server.URL)
	if err != nil {
		p.t.Fatalf("provider url: %v", err)
	}

	host := parsed.Hostname()
	if i := strings.IndexByte(host, '.'); i > 0 {
		return host[i+1:]
	}

	return host
}

func (p *Provider) issuer(services string) *issuer.RS256Issuer {
	p.t.Helper()

	iss, err := issuer.NewRS256Issuer(issuer.Config{
		ActiveKey:       p.key,
		Issuer:          p.server.URL,
		DefaultAudience: []string{p.audience()},
		Services:        services,
		TTL:             time.Minute,
	})
	if err != nil {
		p.t.Fatalf("issuer: %v", err)
	}

	return iss
}

// Token mints a token for Subject whose "services" claim is the given
// directive list ("" = no claim).
func (p *Provider) Token(services string) string {
	p.t.Helper()

	token, _, err := p.issuer(services).Issue(Subject, Subject, nil)
	if err != nil {
		p.t.Fatalf("issue: %v", err)
	}

	return token
}
