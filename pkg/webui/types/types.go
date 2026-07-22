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
	// it up via /api/runtime-config to load <url>/client-v2.js (the
	// shared-session client with automatic token refresh).
	AuthProviderURL string

	// InjectHeadHTML is a raw HTML snippet (typically <script>/<meta>/<link>
	// tags) injected into <head> of the served SPA after the runtime-config
	// script. Set per deployment; commonly used for the global ethPandaOps
	// panda menu loader or analytics. When empty, the BUILDOOR_INJECT_HEAD_HTML
	// env var is consulted as a fallback.
	InjectHeadHTML string

	// OverviewURL points at the multi-instance overview UI. When non-empty
	// it is published to the SPA via window.ethpandaops.buildoor.config so
	// the dashboard can render a top-nav "Overview" link back to the shared
	// landing page.
	OverviewURL string
}
