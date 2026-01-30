// Package builderapi implements the HTTP server that serves both:
// - The traditional Builder API (pre-ePBS) for proposers: /eth/v1/builder/*
// - Buildoor-specific APIs for debugging and tooling: /buildoor/v1/*
//
// Builder API follows https://github.com/ethereum/builder-specs
package builderapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/config"
)

// Server implements the combined Builder API + Buildoor API HTTP server.
type Server struct {
	cfg        *config.BuilderAPIConfig
	log        *logrus.Logger
	server     *http.Server
	router     *mux.Router
	builderSvc *builder.Service // optional: for buildoor debug APIs (e.g. payload cache)
}

// NewServer creates a new server. builderSvc may be nil; if set, buildoor-specific
// endpoints (e.g. payload by slot) will be enabled.
func NewServer(cfg *config.BuilderAPIConfig, log *logrus.Logger, builderSvc *builder.Service) *Server {
	s := &Server{
		cfg:        cfg,
		log:        log,
		router:     mux.NewRouter(),
		builderSvc: builderSvc,
	}

	s.registerRoutes()

	return s
}

// registerRoutes sets up Builder API and Buildoor API routes.
func (s *Server) registerRoutes() {
	// --- Builder API (standard spec) ---
	// https://github.com/ethereum/builder-specs
	builderAPI := s.router.PathPrefix("/eth/v1/builder").Subrouter()
	builderAPI.HandleFunc("/status", s.handleBuilderStatus).Methods(http.MethodGet)

	// --- Buildoor API (debug / tooling) ---
	buildoorAPI := s.router.PathPrefix("/buildoor/v1").Subrouter()
	buildoorAPI.HandleFunc("/payloads/{slot}", s.handleGetPayloadBySlot).Methods(http.MethodGet)
}

// handleBuilderStatus handles GET /eth/v1/builder/status
// Returns 200 OK if the builder is ready to accept requests.
func (s *Server) handleBuilderStatus(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// PayloadBySlotResponse is the JSON response for GET /buildoor/v1/payloads/{slot}.
type PayloadBySlotResponse struct {
	Slot              uint64          `json:"slot"`
	BlockHash         string          `json:"block_hash"`
	ParentBlockHash   string          `json:"parent_block_hash"`
	ParentBlockRoot   string          `json:"parent_block_root"`
	Payload           json.RawMessage `json:"payload"`
	BlobsBundle       json.RawMessage `json:"blobs_bundle,omitempty"`
	ExecutionRequests json.RawMessage `json:"execution_requests,omitempty"`
	BlockValue        string          `json:"block_value"` // wei as string
	FeeRecipient      string          `json:"fee_recipient"`
	GasLimit          uint64          `json:"gas_limit"`
	Timestamp         uint64          `json:"timestamp"`
	BuildSource       string          `json:"build_source"`
	ReadyAt           time.Time       `json:"ready_at"`
}

// handleGetPayloadBySlot handles GET /buildoor/v1/payloads/{slot}.
// Returns the cached execution payload for the given slot, or 404 if not found.
func (s *Server) handleGetPayloadBySlot(w http.ResponseWriter, r *http.Request) {
	if s.builderSvc == nil {
		http.Error(w, "buildoor payload API not available", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	slotStr, ok := vars["slot"]
	if !ok {
		http.Error(w, "missing slot", http.StatusBadRequest)
		return
	}

	slotU64, err := strconv.ParseUint(slotStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid slot: must be a number", http.StatusBadRequest)
		return
	}

	cache := s.builderSvc.GetPayloadCache()
	event := cache.Get(phase0.Slot(slotU64))
	if event == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "payload not found for slot"})
		return
	}

	resp := PayloadBySlotResponse{
		Slot:              uint64(event.Slot),
		BlockHash:         "0x" + hex.EncodeToString(event.BlockHash[:]),
		ParentBlockHash:   "0x" + hex.EncodeToString(event.ParentBlockHash[:]),
		ParentBlockRoot:   "0x" + hex.EncodeToString(event.ParentBlockRoot[:]),
		Payload:           event.Payload,
		BlobsBundle:       event.BlobsBundle,
		ExecutionRequests: event.ExecutionRequests,
		BlockValue:        fmt.Sprintf("%d", event.BlockValue),
		FeeRecipient:      event.FeeRecipient.Hex(),
		GasLimit:          event.GasLimit,
		Timestamp:         event.Timestamp,
		BuildSource:       event.BuildSource.String(),
		ReadyAt:           event.ReadyAt,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
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
