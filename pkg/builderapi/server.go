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
	"math/big"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"

	epbsapi "github.com/ethpandaops/buildoor/pkg/builderapi/epbs"
	"github.com/ethpandaops/buildoor/pkg/builderapi/legacy"
	"github.com/ethpandaops/buildoor/pkg/builderapi/validators"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/proposerpreferences"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// EventBroadcaster provides methods for broadcasting Builder API events to the
// WebUI. It is the combined surface of both dialects' narrow broadcaster
// interfaces (which it satisfies structurally) plus the bids-won broadcast.
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
	BroadcastBidWon(slot uint64, blockHash string, numTxs, numBlobs int, valueETH string, valueWei string)
}

// RequestStats holds counters for Builder API requests, aggregated across both
// dialect handlers.
type RequestStats struct {
	HeadersRequested uint64
	BlocksPublished  uint64
	ValidatorCount   int
}

// Server hosts the Builder API dialect handlers and the Buildoor debug API.
// It owns the route table, the in-memory bids-won store, enable-state fan-out,
// and request-stat aggregation; all endpoint logic lives in the dialects.
type Server struct {
	cfg             *config.BuilderAPIConfig
	log             *logrus.Logger
	chainSvc        chain.Service
	payloadCache    *payload_builder.PayloadCache // debug endpoints + dialect construction
	validatorsStore *validators.Store             // in-memory validator registrations
	legacy          *legacy.Handler               // pre-Gloas dialect (Electra/Fulu)
	epbs            *epbsapi.Handler              // post-Gloas dialect (Gloas/Heze+)
	bidsWonStore    *BidsWonStore                 // in-memory store of successfully delivered blocks
	events          EventBroadcaster              // optional: for broadcasting API events to WebUI
	enabled         atomic.Bool                   // runtime toggle for enabling/disabling the builder API
}

// NewServer creates a new server and constructs both dialect handlers.
// payloadCache may be nil; endpoints needing it degrade gracefully. blsSigner
// may be nil; if set, getHeader signs builder bids. validatorStore is optional;
// when provided it is shared with the builder service for fee recipient lookup.
func NewServer(cfg *config.BuilderAPIConfig, log *logrus.Logger, chainSvc chain.Service,
	payloadCache *payload_builder.PayloadCache, blsSigner *signer.BLSSigner,
	validatorStore *validators.Store) *Server {
	store := validatorStore
	if store == nil {
		store = validators.NewStore()
	}

	s := &Server{
		cfg:             cfg,
		log:             log,
		chainSvc:        chainSvc,
		payloadCache:    payloadCache,
		validatorsStore: store,
		legacy:          legacy.NewHandler(cfg, log, chainSvc, payloadCache, store, blsSigner),
		epbs:            epbsapi.NewHandler(cfg, log, chainSvc, payloadCache, blsSigner),
		bidsWonStore:    NewBidsWonStore(1000),
	}
	s.legacy.SetWinRecorder(s)

	return s
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
// the server and both dialect handlers.
func (s *Server) SetEventBroadcaster(b EventBroadcaster) {
	s.events = b
	s.legacy.SetEventBroadcaster(b)
	s.epbs.SetEventBroadcaster(b)
}

// SetProposerPreferencesCache wires the proposer preferences cache used by the
// post-Gloas dialect to resolve fee recipients when building bids.
func (s *Server) SetProposerPreferencesCache(cache *proposerpreferences.Cache) {
	s.epbs.SetProposerPreferencesCache(cache)
}

// SetBuilderIndex sets the on-chain builder index inserted into Gloas bids.
// Called from the lifecycle manager once registration is observed.
func (s *Server) SetBuilderIndex(index uint64) {
	s.epbs.SetBuilderIndex(index)
}

// GetBidsWonStore returns the bids won store.
func (s *Server) GetBidsWonStore() *BidsWonStore {
	return s.bidsWonStore
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

// RecordBidWon records a successfully delivered block for the UI: in-memory
// bids-won store and WebUI broadcast. It implements legacy.WinRecorder.
// Durable won_blocks persistence is NOT done here — the shared
// payload_bidder.InclusionTracker is the single state-db writer, recording
// actual inclusion (a delivery-time write here would double-insert every
// legacy win alongside the head-event path's write).
func (s *Server) RecordBidWon(slot phase0.Slot, blockHash phase0.Hash32,
	numTxs, numBlobs int, valueWei *big.Int) {
	blockHashHex := "0x" + hex.EncodeToString(blockHash[:])

	entry := BidWonEntry{
		Slot:            uint64(slot),
		BlockHash:       blockHashHex,
		NumTransactions: numTxs,
		NumBlobs:        numBlobs,
		ValueETH:        weiToETH(valueWei),
		ValueWei:        valueWei.String(),
		Timestamp:       time.Now().UnixMilli(),
	}
	s.bidsWonStore.Add(entry)

	if s.events != nil {
		s.events.BroadcastBidWon(entry.Slot, blockHashHex, numTxs, numBlobs, entry.ValueETH, entry.ValueWei)
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
	regs := s.validatorsStore.List()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"validators": regs})
}
