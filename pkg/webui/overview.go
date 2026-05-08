package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"github.com/urfave/negroni"

	"github.com/ethpandaops/buildoor/pkg/webui/handlers"
)

// OverviewConfig configures the multi-instance overview HTTP server.
type OverviewConfig struct {
	Host           string   // bind host (defaults to 0.0.0.0)
	Port           int      // bind port
	Hosts          []string // buildoor instance URLs (one per builder)
	InjectHeadHTML string   // optional <head> HTML snippet (falls back to BUILDOOR_INJECT_HEAD_HTML env var)
}

// OverviewHostEntry is a single configured buildoor instance returned to the UI.
type OverviewHostEntry struct {
	ID    int    `json:"id"`
	URL   string `json:"url"`
	Label string `json:"label"`
}

// StartOverviewServer starts the overview HTTP server. It serves the overview
// SPA bundle and exposes a small proxy API the UI uses to poll each instance's
// /api/buildoor/overview endpoint (so the browser doesn't need CORS to all of them).
func StartOverviewServer(cfg *OverviewConfig, log logrus.FieldLogger) error {
	if len(cfg.Hosts) == 0 {
		return errors.New("at least one --host is required")
	}

	hosts := make([]OverviewHostEntry, 0, len(cfg.Hosts))
	for i, h := range cfg.Hosts {
		hosts = append(hosts, OverviewHostEntry{
			ID:    i,
			URL:   strings.TrimRight(h, "/"),
			Label: deriveOverviewLabel(h),
		})
	}

	router := mux.NewRouter()

	apiRouter := router.PathPrefix("/api/overview").Subrouter()
	apiRouter.HandleFunc("/hosts", overviewHostsHandler(hosts)).Methods(http.MethodGet)
	apiRouter.HandleFunc("/proxy/{idx}", overviewProxyHandler(hosts, log)).Methods(http.MethodGet)

	subFS, err := fs.Sub(staticEmbedFS, "static")
	if err != nil {
		return fmt.Errorf("static FS: %w", err)
	}

	headInjectHTML := cfg.InjectHeadHTML
	if headInjectHTML == "" {
		headInjectHTML = os.Getenv("BUILDOOR_INJECT_HEAD_HTML")
	}

	spaHandler, err := handlers.NewSPAHandlerWithIndex(
		log.WithField("module", "web-overview-spa"),
		subFS,
		"overview.html",
		handlers.RuntimeConfig{},
		headInjectHTML,
	)
	if err != nil {
		return fmt.Errorf("init overview SPA handler: %w", err)
	}
	router.PathPrefix("/").Handler(spaHandler)

	n := negroni.New()
	n.Use(negroni.NewRecovery())
	n.UseHandler(router)

	host := cfg.Host
	if host == "" {
		host = "0.0.0.0"
	}

	port := cfg.Port
	if port == 0 {
		port = 8090
	}

	srv := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", host, port),
		WriteTimeout: 0,
		ReadTimeout:  0,
		IdleTimeout:  120 * time.Second,
		Handler:      n,
	}

	log.WithField("addr", srv.Addr).WithField("hosts", len(hosts)).Info("Overview server listening")

	return srv.ListenAndServe()
}

// overviewHostsHandler returns the configured host list to the UI.
func overviewHostsHandler(hosts []OverviewHostEntry) http.HandlerFunc {
	payload, _ := json.Marshal(map[string]any{"hosts": hosts})

	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}
}

// overviewProxyHandler proxies GET /api/overview/proxy/{idx} to hosts[idx]/api/buildoor/overview.
// Keeps responses small and bounded by a per-request timeout. Surfaces upstream errors as 502.
func overviewProxyHandler(hosts []OverviewHostEntry, log logrus.FieldLogger) http.HandlerFunc {
	client := &http.Client{Timeout: 5 * time.Second}

	return func(w http.ResponseWriter, r *http.Request) {
		idxStr := mux.Vars(r)["idx"]

		idx, err := strconv.Atoi(idxStr)
		if err != nil || idx < 0 || idx >= len(hosts) {
			http.Error(w, "invalid host index", http.StatusBadRequest)
			return
		}

		target := hosts[idx].URL + "/api/buildoor/overview"

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		resp, err := client.Do(req)
		if err != nil {
			log.WithError(err).WithField("target", target).Debug("Overview proxy upstream error")
			writeProxyError(w, http.StatusBadGateway, err.Error())
			return
		}
		defer resp.Body.Close()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
}

func writeProxyError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	payload, _ := json.Marshal(map[string]string{"error": msg})
	_, _ = w.Write(payload)
}

// deriveOverviewLabel produces a short display label from a host URL by
// stripping the scheme. Falls back to the input if parsing yields nothing.
func deriveOverviewLabel(host string) string {
	s := strings.TrimSpace(host)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimRight(s, "/")

	if s == "" {
		return host
	}

	return s
}
