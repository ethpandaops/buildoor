// Package builderapi implements the traditional Builder API (pre-ePBS) for proposers.
// This follows the Ethereum Builder API specification from https://github.com/ethereum/builder-specs
package builderapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/config"
)

// Server implements the Builder API HTTP server.
type Server struct {
	cfg    *config.BuilderAPIConfig
	log    *logrus.Logger
	server *http.Server
	router *mux.Router
}

// NewServer creates a new Builder API server.
func NewServer(cfg *config.BuilderAPIConfig, log *logrus.Logger) *Server {
	s := &Server{
		cfg:    cfg,
		log:    log,
		router: mux.NewRouter(),
	}

	s.registerRoutes()

	return s
}

// registerRoutes sets up all the Builder API routes.
func (s *Server) registerRoutes() {
	// Status endpoint - GET /eth/v1/builder/status
	// https://github.com/ethereum/builder-specs/blob/main/apis/builder/status.yaml
	s.router.HandleFunc("/eth/v1/builder/status", s.handleStatus).Methods(http.MethodGet)
}

// handleStatus handles GET /eth/v1/builder/status
// Returns 200 OK if the builder is ready to accept requests.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// Start starts the Builder API HTTP server.
func (s *Server) Start(ctx context.Context) error {
	addr := fmt.Sprintf("0.0.0.0:%d", s.cfg.Port)

	s.server = &http.Server{
		Addr:         addr,
		Handler:      s.router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	s.log.WithField("addr", addr).Info("Starting Builder API server")

	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.WithError(err).Error("Builder API server error")
		}
	}()

	return nil
}

// Stop gracefully shuts down the Builder API server.
func (s *Server) Stop() error {
	if s.server == nil {
		return nil
	}

	s.log.Info("Stopping Builder API server")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return s.server.Shutdown(ctx)
}
