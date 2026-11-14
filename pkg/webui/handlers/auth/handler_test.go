package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethpandaops/service-authenticatoor/pkg/issuer"
)

// authProvider is a stand-in authenticatoor: discovery document and JWKS
// for one signing key, plus tokens minted with a chosen services claim.
type authProvider struct {
	t      *testing.T
	server *httptest.Server
	key    *rsa.PrivateKey
}

func newAuthProvider(t *testing.T) *authProvider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	p := &authProvider{t: t, key: key}

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

func (p *authProvider) issuer(services string) *issuer.RS256Issuer {
	p.t.Helper()

	iss, err := issuer.NewRS256Issuer(issuer.Config{
		ActiveKey:       p.key,
		Issuer:          p.server.URL,
		DefaultAudience: []string{parentZone(p.server.URL)},
		Services:        services,
		TTL:             time.Minute,
	})
	if err != nil {
		p.t.Fatalf("issuer: %v", err)
	}

	return iss
}

// token mints a token whose "services" claim is the given directive list.
func (p *authProvider) token(services string) string {
	p.t.Helper()

	token, _, err := p.issuer(services).Issue("dev@example.org", "dev@example.org", nil)
	if err != nil {
		p.t.Fatalf("issue: %v", err)
	}

	return token
}

func TestCheckAuthTokenEnforcesTheServicesGate(t *testing.T) {
	provider := newAuthProvider(t)

	handler, err := NewAuthHandler(t.Context(), provider.server.URL)
	if err != nil {
		t.Fatalf("NewAuthHandler: %v", err)
	}

	if handler.IsOpen() {
		t.Fatal("handler is open although an auth provider is configured")
	}

	tests := []struct {
		name     string
		services string
		want     bool
	}{
		{name: "no services claim carries no constraint", services: "", want: true},
		{name: "every service", services: "+all", want: true},
		{name: "buildoor only", services: "-all,+buildoor", want: true},
		{name: "everything but buildoor", services: "+all,-buildoor", want: false},
		{name: "another tool only", services: "-all,+spamoor", want: false},
		{name: "nothing", services: "-all", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := provider.token(tt.services)

			for _, header := range []string{token, "Bearer " + token} {
				if got := handler.CheckAuthToken(header) != nil; got != tt.want {
					t.Errorf("CheckAuthToken accepted = %v, want %v (services %q)", got, tt.want, tt.services)
				}
			}
		})
	}

	if handler.CheckAuthToken("Bearer not-a-token") != nil {
		t.Error("a malformed token was accepted")
	}
}

func TestOpenModeAcceptsEverything(t *testing.T) {
	handler, err := NewAuthHandler(t.Context(), "")
	if err != nil {
		t.Fatalf("NewAuthHandler: %v", err)
	}

	if !handler.IsOpen() || handler.CheckAuthToken("") == nil {
		t.Error("without an auth provider every request must pass")
	}
}
