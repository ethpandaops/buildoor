package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/sirupsen/logrus"
)

// SPAHandler serves a React single-page application.
// It serves static files when they exist, otherwise falls back to index.html
// for client-side routing.
type SPAHandler struct {
	logger      logrus.FieldLogger
	staticFS    http.FileSystem
	indexHTML   []byte
	contentType string
}

// RuntimeConfig is injected into the served index.html as a nested global
// `window.ethpandaops.buildoor.config` so the SPA can read it synchronously
// at boot — no extra round-trip to the backend.
type RuntimeConfig struct {
	AuthProviderURL string `json:"authProviderURL"`
}

// NewSPAHandler creates a new SPA handler. The runtimeConfig is encoded
// into a small <script> block injected into the <head> of index.html
// before any other script runs.
func NewSPAHandler(logger logrus.FieldLogger, staticEmbedFS fs.FS, runtimeConfig RuntimeConfig) (*SPAHandler, error) {
	subFS, err := fs.Sub(staticEmbedFS, "static")
	if err != nil {
		return nil, err
	}

	indexFile, err := subFS.Open("index.html")
	if err != nil {
		return nil, err
	}
	defer indexFile.Close()

	indexHTML, err := io.ReadAll(indexFile)
	if err != nil {
		return nil, err
	}

	indexHTML, err = injectRuntimeConfig(indexHTML, runtimeConfig)
	if err != nil {
		return nil, err
	}

	return &SPAHandler{
		logger:      logger,
		staticFS:    http.FS(subFS),
		indexHTML:   indexHTML,
		contentType: "text/html; charset=utf-8",
	}, nil
}

// injectRuntimeConfig inserts a small <script> block immediately before
// </head> that publishes runtime config under
// window.ethpandaops.buildoor.config. The JSON payload is HTML-safe
// (encoding/json escapes <, >, & by default), so values can't break out
// of the script tag.
func injectRuntimeConfig(html []byte, cfg RuntimeConfig) ([]byte, error) {
	payload, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal runtime config: %w", err)
	}
	tag := []byte(
		"<script>" +
			"window.ethpandaops=window.ethpandaops||{};" +
			"window.ethpandaops.buildoor=window.ethpandaops.buildoor||{};" +
			"window.ethpandaops.buildoor.config=" + string(payload) + ";" +
			"</script>",
	)

	const headClose = "</head>"
	idx := strings.Index(string(html), headClose)
	if idx < 0 {
		return append(tag, html...), nil
	}
	out := make([]byte, 0, len(html)+len(tag))
	out = append(out, html[:idx]...)
	out = append(out, tag...)
	out = append(out, html[idx:]...)
	return out, nil
}

// ServeHTTP implements http.Handler.
func (h *SPAHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	urlPath := path.Clean(r.URL.Path)
	if !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}

	if h.serveStaticFile(w, r, urlPath) {
		return
	}

	h.serveIndex(w)
}

// serveStaticFile attempts to serve a static file. Returns true if successful.
func (h *SPAHandler) serveStaticFile(w http.ResponseWriter, r *http.Request, urlPath string) bool {
	if urlPath == "/" || urlPath == "/index.html" {
		return false
	}

	f, err := h.staticFS.Open(urlPath)
	if err != nil {
		return false
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		return false
	}

	rs, ok := f.(io.ReadSeeker)
	if !ok {
		return false
	}

	http.ServeContent(w, r, urlPath, stat.ModTime(), rs)
	return true
}

// serveIndex serves the SPA index.html.
func (h *SPAHandler) serveIndex(w http.ResponseWriter) {
	w.Header().Set("Content-Type", h.contentType)
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write(h.indexHTML); err != nil {
		h.logger.WithError(err).Error("failed to write index.html")
	}
}
