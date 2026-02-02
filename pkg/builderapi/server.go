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

	apiv1 "github.com/attestantio/go-eth2-client/api/v1"
	apiv1electra "github.com/attestantio/go-eth2-client/api/v1/electra"
	apiv1fulu "github.com/attestantio/go-eth2-client/api/v1/fulu"
	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/builderapi/fulu"
	"github.com/ethpandaops/buildoor/pkg/builderapi/validators"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// PayloadCacheProvider provides access to the payload cache (e.g. *builder.Service).
// Used so tests can inject a mock without full builder deps.
type PayloadCacheProvider interface {
	GetPayloadCache() *builder.PayloadCache
}

// FuluBlockPublisher submits unblinded Fulu block contents to the beacon node.
// Implemented by *beacon.Client in production.
type FuluBlockPublisher interface {
	SubmitFuluBlock(ctx context.Context, contents *apiv1fulu.SignedBlockContents) error
}

// Server implements the combined Builder API + Buildoor API HTTP server.
type Server struct {
	cfg             *config.BuilderAPIConfig
	log             *logrus.Logger
	server          *http.Server
	router          *mux.Router
	builderSvc      PayloadCacheProvider // optional: for buildoor debug APIs and Fulu getHeader/submitBlindedBlockV2
	validatorsStore *validators.Store    // in-memory validator registrations
	blsSigner       *signer.BLSSigner    // optional: for signing Fulu builder bids (getHeader)
	fuluPublisher   FuluBlockPublisher   // optional: for publishing unblinded blocks (submitBlindedBlockV2)
}

// NewServer creates a new server. builderSvc may be nil; if set, buildoor-specific
// endpoints and Fulu getHeader/submitBlindedBlockV2 will be enabled. blsSigner may be nil;
// if set, getHeader will sign builder bids. fuluPublisher may be set later via SetFuluPublisher.
// validatorStore is optional; when provided it is shared with the builder service for fee recipient lookup.
func NewServer(cfg *config.BuilderAPIConfig, log *logrus.Logger, builderSvc PayloadCacheProvider, blsSigner *signer.BLSSigner, validatorStore *validators.Store) *Server {
	store := validatorStore
	if store == nil {
		store = validators.NewStore()
	}
	s := &Server{
		cfg:             cfg,
		log:             log,
		router:          mux.NewRouter(),
		builderSvc:      builderSvc,
		validatorsStore: store,
		blsSigner:       blsSigner,
	}

	s.registerRoutes()

	return s
}

// SetFuluPublisher sets the optional publisher for unblinded Fulu blocks (e.g. beacon node client).
func (s *Server) SetFuluPublisher(p FuluBlockPublisher) {
	s.fuluPublisher = p
}

// Handler returns the HTTP handler for tests.
func (s *Server) Handler() http.Handler {
	return s.router
}

// registerRoutes sets up Builder API and Buildoor API routes.
func (s *Server) registerRoutes() {
	// --- Builder API (standard spec) ---
	// https://github.com/ethereum/builder-specs
	builderAPI := s.router.PathPrefix("/eth/v1/builder").Subrouter()
	builderAPI.HandleFunc("/status", s.handleBuilderStatus).Methods(http.MethodGet)
	builderAPI.HandleFunc("/validators", s.handleRegisterValidators).Methods(http.MethodPost)
	builderAPI.HandleFunc("/header/{slot}/{parent_hash}/{pubkey}", s.handleGetHeader).Methods(http.MethodGet)

	// --- Builder API v2 (Fulu blinded blocks) ---
	builderAPIv2 := s.router.PathPrefix("/eth/v2/builder").Subrouter()
	builderAPIv2.HandleFunc("/blinded_blocks", s.handleSubmitBlindedBlockV2).Methods(http.MethodPost)

	// --- Buildoor API (debug / tooling) ---
	buildoorAPI := s.router.PathPrefix("/buildoor/v1").Subrouter()
	buildoorAPI.HandleFunc("/payloads/{slot}", s.handleGetPayloadBySlot).Methods(http.MethodGet)
}

// handleBuilderStatus handles GET /eth/v1/builder/status
// Returns 200 OK if the builder is ready to accept requests.
func (s *Server) handleBuilderStatus(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// handleRegisterValidators handles POST /eth/v1/builder/validators.
// Accepts a JSON array of SignedValidatorRegistration, verifies each signature,
// and stores valid registrations. Returns 200 on success, 400 on validation failure.
func (s *Server) handleRegisterValidators(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/json" {
		writeValidatorError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}

	var regs []*apiv1.SignedValidatorRegistration
	if err := json.NewDecoder(r.Body).Decode(&regs); err != nil {
		writeValidatorError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	for _, reg := range regs {
		if reg == nil || reg.Message == nil {
			writeValidatorError(w, http.StatusBadRequest, "registration message missing")
			return
		}
		if !validators.VerifyRegistration(reg) {
			writeValidatorError(w, http.StatusBadRequest, "invalid signature for validator "+hex.EncodeToString(reg.Message.Pubkey[:]))
			return
		}
		s.validatorsStore.Put(reg)
	}

	w.WriteHeader(http.StatusOK)
}

func writeValidatorError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": code, "message": message})
}

// PayloadBySlotResponse is the JSON response for GET /buildoor/v1/payloads/{slot}.
type PayloadBySlotResponse struct {
	Slot            uint64          `json:"slot"`
	BlockHash       string          `json:"block_hash"`
	ParentBlockHash string          `json:"parent_block_hash"`
	ParentBlockRoot string          `json:"parent_block_root"`
	Payload         json.RawMessage `json:"payload"`
	BlobsBundle     json.RawMessage `json:"blobs_bundle,omitempty"`
	BlockValue      string          `json:"block_value"` // wei as string
	FeeRecipient    string          `json:"fee_recipient"`
	GasLimit        uint64          `json:"gas_limit"`
	Timestamp       uint64          `json:"timestamp"`
	BuildSource     string          `json:"build_source"`
	ReadyAt         time.Time       `json:"ready_at"`
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

	marshalledPayload, err := event.Payload.MarshalJSON()
	if err != nil {
		return
	}

	marshalledBlobsBundle, err := event.BlobsBundle.MarshalJSON()
	if err != nil {
		return
	}

	resp := PayloadBySlotResponse{
		Slot:            uint64(event.Slot),
		BlockHash:       "0x" + hex.EncodeToString(event.BlockHash[:]),
		ParentBlockHash: "0x" + hex.EncodeToString(event.ParentBlockHash[:]),
		ParentBlockRoot: "0x" + hex.EncodeToString(event.ParentBlockRoot[:]),
		Payload:         marshalledPayload,
		BlobsBundle:     marshalledBlobsBundle,
		BlockValue:      fmt.Sprintf("%d", event.BlockValue),
		FeeRecipient:    event.FeeRecipient.Hex(),
		GasLimit:        event.GasLimit,
		Timestamp:       event.Timestamp,
		BuildSource:     event.BuildSource.String(),
		ReadyAt:         event.ReadyAt,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// handleGetHeader handles GET /eth/v1/builder/header/{slot}/{parent_hash}/{pubkey}.
// Returns 200 with Fulu SignedBuilderBid, or 204 if no bid, or 400 on invalid params / unregistered proposer.
func (s *Server) handleGetHeader(w http.ResponseWriter, r *http.Request) {
	if s.builderSvc == nil || s.blsSigner == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	vars := mux.Vars(r)
	slotStr := vars["slot"]
	parentHashStr := vars["parent_hash"]
	pubkeyStr := vars["pubkey"]

	slotU64, err := strconv.ParseUint(slotStr, 10, 64)
	if err != nil {
		writeValidatorError(w, http.StatusBadRequest, "invalid slot: must be a number")
		return
	}

	parentHashBytes, err := hex.DecodeString(trimHex(parentHashStr))
	if err != nil || len(parentHashBytes) != 32 {
		writeValidatorError(w, http.StatusBadRequest, "invalid parent_hash: must be 32 bytes hex")
		return
	}
	var parentHash phase0.Hash32
	copy(parentHash[:], parentHashBytes)

	pubkeyBytes, err := hex.DecodeString(trimHex(pubkeyStr))
	if err != nil || len(pubkeyBytes) != 48 {
		writeValidatorError(w, http.StatusBadRequest, "invalid pubkey: must be 48 bytes hex")
		return
	}
	var pubkey phase0.BLSPubKey
	copy(pubkey[:], pubkeyBytes)

	if s.validatorsStore.Get(pubkey) == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	cache := s.builderSvc.GetPayloadCache()
	event := cache.Get(phase0.Slot(slotU64))
	if event == nil || event.ParentBlockHash != parentHash {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	subsidyGwei := uint64(0)
	if s.cfg != nil {
		subsidyGwei = s.cfg.BlockValueSubsidyGwei
	}
	signedBid, err := fulu.BuildSignedBuilderBid(event, pubkey, s.blsSigner, subsidyGwei)
	if err != nil {
		s.log.WithError(err).Warn("Failed to build Fulu SignedBuilderBid")
		writeValidatorError(w, http.StatusInternalServerError, "failed to build bid")
		return
	}

	resp := fulu.GetHeaderResponse{
		Version: "fulu",
		Data:    signedBid,
	}

	s.log.Infof("Delivered header for slot %d, block hash %s, parent hash %s, pubkey %s", slotU64, "0x"+hex.EncodeToString(event.BlockHash[:]), "0x"+hex.EncodeToString(parentHash[:]), "0x"+hex.EncodeToString(pubkey[:]))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Eth-Consensus-Version", "fulu")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func trimHex(s string) string {
	if len(s) >= 2 && (s[0:2] == "0x" || s[0:2] == "0X") {
		return s[2:]
	}
	return s
}

// handleSubmitBlindedBlockV2 handles POST /eth/v2/builder/blinded_blocks (Fulu SignedBlindedBeaconBlock).
// Returns 202 Accepted on success, 400 on validation/match failure, 415 on wrong Content-Type.
func (s *Server) handleSubmitBlindedBlockV2(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Content-Type") != "application/json" {
		writeValidatorError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}

	if s.builderSvc == nil {
		writeValidatorError(w, http.StatusBadRequest, "builder not available")
		return
	}

	var blinded apiv1electra.SignedBlindedBeaconBlock
	if err := json.NewDecoder(r.Body).Decode(&blinded); err != nil {
		writeValidatorError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if blinded.Message == nil || blinded.Message.Body == nil ||
		blinded.Message.Body.ExecutionPayloadHeader == nil {
		writeValidatorError(w, http.StatusBadRequest, "invalid blinded block: missing message or execution_payload_header")
		return
	}

	blockHash := blinded.Message.Body.ExecutionPayloadHeader.BlockHash
	cache := s.builderSvc.GetPayloadCache()
	event := cache.GetByBlockHash(blockHash)
	if event == nil {
		writeValidatorError(w, http.StatusBadRequest, "no matching payload for block hash")
		return
	}

	contents, err := fulu.UnblindSignedBlindedBeaconBlock(&blinded, event)
	if err != nil {
		writeValidatorError(w, http.StatusBadRequest, "unblind failed: "+err.Error())
		return
	}
	if contents == nil {
		writeValidatorError(w, http.StatusBadRequest, "unblind produced no contents")
		return
	}

	s.log.Infof("Unblinded Fulu block for slot %d, block hash %s", contents.SignedBlock.Message.Slot, "0x"+hex.EncodeToString(blockHash[:]))

	if s.fuluPublisher != nil {
		if err := s.fuluPublisher.SubmitFuluBlock(r.Context(), contents); err != nil {
			s.log.WithError(err).Error("Failed to publish unblinded Fulu block")
			writeValidatorError(w, http.StatusInternalServerError, "failed to publish block: "+err.Error())
			return
		}
	}

	s.log.Infof("Submitted unblinded Fulu block for slot %d, block hash %s", contents.SignedBlock.Message.Slot, "0x"+hex.EncodeToString(blockHash[:]))

	w.WriteHeader(http.StatusAccepted)
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
