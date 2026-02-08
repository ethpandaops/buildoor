// Package builderapi implements the HTTP server that serves both:
// - The traditional Builder API (pre-ePBS) for proposers: /eth/v1/builder/*
// - Buildoor-specific APIs for debugging and tooling: /buildoor/v1/*
//
// Builder API follows https://github.com/ethereum/builder-specs
package builderapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
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

// EventBroadcaster provides methods for broadcasting Builder API events to the WebUI.
type EventBroadcaster interface {
	BroadcastBuilderAPIGetHeaderReceived(slot uint64, parentHash, pubkey string)
	BroadcastBuilderAPIGetHeaderDelivered(slot uint64, blockHash, blockValue string)
	BroadcastBuilderAPISubmitBlindedReceived(slot uint64, blockHash string)
	BroadcastBuilderAPISubmitBlindedDelivered(slot uint64, blockHash string)
	BroadcastBidWon(slot uint64, blockHash string, numTxs, numBlobs int, valueETH string, valueWei uint64)
}

// Server implements the combined Builder API + Buildoor API HTTP server.
type Server struct {
	cfg                   *config.BuilderAPIConfig
	log                   *logrus.Logger
	server                *http.Server
	router                *mux.Router
	builderSvc            PayloadCacheProvider // optional: for buildoor debug APIs and Fulu getHeader/submitBlindedBlockV2
	validatorsStore       *validators.Store    // in-memory validator registrations
	blsSigner             *signer.BLSSigner    // optional: for signing Fulu builder bids (getHeader)
	fuluPublisher         FuluBlockPublisher   // optional: for publishing unblinded blocks (submitBlindedBlockV2)
	eventBroadcaster      EventBroadcaster     // optional: for broadcasting API events to WebUI
	bidsWonStore          *BidsWonStore        // in-memory store of successfully delivered blocks
	enabled               atomic.Bool          // runtime toggle for enabling/disabling the builder API
	genesisForkVersion    phase0.Version       // genesis fork version for builder domain (mev-boost-relay style)
	forkVersion           phase0.Version       // current fork version for chain-specific verification
	genesisValidatorsRoot phase0.Root          // genesis validators root for chain-specific verification
}

// NewServer creates a new server. builderSvc may be nil; if set, buildoor-specific
// endpoints and Fulu getHeader/submitBlindedBlockV2 will be enabled. blsSigner may be nil;
// if set, getHeader will sign builder bids. fuluPublisher may be set later via SetFuluPublisher.
// validatorStore is optional; when provided it is shared with the builder service for fee recipient lookup.
// genesisForkVersion is used for DomainBuilder (genesis fork + zero root) like mev-boost-relay; forkVersion and
// genesisValidatorsRoot are used for chain-specific verification. Pass chain values from the beacon node.
func NewServer(cfg *config.BuilderAPIConfig, log *logrus.Logger, builderSvc PayloadCacheProvider, blsSigner *signer.BLSSigner, validatorStore *validators.Store, genesisForkVersion, forkVersion phase0.Version, genesisValidatorsRoot phase0.Root) *Server {
	store := validatorStore
	if store == nil {
		store = validators.NewStore()
	}
	s := &Server{
		cfg:                   cfg,
		log:                   log,
		router:                mux.NewRouter(),
		builderSvc:            builderSvc,
		validatorsStore:       store,
		blsSigner:             blsSigner,
		bidsWonStore:          NewBidsWonStore(1000),
		genesisForkVersion:    genesisForkVersion,
		forkVersion:           forkVersion,
		genesisValidatorsRoot: genesisValidatorsRoot,
	}

	s.registerRoutes()

	return s
}

// SetEnabled sets the enabled state of the Builder API server.
func (s *Server) SetEnabled(enabled bool) {
	s.enabled.Store(enabled)
}

// IsEnabled returns whether the Builder API server is enabled.
func (s *Server) IsEnabled() bool {
	return s.enabled.Load()
}

// SetFuluPublisher sets the optional publisher for unblinded Fulu blocks (e.g. beacon node client).
func (s *Server) SetFuluPublisher(p FuluBlockPublisher) {
	s.fuluPublisher = p
}

// SetEventBroadcaster sets the optional event broadcaster for WebUI events.
func (s *Server) SetEventBroadcaster(b EventBroadcaster) {
	s.eventBroadcaster = b
}

// GetBidsWonStore returns the bids won store.
func (s *Server) GetBidsWonStore() *BidsWonStore {
	return s.bidsWonStore
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
	buildoorAPI.HandleFunc("/validators", s.handleGetValidators).Methods(http.MethodGet)
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
	log := s.log.WithField("path", "/eth/v1/builder/validators")
	defer func() {
		if err := recover(); err != nil {
			log.WithField("panic", err).Error("Panic in validator registration handler")
			writeValidatorError(w, http.StatusInternalServerError, fmt.Sprintf("internal error: %v", err))
		}
	}()

	log.WithFields(logrus.Fields{
		"method":         r.Method,
		"content_type":   r.Header.Get("Content-Type"),
		"content_length": r.Header.Get("Content-Length"),
	}).Debug("Validator registration request received")

	if r.Header.Get("Content-Type") != "application/json" {
		log.WithField("content_type", r.Header.Get("Content-Type")).Warn("Rejected: Content-Type must be application/json")
		writeValidatorError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.WithError(err).Warn("Rejected: failed to read body")
		writeValidatorError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	var regs []*apiv1.SignedValidatorRegistration
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&regs); err != nil {
		log.WithError(err).WithField("request_body_json", string(body)).Warn("Rejected: invalid JSON body")
		writeValidatorError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	log.WithField("count", len(regs)).Debug("Decoded validator registrations")

	for i, reg := range regs {
		if reg == nil || reg.Message == nil {
			log.WithFields(logrus.Fields{"index": i, "total": len(regs)}).Warn("Rejected: registration message missing")
			writeValidatorError(w, http.StatusBadRequest, "registration message missing")
			return
		}
		pubkeyHex := hex.EncodeToString(reg.Message.Pubkey[:])
		if !validators.VerifyRegistrationWithDomain(reg, s.genesisForkVersion, s.forkVersion, s.genesisValidatorsRoot) {
			// Log first failing registration as JSON for debugging (copy and share).
			rejJSON, _ := json.Marshal(reg)
			log.WithFields(logrus.Fields{
				"index":             i,
				"total":             len(regs),
				"pubkey":            pubkeyHex,
				"rejected_reg_json": string(rejJSON),
			}).Warn("Rejected: invalid signature for validator")
			writeValidatorError(w, http.StatusBadRequest, "invalid signature for validator "+pubkeyHex)
			return
		}
		s.validatorsStore.Put(reg)
		log.WithFields(logrus.Fields{"index": i, "pubkey": pubkeyHex}).Debug("Stored validator registration")
	}

	log.WithField("stored_count", len(regs)).Info("Validator registrations accepted")
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

// handleGetValidators handles GET /buildoor/v1/validators.
// Returns the list of validator registrations stored from POST /eth/v1/builder/validators.
func (s *Server) handleGetValidators(w http.ResponseWriter, _ *http.Request) {
	regs := s.validatorsStore.List()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"validators": regs})
}

// handleGetHeader handles GET /eth/v1/builder/header/{slot}/{parent_hash}/{pubkey}.
// Returns 200 with Fulu SignedBuilderBid, or 204 if no bid, or 400 on invalid params / unregistered proposer.
func (s *Server) handleGetHeader(w http.ResponseWriter, r *http.Request) {
	log := s.log.WithField("path", "/eth/v1/builder/header/...")

	if s.builderSvc == nil || s.blsSigner == nil || !s.enabled.Load() {
		log.Warn("getHeader: returning 204 — builder service or BLS signer not available or service disabled")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	vars := mux.Vars(r)
	slotStr := vars["slot"]
	parentHashStr := vars["parent_hash"]
	pubkeyStr := vars["pubkey"]

	log = log.WithFields(logrus.Fields{
		"slot":        slotStr,
		"parent_hash": parentHashStr,
		"pubkey":      pubkeyStr,
	})
	log.Debug("getHeader request received")

	// log all headers
	for header, value := range r.Header {
		log.WithField("header", header).Debug("value: " + strings.Join(value, ", "))
	}

	slotU64, err := strconv.ParseUint(slotStr, 10, 64)
	if err != nil {
		log.WithError(err).Warn("getHeader: invalid slot")
		writeValidatorError(w, http.StatusBadRequest, "invalid slot: must be a number")
		return
	}

	// Broadcast getHeader received event
	if s.eventBroadcaster != nil {
		s.eventBroadcaster.BroadcastBuilderAPIGetHeaderReceived(slotU64, parentHashStr, pubkeyStr)
	}

	parentHashBytes, err := hex.DecodeString(trimHex(parentHashStr))
	if err != nil || len(parentHashBytes) != 32 {
		log.WithError(err).Warn("getHeader: invalid parent_hash")
		writeValidatorError(w, http.StatusBadRequest, "invalid parent_hash: must be 32 bytes hex")
		return
	}
	var parentHash phase0.Hash32
	copy(parentHash[:], parentHashBytes)

	pubkeyBytes, err := hex.DecodeString(trimHex(pubkeyStr))
	if err != nil || len(pubkeyBytes) != 48 {
		log.WithError(err).Warn("getHeader: invalid pubkey")
		writeValidatorError(w, http.StatusBadRequest, "invalid pubkey: must be 48 bytes hex")
		return
	}
	var pubkey phase0.BLSPubKey
	copy(pubkey[:], pubkeyBytes)

	if s.validatorsStore.Get(pubkey) == nil {
		log.WithField("pubkey_hex", "0x"+hex.EncodeToString(pubkey[:])).Info(
			"getHeader: returning 204 — proposer not in validator store (no registration for this pubkey)")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	cache := s.builderSvc.GetPayloadCache()
	event := cache.Get(phase0.Slot(slotU64))
	if event == nil {
		log.WithField("slot", slotU64).Info(
			"getHeader: returning 204 — no cached payload for slot")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if event.ParentBlockHash != parentHash {
		log.WithFields(logrus.Fields{
			"slot":                slotU64,
			"request_parent_hash": "0x" + hex.EncodeToString(parentHash[:]),
			"cached_parent_hash":  "0x" + hex.EncodeToString(event.ParentBlockHash[:]),
		}).Info("getHeader: returning 204 — cached payload parent hash does not match request")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	log.Infof("getHeader: PayloadEvent gas limit is %d", event.GasLimit)

	subsidyGwei := uint64(0)
	if s.cfg != nil {
		subsidyGwei = s.cfg.BlockValueSubsidyGwei
	}
	signedBid, err := fulu.BuildSignedBuilderBid(event, s.blsSigner.PublicKey(), s.blsSigner, subsidyGwei, s.genesisForkVersion, s.genesisValidatorsRoot)
	if err != nil {
		log.WithError(err).Warn("getHeader: failed to build SignedBuilderBid")
		writeValidatorError(w, http.StatusInternalServerError, "failed to build bid")
		return
	}

	resp := fulu.GetHeaderResponse{
		Version: "fulu",
		Data:    signedBid,
	}

	log.WithFields(logrus.Fields{
		"slot":        slotU64,
		"block_hash":  "0x" + hex.EncodeToString(event.BlockHash[:]),
		"parent_hash": "0x" + hex.EncodeToString(parentHash[:]),
		"value":       signedBid.Message.Value.String(),
		"gas_limit":   signedBid.Message.Header.GasLimit,
	}).Infof("getHeader: delivered header for slot %d", slotU64)

	// Broadcast getHeader delivered event
	if s.eventBroadcaster != nil {
		blockHashHex := "0x" + hex.EncodeToString(event.BlockHash[:])
		s.eventBroadcaster.BroadcastBuilderAPIGetHeaderDelivered(slotU64, blockHashHex, signedBid.Message.Value.String())
	}

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
	log := s.log.WithField("path", "/eth/v2/builder/blinded_blocks")

	if r.Header.Get("Content-Type") != "application/json" {
		log.Warn("submitBlindedBlock: Content-Type must be application/json")
		writeValidatorError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}

	if s.builderSvc == nil {
		log.Warn("submitBlindedBlock: builder service not available")
		writeValidatorError(w, http.StatusBadRequest, "builder not available")
		return
	}

	var blinded apiv1electra.SignedBlindedBeaconBlock
	if err := json.NewDecoder(r.Body).Decode(&blinded); err != nil {
		log.WithError(err).Warn("submitBlindedBlock: invalid JSON body")
		writeValidatorError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if blinded.Message == nil || blinded.Message.Body == nil ||
		blinded.Message.Body.ExecutionPayloadHeader == nil {
		log.Warn("submitBlindedBlock: blinded block missing message or execution_payload_header")
		writeValidatorError(w, http.StatusBadRequest, "invalid blinded block: missing message or execution_payload_header")
		return
	}

	blockHash := blinded.Message.Body.ExecutionPayloadHeader.BlockHash
	slot := blinded.Message.Slot
	blockHashHex := "0x" + hex.EncodeToString(blockHash[:])
	log = log.WithFields(logrus.Fields{
		"slot":       slot,
		"block_hash": blockHashHex,
	})
	log.Debug("submitBlindedBlock request received")

	// Broadcast submitBlindedBlock received event
	if s.eventBroadcaster != nil {
		s.eventBroadcaster.BroadcastBuilderAPISubmitBlindedReceived(uint64(slot), blockHashHex)
	}

	cache := s.builderSvc.GetPayloadCache()
	event := cache.GetByBlockHash(blockHash)
	if event == nil {
		log.Info("submitBlindedBlock: no cached payload for block hash (payload may not have been built or already evicted)")
		writeValidatorError(w, http.StatusBadRequest, "no matching payload for block hash")
		return
	}

	contents, err := fulu.UnblindSignedBlindedBeaconBlock(&blinded, event)
	if err != nil {
		log.WithError(err).Warn("submitBlindedBlock: unblind failed")
		writeValidatorError(w, http.StatusBadRequest, "unblind failed: "+err.Error())
		return
	}
	if contents == nil {
		log.Warn("submitBlindedBlock: unblind produced no contents")
		writeValidatorError(w, http.StatusBadRequest, "unblind produced no contents")
		return
	}

	log.Infof("submitBlindedBlock: unblinded block for slot %d", slot)

	if s.fuluPublisher != nil {
		if err := s.fuluPublisher.SubmitFuluBlock(r.Context(), contents); err != nil {
			log.WithError(err).Error("submitBlindedBlock: failed to publish unblinded block")
			writeValidatorError(w, http.StatusInternalServerError, "failed to publish block: "+err.Error())
			return
		} else {
			log.Info("SubmitBlindedBlock: Successfully published block!")
		}
	} else {
		log.Warn("submitBlindedBlock: no publisher available")
		writeValidatorError(w, http.StatusBadRequest, "no publisher available")
		return
	}

	log.Infof("submitBlindedBlock: submitted unblinded block for slot %d, block hash %s", slot, blockHashHex)

	// Capture bid won data
	numTxs := len(event.Payload.Transactions)
	numBlobs := 0
	if event.BlobsBundle != nil && event.BlobsBundle.Commitments != nil {
		numBlobs = len(event.BlobsBundle.Commitments)
	}

	valueETH := weiToETH(event.BlockValue)

	entry := BidWonEntry{
		Slot:            uint64(slot),
		BlockHash:       blockHashHex,
		NumTransactions: numTxs,
		NumBlobs:        numBlobs,
		ValueETH:        valueETH,
		ValueWei:        event.BlockValue,
		Timestamp:       time.Now().UnixMilli(),
	}

	s.bidsWonStore.Add(entry)

	// Broadcast bid won event to WebUI
	if s.eventBroadcaster != nil {
		s.eventBroadcaster.BroadcastBidWon(uint64(slot), blockHashHex, numTxs, numBlobs, valueETH, event.BlockValue)
		s.eventBroadcaster.BroadcastBuilderAPISubmitBlindedDelivered(uint64(slot), blockHashHex)
	}

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
