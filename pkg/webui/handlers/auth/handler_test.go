package auth

import (
	"testing"

	"github.com/ethpandaops/buildoor/pkg/webui/handlers/auth/authtest"
)

func TestCheckAuthTokenEnforcesTheServicesGate(t *testing.T) {
	provider := authtest.NewProvider(t)

	handler, err := NewAuthHandler(t.Context(), provider.URL(), "")
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
			token := provider.Token(tt.services)

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
	handler, err := NewAuthHandler(t.Context(), "", "")
	if err != nil {
		t.Fatalf("NewAuthHandler: %v", err)
	}

	if !handler.IsOpen() || handler.CheckAuthToken("") == nil {
		t.Error("without an auth provider every request must pass")
	}
}

func TestCheckAuthTokenUsesTheConfiguredServiceName(t *testing.T) {
	provider := authtest.NewProvider(t)

	handler, err := NewAuthHandler(t.Context(), provider.URL(), " buildoor-2 ")
	if err != nil {
		t.Fatalf("NewAuthHandler: %v", err)
	}

	tests := []struct {
		name     string
		services string
		want     bool
	}{
		{name: "no services claim carries no constraint", services: "", want: true},
		{name: "the configured name", services: "-all,+buildoor-2", want: true},
		{name: "the default name only", services: "-all,+buildoor", want: false},
		{name: "everything but the configured name", services: "+all,-buildoor-2", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := handler.CheckAuthToken(provider.Token(tt.services)) != nil; got != tt.want {
				t.Errorf("CheckAuthToken accepted = %v, want %v (services %q)", got, tt.want, tt.services)
			}
		})
	}
}
