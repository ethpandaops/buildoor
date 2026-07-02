// Package builderapi hosts the Builder API HTTP server. It mounts two
// flow-specific dialect handlers onto one route table:
//   - pkg/builderapi/legacy — the pre-Gloas dialect (Electra/Fulu):
//     registerValidators, getHeader, submitBlindedBlock
//   - pkg/builderapi/epbs — the post-Gloas dialect (Gloas/Heze+):
//     getExecutionPayloadBid, submitBeaconBlock, submitBuilderPreferences
//
// plus Buildoor-specific debug/tooling endpoints under /buildoor/v1/*.
//
// Builder API follows https://github.com/ethereum/builder-specs
package builderapi

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	gloasspec "github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"

	epbsapi "github.com/ethpandaops/buildoor/pkg/builderapi/epbs"
	"github.com/ethpandaops/buildoor/pkg/builderapi/legacy"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// EventBroadcaster provides methods for broadcasting Builder API events to the
// WebUI. It is the combined surface of both dialects' narrow broadcaster
// interfaces (which it satisfies structurally).
type EventBroadcaster interface {
	BroadcastBuilderAPIGetHeaderReceived(slot uint64, parentHash, pubkey string)
	BroadcastBuilderAPIGetHeaderDelivered(slot uint64, blockHash, blockValue string)
	BroadcastBuilderAPISubmitBlindedReceived(slot uint64, blockHash string)
	BroadcastBuilderAPISubmitBlindedDelivered(slot uint64, blockHash string)
	// Gloas (post-Gloas) builder API interactions.
	BroadcastBuilderAPIGetBidReceived(slot uint64, parentHash, pubkey string)
	BroadcastBuilderAPIGetBidDelivered(slot uint64, blockHash, blockValue string)
	BroadcastBuilderAPISubmitBlockReceived(slot uint64, blockHash string)
	BroadcastBuilderAPISubmitBlockDelivered(slot uint64, blockHash string)
}

// RequestStats holds counters for Builder API requests, aggregated across both
// dialect handlers.
type RequestStats struct {
	HeadersRequested uint64
	BlocksPublished  uint64
	ValidatorCount   int
}

// Server hosts the Builder API dialect handlers and the Buildoor debug API.
// It owns the route table, enable-state fan-out, and request-stat
// aggregation; all endpoint logic lives in the dialects. Won-block tracking
// is NOT done here — the shared payload_bidder.InclusionTracker is the single
// owner of won-block records, recording actual inclusion.
type Server struct {
	cfg             *config.BuilderAPIConfig
	log             *logrus.Logger
	chainSvc        chain.Service
	payloadCache    *payload_builder.PayloadCache // debug endpoints + dialect construction
	validatorsStore *memstore.Store[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration]
	legacy          *legacy.Handler  // pre-Gloas dialect (Electra/Fulu)
	epbs            *epbsapi.Handler // post-Gloas dialect (Gloas/Heze+)
	enabled         atomic.Bool      // runtime toggle for enabling/disabling the builder API
}

// NewServer creates a new server and constructs both dialect handlers.
// payloadCache may be nil; endpoints needing it degrade gracefully. blsSigner
// may be nil; if set, getHeader signs builder bids. validatorStore is optional
// (an in-memory store is created when nil); when provided it is the shared
// instance also read by the legacy registration settings resolver.
func NewServer(cfg *config.BuilderAPIConfig, log *logrus.Logger, chainSvc chain.Service,
	payloadCache *payload_builder.PayloadCache, blsSigner *signer.BLSSigner,
	validatorStore *memstore.Store[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration]) *Server {
	store := validatorStore
	if store == nil {
		store = memstore.New[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration]()
	}

	return &Server{
		cfg:             cfg,
		log:             log,
		chainSvc:        chainSvc,
		payloadCache:    payloadCache,
		validatorsStore: store,
		legacy:          legacy.NewHandler(cfg, log, chainSvc, payloadCache, store, blsSigner),
		epbs:            epbsapi.NewHandler(cfg, log, chainSvc, payloadCache, blsSigner),
	}
}

// SetEnabled sets the enabled state of the Builder API server and both
// dialect handlers.
func (s *Server) SetEnabled(enabled bool) {
	s.enabled.Store(enabled)
	s.legacy.SetEnabled(enabled)
	s.epbs.SetEnabled(enabled)
}

// IsEnabled returns whether the Builder API server is enabled.
func (s *Server) IsEnabled() bool {
	return s.enabled.Load()
}

// SetFuluPublisher sets the publisher for unblinded Fulu blocks used by the
// legacy dialect (e.g. beacon node client).
func (s *Server) SetFuluPublisher(p legacy.BlockPublisher) {
	s.legacy.SetBlockPublisher(p)
}

// SetCLClient wires the beacon client used by the post-Gloas dialect to
// broadcast the proposer's signed beacon block. Envelope publishing is owned
// by the shared payload_bidder.RevealService, not this package.
func (s *Server) SetCLClient(c *beacon.Client) {
	s.epbs.SetBlockBroadcaster(c)
}

// SetRevealService wires the shared reveal service used by the post-Gloas
// dialect to schedule execution payload envelope reveals.
func (s *Server) SetRevealService(rs *payload_bidder.RevealService) {
	s.epbs.SetRevealService(rs)
}

// SetEventBroadcaster sets the optional event broadcaster for WebUI events on
// both dialect handlers.
func (s *Server) SetEventBroadcaster(b EventBroadcaster) {
	s.legacy.SetEventBroadcaster(b)
	s.epbs.SetEventBroadcaster(b)
}

// SetProposerPreferencesStore wires the per-slot proposer preferences store
// used by the post-Gloas dialect to resolve fee recipients when building bids.
func (s *Server) SetProposerPreferencesStore(
	store *memstore.Store[phase0.Slot, *gloasspec.SignedProposerPreferences]) {
	s.epbs.SetProposerPreferencesStore(store)
}

// SetBuilderIndex sets the on-chain builder index inserted into Gloas bids.
// Called from the lifecycle manager once registration is observed.
func (s *Server) SetBuilderIndex(index uint64) {
	s.epbs.SetBuilderIndex(index)
}

// GetBuilderPreferencesStore returns the store of latest per-validator builder
// preferences submitted via the submitBuilderPreferences API.
func (s *Server) GetBuilderPreferencesStore() *epbsapi.BuilderPreferencesStore {
	return s.epbs.GetBuilderPreferencesStore()
}

// GetRequestStats returns the current request counters aggregated across both
// dialect handlers.
func (s *Server) GetRequestStats() RequestStats {
	return RequestStats{
		HeadersRequested: s.legacy.HeadersRequested() + s.epbs.BidsRequested(),
		BlocksPublished:  s.legacy.BlocksPublished() + s.epbs.BlocksAccepted(),
		ValidatorCount:   s.validatorsStore.Len(),
	}
}

// RegisterRoutes registers Builder API and Buildoor API routes onto the given
// router, delegating the spec endpoints to the dialect handlers.
func (s *Server) RegisterRoutes(router *mux.Router) {
	// --- Builder API (standard spec) ---
	// https://github.com/ethereum/builder-specs
	builderAPI := router.PathPrefix("/eth/v1/builder").Subrouter()
	builderAPI.HandleFunc("/status", s.handleBuilderStatus).Methods(http.MethodGet)
	builderAPI.HandleFunc("/validators", s.legacy.HandleRegisterValidators).Methods(http.MethodPost)
	builderAPI.HandleFunc("/header/{slot}/{parent_hash}/{pubkey}", s.legacy.HandleGetHeader).Methods(http.MethodGet)

	// --- Builder API v2 (Fulu blinded blocks) ---
	builderAPIv2 := router.PathPrefix("/eth/v2/builder").Subrouter()
	builderAPIv2.HandleFunc("/blinded_blocks", s.legacy.HandleSubmitBlindedBlock).Methods(http.MethodPost)

	// --- Builder API (post-Gloas dialect) ---
	// https://github.com/ethereum/builder-specs/blob/epbs-spec-updates/apis/builder/execution_payload_bid.yaml
	builderAPI.HandleFunc(
		"/execution_payload_bid/{slot}/{parent_hash}/{parent_root}/{proposer_pubkey}",
		s.epbs.HandleGetExecutionPayloadBid,
	).Methods(http.MethodPost)
	// https://github.com/ethereum/builder-specs/blob/epbs-spec-updates/apis/builder/beacon_block.yaml
	builderAPI.HandleFunc("/beacon_block", s.epbs.HandleSubmitBeaconBlock).Methods(http.MethodPost)
	// https://github.com/ethereum/builder-specs/blob/epbs-spec-updates/apis/builder/builder_preferences.yaml
	builderAPI.HandleFunc(
		"/builder_preferences/{validator_pubkey}",
		s.epbs.HandleSubmitBuilderPreferences,
	).Methods(http.MethodPost)

	// --- Buildoor API (debug / tooling) ---
	buildoorAPI := router.PathPrefix("/buildoor/v1").Subrouter()
	buildoorAPI.HandleFunc("/payloads/{slot}", s.handleGetPayloadBySlot).Methods(http.MethodGet)
	buildoorAPI.HandleFunc("/validators", s.handleGetValidators).Methods(http.MethodGet)
}

// Handler returns an HTTP handler with routes registered; used in tests.
func (s *Server) Handler() http.Handler {
	r := mux.NewRouter()
	s.RegisterRoutes(r)
	return r
}

// handleBuilderStatus handles GET /eth/v1/builder/status
// Returns 200 OK if the builder is ready to accept requests.
func (s *Server) handleBuilderStatus(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
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
	ReadyAt         time.Time       `json:"ready_at"`
}

// handleGetPayloadBySlot handles GET /buildoor/v1/payloads/{slot}.
// Returns the cached execution payload for the given slot, or 404 if not found.
func (s *Server) handleGetPayloadBySlot(w http.ResponseWriter, r *http.Request) {
	if s.payloadCache == nil {
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

	event := s.payloadCache.Get(phase0.Slot(slotU64))
	if event == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "payload not found for slot"})
		return
	}

	marshalledPayload, err := event.ExecutionPayload.MarshalJSON()
	if err != nil {
		return
	}

	marshalledBlobsBundle, err := event.BlobsBundle.MarshalJSON()
	if err != nil {
		return
	}

	resp := PayloadBySlotResponse{
		Slot:            uint64(event.Attributes.ProposalSlot),
		BlockHash:       "0x" + hex.EncodeToString(event.BlockHash[:]),
		ParentBlockHash: "0x" + hex.EncodeToString(event.Attributes.ParentBlockHash[:]),
		ParentBlockRoot: "0x" + hex.EncodeToString(event.Attributes.ParentBlockRoot[:]),
		Payload:         marshalledPayload,
		BlobsBundle:     marshalledBlobsBundle,
		BlockValue:      event.BlockValue.String(),
		FeeRecipient:    event.FeeRecipient.Hex(),
		GasLimit:        event.ExecutionPayload.GasLimit,
		Timestamp:       event.Attributes.Timestamp,
		ReadyAt:         event.ReadyAt,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// handleGetValidators handles GET /buildoor/v1/validators.
// Returns the list of validator registrations stored from POST /eth/v1/builder/validators.
func (s *Server) handleGetValidators(w http.ResponseWriter, _ *http.Request) {
	regs := s.validatorsStore.Values()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"validators": regs})
}
