package handlers

import (
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

// NewSPAHandler creates a new SPA handler.
func NewSPAHandler(logger logrus.FieldLogger, staticEmbedFS fs.FS) (*SPAHandler, error) {
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

	return &SPAHandler{
		logger:      logger,
		staticFS:    http.FS(subFS),
		indexHTML:   indexHTML,
		contentType: "text/html; charset=utf-8",
	}, nil
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
