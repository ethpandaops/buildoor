package types

type FrontendConfig struct {
	Host     string
	Port     int
	SiteName string
	Debug    bool
	Pprof    bool
	Minify   bool

	// AuthProviderURL is the canonical URL of the remote authenticatoor
	// service. Tokens are validated against its JWKS, and the SPA picks
	// it up via /api/runtime-config to load <url>/client.js.
	AuthProviderURL string
}
