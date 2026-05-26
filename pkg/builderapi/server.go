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

	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	apiv1electra "github.com/ethpandaops/go-eth2-client/api/v1/electra"
	apiv1fulu "github.com/ethpandaops/go-eth2-client/api/v1/fulu"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/electra"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/builderapi/fulu"
	"github.com/ethpandaops/buildoor/pkg/builderapi/validators"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/proposerpreferences"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// domainBeaconBuilder is DOMAIN_BEACON_BUILDER from the Gloas consensus spec,
// used to sign ExecutionPayloadBid and ExecutionPayloadEnvelope messages.
var domainBeaconBuilder = phase0.DomainType{0x0B, 0x00, 0x00, 0x00}

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
	BroadcastBidWon(slot uint64, blockHash string, numTxs, numBlobs int, valueETH string, valueWei string)
}

// RequestStats holds counters for Builder API requests.
type RequestStats struct {
	HeadersRequested uint64
	BlocksPublished  uint64
	ValidatorCount   int
}

// Server implements the combined Builder API + Buildoor API HTTP server.
type Server struct {
	cfg                   *config.BuilderAPIConfig
	log                   *logrus.Logger
	server                *http.Server
	router                *mux.Router
	builderSvc            PayloadCacheProvider       // optional: for buildoor debug APIs and Fulu getHeader/submitBlindedBlockV2
	validatorsStore       *validators.Store          // in-memory validator registrations
	blsSigner             *signer.BLSSigner          // optional: for signing Fulu builder bids (getHeader)
	fuluPublisher         FuluBlockPublisher         // optional: for publishing unblinded blocks (submitBlindedBlockV2)
	clClient              *beacon.Client             // optional: beacon client used to publish Gloas execution payload envelopes
	eventBroadcaster      EventBroadcaster           // optional: for broadcasting API events to WebUI
	bidsWonStore          *BidsWonStore              // in-memory store of successfully delivered blocks
	propPrefsCache        *proposerpreferences.Cache // optional: per-slot proposer preferences for Gloas bid construction
	builderIndex          atomic.Uint64              // builder index used in Gloas bids; set after lifecycle registration
	enabled               atomic.Bool                // runtime toggle for enabling/disabling the builder API
	headersRequested      atomic.Uint64              // count of getHeader requests received
	blocksPublished       atomic.Uint64              // count of successfully published blocks
	genesisForkVersion    phase0.Version             // genesis fork version for builder domain (mev-boost-relay style)
	forkVersion           phase0.Version             // current fork version for chain-specific verification
	genesisValidatorsRoot phase0.Root                // genesis validators root for chain-specific verification
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

// SetCLClient wires the beacon client used to publish Gloas execution payload
// envelopes after a SignedBeaconBlock is submitted via the Builder API.
func (s *Server) SetCLClient(c *beacon.Client) {
	s.clClient = c
}

// SetEventBroadcaster sets the optional event broadcaster for WebUI events.
func (s *Server) SetEventBroadcaster(b EventBroadcaster) {
	s.eventBroadcaster = b
}

// SetProposerPreferencesCache wires the proposer preferences cache used to
// resolve fee recipient and gas limit when building Gloas execution payload bids.
func (s *Server) SetProposerPreferencesCache(cache *proposerpreferences.Cache) {
	s.propPrefsCache = cache
}

// SetBuilderIndex sets the on-chain builder index inserted into Gloas bids.
// Called from the lifecycle manager once registration is observed.
func (s *Server) SetBuilderIndex(index uint64) {
	s.builderIndex.Store(index)
}

// GetBidsWonStore returns the bids won store.
func (s *Server) GetBidsWonStore() *BidsWonStore {
	return s.bidsWonStore
}

// GetRequestStats returns the current request counters.
func (s *Server) GetRequestStats() RequestStats {
	return RequestStats{
		HeadersRequested: s.headersRequested.Load(),
		BlocksPublished:  s.blocksPublished.Load(),
		ValidatorCount:   s.validatorsStore.Len(),
	}
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

	// --- Builder API (Gloas) ---
	// https://github.com/ethereum/builder-specs/blob/epbs-spec-updates/apis/builder/execution_payload_bid.yaml
	builderAPI.HandleFunc(
		"/execution_payload_bid/{slot}/{parent_hash}/{parent_root}/{proposer_pubkey}",
		s.handleGetExecutionPayloadBid,
	).Methods(http.MethodPost)
	// https://github.com/ethereum/builder-specs/blob/epbs-spec-updates/apis/builder/beacon_block.yaml
	builderAPI.HandleFunc("/beacon_block", s.handleSubmitSignedBeaconBlock).Methods(http.MethodPost)

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
		BlockValue:      event.BlockValue.String(),
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

	s.headersRequested.Add(1)

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

	subsidyGwei := uint64(0)
	if s.cfg != nil {
		subsidyGwei = s.cfg.BlockValueSubsidyGwei
	}
	log.Info("Subsidy Gwei: " + fmt.Sprintf("%d", subsidyGwei))
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

// GetExecutionPayloadBidResponse is the JSON envelope returned by
// POST /eth/v1/builder/execution_payload_bid/{slot}/{parent_hash}/{parent_root}/{proposer_pubkey}.
type GetExecutionPayloadBidResponse struct {
	Version string                           `json:"version"`
	Data    *gloas.SignedExecutionPayloadBid `json:"data"`
}

// handleGetExecutionPayloadBid handles
// POST /eth/v1/builder/execution_payload_bid/{slot}/{parent_hash}/{parent_root}/{proposer_pubkey}.
//
// Looks up the cached payload for the requested slot, validates the supplied
// parent_hash and parent_root against it, then constructs and signs a Gloas
// SignedExecutionPayloadBid using the proposer's fee recipient from the
// ProposerPreferences cache. Returns 204 if no payload is cached, 400 if the
// inputs do not match the cached payload or proposer preferences are missing.
//
// Note: RequestAuth verification and BuilderPreferences (max_execution_payment)
// are not yet wired — both value and execution_payment are set to zero.
func (s *Server) handleGetExecutionPayloadBid(w http.ResponseWriter, r *http.Request) {
	log := s.log.WithField("path", "/eth/v1/builder/execution_payload_bid/...")

	if !s.enabled.Load() || s.builderSvc == nil || s.blsSigner == nil {
		log.Warn("getExecutionPayloadBid: returning 204 — builder service disabled or signer/payload cache unavailable")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if s.propPrefsCache == nil {
		log.Warn("getExecutionPayloadBid: proposer preferences cache not configured")
		writeValidatorError(w, http.StatusInternalServerError, "proposer preferences cache not configured")
		return
	}

	vars := mux.Vars(r)
	slotStr := vars["slot"]
	parentHashStr := vars["parent_hash"]
	parentRootStr := vars["parent_root"]
	proposerPubkeyStr := vars["proposer_pubkey"]

	log = log.WithFields(logrus.Fields{
		"slot":            slotStr,
		"parent_hash":     parentHashStr,
		"parent_root":     parentRootStr,
		"proposer_pubkey": proposerPubkeyStr,
	})
	log.Debug("getExecutionPayloadBid request received")

	slotU64, err := strconv.ParseUint(slotStr, 10, 64)
	if err != nil {
		log.WithError(err).Warn("getExecutionPayloadBid: invalid slot")
		writeValidatorError(w, http.StatusBadRequest, "invalid slot: must be a number")
		return
	}
	slot := phase0.Slot(slotU64)

	parentHashBytes, err := hex.DecodeString(trimHex(parentHashStr))
	if err != nil || len(parentHashBytes) != 32 {
		log.WithError(err).Warn("getExecutionPayloadBid: invalid parent_hash")
		writeValidatorError(w, http.StatusBadRequest, "invalid parent_hash: must be 32 bytes hex")
		return
	}
	var parentHash phase0.Hash32
	copy(parentHash[:], parentHashBytes)

	parentRootBytes, err := hex.DecodeString(trimHex(parentRootStr))
	if err != nil || len(parentRootBytes) != 32 {
		log.WithError(err).Warn("getExecutionPayloadBid: invalid parent_root")
		writeValidatorError(w, http.StatusBadRequest, "invalid parent_root: must be 32 bytes hex")
		return
	}
	var parentRoot phase0.Root
	copy(parentRoot[:], parentRootBytes)

	event := s.builderSvc.GetPayloadCache().Get(slot)
	if event == nil {
		log.Info("getExecutionPayloadBid: returning 204 — no cached payload for slot")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if event.ParentBlockHash != parentHash {
		log.WithFields(logrus.Fields{
			"request_parent_hash": "0x" + hex.EncodeToString(parentHash[:]),
			"cached_parent_hash":  "0x" + hex.EncodeToString(event.ParentBlockHash[:]),
		}).Info("getExecutionPayloadBid: 400 — parent_hash does not match cached payload")
		writeValidatorError(w, http.StatusBadRequest, "parent_hash does not match cached payload")
		return
	}

	if event.ParentBlockRoot != parentRoot {
		log.WithFields(logrus.Fields{
			"request_parent_root": "0x" + hex.EncodeToString(parentRoot[:]),
			"cached_parent_root":  "0x" + hex.EncodeToString(event.ParentBlockRoot[:]),
		}).Info("getExecutionPayloadBid: 400 — parent_root does not match cached payload")
		writeValidatorError(w, http.StatusBadRequest, "parent_root does not match cached payload")
		return
	}

	signedPrefs, ok := s.propPrefsCache.Get(slot)
	if !ok || signedPrefs == nil || signedPrefs.Message == nil {
		log.Info("getExecutionPayloadBid: 400 — no proposer preferences cached for slot")
		writeValidatorError(w, http.StatusBadRequest, "no proposer preferences cached for slot")
		return
	}
	prefs := signedPrefs.Message

	execRequests := &electra.ExecutionRequests{
		Deposits:       []*electra.DepositRequest{},
		Withdrawals:    []*electra.WithdrawalRequest{},
		Consolidations: []*electra.ConsolidationRequest{},
	}
	if len(event.ExecutionRequests) > 0 {
		parsed, parseErr := fulu.ParseExecutionRequests(event.ExecutionRequests)
		if parseErr != nil {
			log.WithError(parseErr).Warn("getExecutionPayloadBid: failed to parse execution requests")
			writeValidatorError(w, http.StatusInternalServerError, "failed to parse execution requests")
			return
		}
		execRequests = parsed
	}
	execRequestsRoot, err := execRequests.HashTreeRoot()
	if err != nil {
		log.WithError(err).Warn("getExecutionPayloadBid: failed to compute execution requests root")
		writeValidatorError(w, http.StatusInternalServerError, "failed to compute execution requests root")
		return
	}

	bid := &gloas.ExecutionPayloadBid{
		ParentBlockHash:       event.ParentBlockHash,
		ParentBlockRoot:       event.ParentBlockRoot,
		BlockHash:             event.BlockHash,
		PrevRandao:            event.PrevRandao,
		FeeRecipient:          prefs.FeeRecipient,
		GasLimit:              event.GasLimit,
		BuilderIndex:          gloas.BuilderIndex(s.builderIndex.Load()),
		Slot:                  slot,
		Value:                 0,
		ExecutionPayment:      0,
		BlobKZGCommitments:    []deneb.KZGCommitment{},
		ExecutionRequestsRoot: execRequestsRoot,
	}
	if event.BlobsBundle != nil {
		bid.BlobKZGCommitments = make([]deneb.KZGCommitment, len(event.BlobsBundle.Commitments))
		for i, c := range event.BlobsBundle.Commitments {
			copy(bid.BlobKZGCommitments[i][:], c)
		}
	}

	bidRoot, err := bid.HashTreeRoot()
	if err != nil {
		log.WithError(err).Warn("getExecutionPayloadBid: failed to compute bid hash tree root")
		writeValidatorError(w, http.StatusInternalServerError, "failed to compute bid root")
		return
	}
	var root phase0.Root
	copy(root[:], bidRoot[:])

	domain := signer.ComputeDomain(domainBeaconBuilder, s.forkVersion, s.genesisValidatorsRoot)
	sig, err := s.blsSigner.SignWithDomain(root, domain)
	if err != nil {
		log.WithError(err).Warn("getExecutionPayloadBid: failed to sign bid")
		writeValidatorError(w, http.StatusInternalServerError, "failed to sign bid")
		return
	}

	signedBid := &gloas.SignedExecutionPayloadBid{
		Message:   bid,
		Signature: sig,
	}

	log.WithFields(logrus.Fields{
		"block_hash":    "0x" + hex.EncodeToString(event.BlockHash[:]),
		"builder_index": bid.BuilderIndex,
		"fee_recipient": prefs.FeeRecipient.String(),
		"gas_limit":     bid.GasLimit,
	}).Info("getExecutionPayloadBid: delivered Gloas SignedExecutionPayloadBid")

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Eth-Consensus-Version", "gloas")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(GetExecutionPayloadBidResponse{
		Version: "gloas",
		Data:    signedBid,
	})
}

// handleSubmitSignedBeaconBlock handles POST /eth/v1/builder/beacon_block.
//
// The proposer submits a full Gloas SignedBeaconBlock that binds them to the
// builder's bid. If the builder still holds the payload referenced by the
// bid's block_hash, it constructs the corresponding SignedExecutionPayloadEnvelope
// and publishes it (along with blobs / KZG cell proofs) to the beacon node.
//
// Returns 202 on success, 400 on a malformed block or missing payload,
// 415 on wrong Content-Type, 500 on internal errors, and 503 if the server is
// not fully configured.
func (s *Server) handleSubmitSignedBeaconBlock(w http.ResponseWriter, r *http.Request) {
	log := s.log.WithField("path", "/eth/v1/builder/beacon_block")

	if !s.enabled.Load() || s.builderSvc == nil || s.blsSigner == nil || s.clClient == nil {
		log.Warn("submitSignedBeaconBlock: 503 — builder not fully configured (disabled, payload cache, signer, or CL client missing)")
		writeValidatorError(w, http.StatusServiceUnavailable, "builder not ready")
		return
	}

	if r.Header.Get("Content-Type") != "application/json" {
		log.WithField("content_type", r.Header.Get("Content-Type")).Warn("submitSignedBeaconBlock: rejected — Content-Type must be application/json")
		writeValidatorError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.WithError(err).Warn("submitSignedBeaconBlock: failed to read body")
		writeValidatorError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	var block gloas.SignedBeaconBlock
	if err := json.Unmarshal(body, &block); err != nil {
		log.WithError(err).Warn("submitSignedBeaconBlock: invalid JSON body")
		writeValidatorError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if block.Message == nil || block.Message.Body == nil ||
		block.Message.Body.SignedExecutionPayloadBid == nil ||
		block.Message.Body.SignedExecutionPayloadBid.Message == nil {
		log.Warn("submitSignedBeaconBlock: missing signed_execution_payload_bid in block body")
		writeValidatorError(w, http.StatusBadRequest, "missing signed_execution_payload_bid in block body")
		return
	}

	bid := block.Message.Body.SignedExecutionPayloadBid.Message
	blockHashHex := "0x" + hex.EncodeToString(bid.BlockHash[:])
	log = log.WithFields(logrus.Fields{
		"slot":       bid.Slot,
		"block_hash": blockHashHex,
	})
	log.Debug("submitSignedBeaconBlock request received")

	event := s.builderSvc.GetPayloadCache().GetByBlockHash(bid.BlockHash)
	if event == nil {
		log.Info("submitSignedBeaconBlock: 400 — no cached payload for bid block hash")
		writeValidatorError(w, http.StatusBadRequest, "no cached payload for bid block hash")
		return
	}

	gloasPayload, err := fulu.ExecutionPayloadToGloas(event.Payload)
	if err != nil {
		log.WithError(err).Warn("submitSignedBeaconBlock: failed to convert payload to gloas format")
		writeValidatorError(w, http.StatusInternalServerError, "failed to convert payload")
		return
	}

	execRequests := &electra.ExecutionRequests{
		Deposits:       []*electra.DepositRequest{},
		Withdrawals:    []*electra.WithdrawalRequest{},
		Consolidations: []*electra.ConsolidationRequest{},
	}
	if len(event.ExecutionRequests) > 0 {
		parsed, parseErr := fulu.ParseExecutionRequests(event.ExecutionRequests)
		if parseErr != nil {
			log.WithError(parseErr).Warn("submitSignedBeaconBlock: failed to parse execution requests")
			writeValidatorError(w, http.StatusInternalServerError, "failed to parse execution requests")
			return
		}
		execRequests = parsed
	}

	beaconBlockRoot, err := block.Message.HashTreeRoot()
	if err != nil {
		log.WithError(err).Warn("submitSignedBeaconBlock: failed to compute beacon block hash tree root")
		writeValidatorError(w, http.StatusInternalServerError, "failed to compute beacon block root")
		return
	}
	var blockRoot phase0.Root
	copy(blockRoot[:], beaconBlockRoot[:])

	envelope := &gloas.ExecutionPayloadEnvelope{
		Payload:               gloasPayload,
		ExecutionRequests:     execRequests,
		BuilderIndex:          gloas.BuilderIndex(s.builderIndex.Load()),
		BeaconBlockRoot:       blockRoot,
		ParentBeaconBlockRoot: block.Message.ParentRoot,
	}

	envelopeRoot, err := envelope.HashTreeRoot()
	if err != nil {
		log.WithError(err).Warn("submitSignedBeaconBlock: failed to compute envelope hash tree root")
		writeValidatorError(w, http.StatusInternalServerError, "failed to compute envelope root")
		return
	}
	var root phase0.Root
	copy(root[:], envelopeRoot[:])

	domain := signer.ComputeDomain(domainBeaconBuilder, s.forkVersion, s.genesisValidatorsRoot)
	sig, err := s.blsSigner.SignWithDomain(root, domain)
	if err != nil {
		log.WithError(err).Warn("submitSignedBeaconBlock: failed to sign envelope")
		writeValidatorError(w, http.StatusInternalServerError, "failed to sign envelope")
		return
	}

	signedEnvelope := &gloas.SignedExecutionPayloadEnvelope{
		Message:   envelope,
		Signature: sig,
	}

	envelopeJSON, err := json.Marshal(signedEnvelope)
	if err != nil {
		log.WithError(err).Warn("submitSignedBeaconBlock: failed to marshal signed envelope")
		writeValidatorError(w, http.StatusInternalServerError, "failed to marshal envelope")
		return
	}

	var blobs, kzgProofs [][]byte
	if event.BlobsBundle != nil && len(event.BlobsBundle.Blobs) > 0 {
		blobs = event.BlobsBundle.Blobs
		kzgProofs = event.BlobsBundle.Proofs
	}

	if err := s.clClient.SubmitExecutionPayloadEnvelope(r.Context(), envelopeJSON, blobs, kzgProofs); err != nil {
		log.WithError(err).Error("submitSignedBeaconBlock: failed to publish execution payload envelope")
		writeValidatorError(w, http.StatusInternalServerError, "failed to publish envelope: "+err.Error())
		return
	}

	log.WithFields(logrus.Fields{
		"beacon_block_root": "0x" + hex.EncodeToString(blockRoot[:]),
		"blobs":             len(blobs),
	}).Info("submitSignedBeaconBlock: published execution payload envelope")

	w.WriteHeader(http.StatusAccepted)
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

	s.blocksPublished.Add(1)

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
		ValueWei:        event.BlockValue.String(),
		Timestamp:       time.Now().UnixMilli(),
	}

	s.bidsWonStore.Add(entry)

	// Broadcast bid won event to WebUI
	if s.eventBroadcaster != nil {
		s.eventBroadcaster.BroadcastBidWon(uint64(slot), blockHashHex, numTxs, numBlobs, valueETH, event.BlockValue.String())
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
