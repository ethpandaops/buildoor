package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/version"
)

// StatusResponse is the response for the status endpoint.
type StatusResponse struct {
	Running           bool   `json:"running"`
	BuilderIndex      uint64 `json:"builder_index"`
	BuilderPubkey     string `json:"builder_pubkey"`
	CurrentSlot       uint64 `json:"current_slot"`
	IsRegistered      bool   `json:"is_registered"`
	CLBalance         uint64 `json:"cl_balance_gwei,omitempty"`
	PendingPayments   uint64 `json:"pending_payments_gwei,omitempty"`
	EffectiveBalance  uint64 `json:"effective_balance_gwei,omitempty"`
	LifecycleEnabled  bool   `json:"lifecycle_enabled"`
	WalletAddress     string `json:"wallet_address,omitempty"`
	WalletBalance     string `json:"wallet_balance_wei,omitempty"`
	DepositEpoch      uint64 `json:"deposit_epoch,omitempty"`
	WithdrawableEpoch uint64 `json:"withdrawable_epoch,omitempty"`
}

// StatsResponse is the response for the stats endpoint.
type StatsResponse struct {
	SlotsBuilt     uint64 `json:"slots_built"`
	BlocksIncluded uint64 `json:"blocks_included"`
	BidsSubmitted  uint64 `json:"bids_submitted"`
	BidsWon        uint64 `json:"bids_won"`
	TotalPaid      uint64 `json:"total_paid_gwei"`
	RevealsSuccess uint64 `json:"reveals_success"`
	RevealsFailed  uint64 `json:"reveals_failed"`
	RevealsSkipped uint64 `json:"reveals_skipped"`
	// Builder API stats
	BuilderAPIHeadersRequested     uint64 `json:"builder_api_headers_requested"`
	BuilderAPIBlocksPublished      uint64 `json:"builder_api_blocks_published"`
	BuilderAPIRegisteredValidators int    `json:"builder_api_registered_validators"`
}

// GetVersion godoc
// @Id getVersion
// @Summary Get the current version
// @Tags Version
// @Description Returns the current version
// @Produce json
// @Success 200 {string} string "Success"
// @Failure 500 {string} string "Server Error"
// @Router /version [get]
func (h *APIHandler) GetVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": version.GetBuildVersion()})
}

// GetStatus godoc
// @Id getStatus
// @Summary Get builder status
// @Tags Status
// @Description Returns the current builder status including running state, current slot,
// @Description builder index and public key.
// @Produce json
// @Success 200 {object} StatusResponse "Success"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /status [get]
func (h *APIHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	resp := StatusResponse{
		Running:     true,
		CurrentSlot: uint64(h.builderSvc.GetCurrentSlot()),
	}

	// Get builder identity and pending payments from ePBS service
	if h.epbsSvc != nil {
		resp.BuilderIndex = h.epbsSvc.GetBuilderIndex()
		pubkey := h.epbsSvc.GetBuilderPubkey()
		resp.BuilderPubkey = pubkey.String()
		resp.IsRegistered = h.epbsSvc.IsRegistered()

		// Get pending payments from the shared payment tracker
		if h.payments != nil {
			resp.PendingPayments = h.payments.GetTotalPendingPayments()
		}

		// Get live balance from chain service (works without lifecycle enabled)
		if h.chainSvc != nil {
			if builderInfo := h.chainSvc.GetBuilderByPubkey(pubkey); builderInfo != nil {
				resp.CLBalance = builderInfo.Balance
				resp.DepositEpoch = builderInfo.DepositEpoch
				resp.WithdrawableEpoch = builderInfo.WithdrawableEpoch
			}
		}
	}

	// Calculate effective balance
	if resp.CLBalance > resp.PendingPayments {
		resp.EffectiveBalance = resp.CLBalance - resp.PendingPayments
	}

	// Get wallet info from lifecycle manager
	if h.lifecycleMgr != nil {
		resp.LifecycleEnabled = h.lifecycleMgr.IsEnabled()

		if wallet := h.lifecycleMgr.GetWallet(); wallet != nil {
			resp.WalletAddress = wallet.Address().Hex()

			if balance, err := wallet.GetBalance(r.Context()); err == nil && balance != nil {
				resp.WalletBalance = balance.String()
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetStats godoc
// @Id getStats
// @Summary Get builder statistics
// @Tags Stats
// @Description Returns builder statistics including slots built, bids submitted/won,
// @Description total paid, and reveal success/failure counts.
// @Produce json
// @Success 200 {object} StatsResponse "Success"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /stats [get]
func (h *APIHandler) GetStats(w http.ResponseWriter, _ *http.Request) {
	stats := h.builderSvc.GetStats()

	resp := StatsResponse{
		SlotsBuilt:     stats.SlotsBuilt,
		BlocksIncluded: stats.BlocksIncluded,
		BidsSubmitted:  stats.BidsSubmitted,
		BidsWon:        stats.BidsWon,
		TotalPaid:      stats.TotalPaid,
		RevealsSuccess: stats.RevealsSuccess,
		RevealsFailed:  stats.RevealsFailed,
		RevealsSkipped: stats.RevealsSkipped,
	}

	if h.builderAPISvc != nil {
		apiStats := h.builderAPISvc.GetRequestStats()
		resp.BuilderAPIHeadersRequested = apiStats.HeadersRequested
		resp.BuilderAPIBlocksPublished = apiStats.BlocksPublished
		resp.BuilderAPIRegisteredValidators = apiStats.ValidatorCount
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetConfig godoc
// @Id getConfig
// @Summary Get buildoor configuration
// @Tags Config
// @Description Returns the buildoor configuration in use. Keys, secrets and the client endpoints are left out; the credentials in the RPC upstream URLs are masked.
// @Produce json
// @Success 200 {object} map[string]interface{} "Success"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /config [get]
func (h *APIHandler) GetConfig(w http.ResponseWriter, _ *http.Request) {
	cfg := h.builderSvc.GetConfig()
	// Redact sensitive fields before returning
	out := configToMap(cfg)
	writeJSON(w, http.StatusOK, out)
}

// UpdateSettingsRequest is a generic path-based settings mutation: keys are
// the canonical settings registry keys (e.g. "epbs.bid_subsidy",
// "schedule.mode", "builder_api.value_override_gwei"), values their JSON
// encodings. Any subset of settings updates in one call.
type UpdateSettingsRequest map[string]json.RawMessage

// UpdateSettings godoc
// @Id updateSettings
// @Summary Update global settings by key path
// @Tags Config
// @Description Applies partial global settings updates keyed by canonical
// @Description settings paths (e.g. {"epbs.bid_subsidy": 1000,
// @Description "schedule.mode": "all"}) without requiring full config
// @Description objects. Unknown keys are rejected. The module enable flags
// @Description (epbs_enabled, builder_api_enabled, lifecycle_enabled) are
// @Description settings too; enabling a module this instance cannot run is
// @Description refused. Requires authentication.
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer token"
// @Param request body object true "Map of settings key paths to values"
// @Success 200 {object} map[string]string "Success"
// @Failure 400 {object} map[string]string "Unknown key or invalid value"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Router /config [patch]
func (h *APIHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPlanUpdateBodyBytes)

	var req UpdateSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if len(req) == 0 {
		writeError(w, http.StatusBadRequest, "no settings provided")
		return
	}

	toggled, err := h.checkModuleToggles(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if !h.applySettings(w, r, token, "config.settings", req, req) {
		return
	}

	if toggled && h.eventStreamMgr != nil {
		h.eventStreamMgr.BroadcastServiceStatus()
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// checkModuleToggles refuses to enable a module this instance cannot run (no
// Gloas fork scheduled, no API port, no lifecycle prerequisites) and reports
// whether the update touches any module enable flag.
func (h *APIHandler) checkModuleToggles(req UpdateSettingsRequest) (bool, error) {
	modules := []struct {
		key       string
		name      string
		available bool
	}{
		{config.KeyEPBSEnabled, "ePBS", h.epbsSvc != nil},
		{config.KeyBuilderAPIEnabled, "the Builder API", h.builderAPISvc != nil},
		{config.KeyLifecycleEnabled, "lifecycle management", h.lifecycleMgr != nil},
	}

	toggled := false

	for _, module := range modules {
		raw, ok := req[module.key]
		if !ok {
			continue
		}

		toggled = true

		var enable bool
		if err := json.Unmarshal(raw, &enable); err != nil {
			return false, fmt.Errorf("%s must be a boolean", module.key)
		}

		if enable && !module.available {
			return false, fmt.Errorf("%s is not available on this instance", module.name)
		}
	}

	return toggled, nil
}

// ValidatorRegistrationResponse represents a formatted validator registration for the UI.
type ValidatorRegistrationResponse struct {
	Pubkey       string `json:"pubkey"`        // Hex-encoded BLS public key
	FeeRecipient string `json:"fee_recipient"` // Hex-encoded Ethereum address
	GasLimit     uint64 `json:"gas_limit"`     // Gas limit for blocks
	Timestamp    uint64 `json:"timestamp"`     // Unix timestamp
}

// GetValidatorsResponse is the response for GetValidators.
type GetValidatorsResponse struct {
	Validators []ValidatorRegistrationResponse `json:"validators"`
}

// GetValidators godoc
// @Id getValidators
// @Summary List registered validators
// @Tags Buildoor
// @Description Returns the list of validators registered via the Builder API (fee recipient preferences). Not paginated.
// @Produce json
// @Success 200 {object} GetValidatorsResponse "Success"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /registrations/validators [get]
func (h *APIHandler) GetValidators(w http.ResponseWriter, _ *http.Request) {
	if h.validatorStore == nil {
		writeJSON(w, http.StatusOK, GetValidatorsResponse{Validators: []ValidatorRegistrationResponse{}})
		return
	}
	regs := h.validatorStore.Values()
	formatted := make([]ValidatorRegistrationResponse, 0, len(regs))
	for _, reg := range regs {
		if reg.Message == nil {
			continue
		}
		formatted = append(formatted, ValidatorRegistrationResponse{
			Pubkey:       fmt.Sprintf("%#x", reg.Message.Pubkey),
			FeeRecipient: fmt.Sprintf("%#x", reg.Message.FeeRecipient),
			GasLimit:     reg.Message.GasLimit,
			Timestamp:    uint64(reg.Message.Timestamp.Unix()),
		})
	}
	writeJSON(w, http.StatusOK, GetValidatorsResponse{Validators: formatted})
}

// BuilderAPIStatusResponse is the response for GetBuilderAPIStatus.
type BuilderAPIStatusResponse struct {
	Enabled               bool  `json:"enabled"`
	ValidatorCount        int   `json:"validator_count"`
	BlockValueSubsidyGwei int64 `json:"block_value_subsidy_gwei"`
}

// GetBuilderAPIStatus godoc
// @Id getBuilderAPIStatus
// @Summary Get Builder API status
// @Tags Buildoor
// @Description Returns the current status of the Builder API including configuration and validator count.
// @Produce json
// @Success 200 {object} BuilderAPIStatusResponse "Success"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /builder-api/status [get]
func (h *APIHandler) GetBuilderAPIStatus(w http.ResponseWriter, _ *http.Request) {
	cfg := h.builderSvc.GetConfig()
	validatorCount := 0
	if h.validatorStore != nil {
		validatorCount = h.validatorStore.Len()
	}

	status := BuilderAPIStatusResponse{
		Enabled:               cfg.BuilderAPIEnabled,
		ValidatorCount:        validatorCount,
		BlockValueSubsidyGwei: cfg.BuilderAPI.BlockValueSubsidyGwei,
	}
	writeJSON(w, http.StatusOK, status)
}

// BidsWonResponse is the response for GetBidsWon.
type BidsWonResponse struct {
	BidsWon []*payload_bidder.WonBlock `json:"bids_won"`
	Total   int                        `json:"total"`
	Offset  int                        `json:"offset"`
	Limit   int                        `json:"limit"`
}

// GetBidsWon godoc
// @Id getBidsWon
// @Summary Get bids won (blocks of ours included on chain)
// @Tags Buildoor
// @Description Returns a paginated list of won blocks (Builder API and p2p ePBS) with transaction counts, blob counts, and values, read from the shared inclusion tracker.
// @Produce json
// @Param offset query int false "Offset for pagination" default(0)
// @Param limit query int false "Limit for pagination (max 100)" default(20)
// @Success 200 {object} BidsWonResponse "Success"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /bids-won [get]
func (h *APIHandler) GetBidsWon(w http.ResponseWriter, r *http.Request) {
	// Parse query params
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	// Defaults and validation
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	bidsWon := []*payload_bidder.WonBlock{}
	total := 0

	// The shared inclusion tracker is the single owner of won-block records
	// (both flows, all forks; persisted via the state-db kv_store when set).
	if h.resultTracker != nil {
		bidsWon, total = h.resultTracker.GetWonBlocks(offset, limit)
	}

	writeJSON(w, http.StatusOK, BidsWonResponse{
		BidsWon: bidsWon,
		Total:   total,
		Offset:  offset,
		Limit:   limit,
	})
}

// ProposerPreferencesEntry represents a single cached proposer preference for the API response.
type ProposerPreferencesEntry struct {
	Slot           uint64 `json:"slot"`
	ValidatorIndex uint64 `json:"validator_index"`
	ClientName     string `json:"client_name,omitempty"`
	FeeRecipient   string `json:"fee_recipient"`
	TargetGasLimit uint64 `json:"target_gas_limit"`
}

// ProposerPreferencesResponse is the response for GetProposerPreferences.
type ProposerPreferencesResponse struct {
	Preferences []ProposerPreferencesEntry `json:"preferences"`
}

// GetProposerPreferences godoc
// @Id getProposerPreferences
// @Summary Get cached proposer preferences
// @Tags Buildoor
// @Description Returns all proposer preferences currently in the cache, received via P2P gossip.
// @Produce json
// @Success 200 {object} ProposerPreferencesResponse "Success"
// @Failure 404 {object} map[string]string "Proposer preferences not enabled"
// @Router /registrations/proposer-preferences [get]
func (h *APIHandler) GetProposerPreferences(w http.ResponseWriter, _ *http.Request) {
	if h.propPrefSvc == nil {
		writeError(w, http.StatusNotFound, "proposer preferences service not enabled")
		return
	}

	entries := h.propPrefSvc.GetStore().Entries()
	result := make([]ProposerPreferencesEntry, 0, len(entries))

	for slot, pref := range entries {
		if pref == nil || pref.Message == nil {
			continue
		}

		entry := ProposerPreferencesEntry{
			Slot:           uint64(slot),
			ValidatorIndex: uint64(pref.Message.ValidatorIndex),
			FeeRecipient:   fmt.Sprintf("0x%x", pref.Message.FeeRecipient[:]),
			TargetGasLimit: pref.Message.TargetGasLimit,
		}
		if h.valRanges != nil {
			entry.ClientName = h.valRanges.GetClientName(pref.Message.ValidatorIndex)
		}
		result = append(result, entry)
	}

	writeJSON(w, http.StatusOK, ProposerPreferencesResponse{Preferences: result})
}

// BuilderPreferencesEntry represents a single cached builder preference for the API response.
type BuilderPreferencesEntry struct {
	ValidatorPubkey     string `json:"validator_pubkey"`
	MaxExecutionPayment uint64 `json:"max_execution_payment"`
}

// BuilderPreferencesResponse is the response for GetBuilderPreferences.
type BuilderPreferencesResponse struct {
	Preferences []BuilderPreferencesEntry `json:"preferences"`
}

// GetBuilderPreferences godoc
// @Id getBuilderPreferences
// @Summary Get cached builder preferences
// @Tags Buildoor
// @Description Returns all builder preferences currently in the cache, submitted by proposers via the submitBuilderPreferences API.
// @Produce json
// @Success 200 {object} BuilderPreferencesResponse "Success"
// @Failure 404 {object} map[string]string "Builder API not enabled"
// @Router /registrations/builder-preferences [get]
func (h *APIHandler) GetBuilderPreferences(w http.ResponseWriter, _ *http.Request) {
	if h.builderAPISvc == nil || h.builderAPISvc.GetBuilderPreferencesStore() == nil {
		writeError(w, http.StatusNotFound, "builder API not enabled")
		return
	}

	entries := h.builderAPISvc.GetBuilderPreferencesStore().GetAll()
	result := make([]BuilderPreferencesEntry, 0, len(entries))
	for pubkey, maxPayment := range entries {
		result = append(result, BuilderPreferencesEntry{
			ValidatorPubkey:     fmt.Sprintf("0x%x", pubkey[:]),
			MaxExecutionPayment: uint64(maxPayment),
		})
	}

	writeJSON(w, http.StatusOK, BuilderPreferencesResponse{Preferences: result})
}

// configToMap returns the public form of the config. It is the only form in
// which the config may leave the process: both the REST endpoint and the
// (unauthenticated) SSE config event serve it. Keys, secrets and the
// startup-only endpoints are json:"-" on the config itself; the upstream
// list, a setting the UI edits, keeps its hosts with the credentials masked.
func configToMap(cfg *config.Config) map[string]any {
	if cfg == nil {
		return nil
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	if txpool, ok := m["txpool"].(map[string]any); ok {
		txpool["rpc_upstreams"] = config.RedactRPCUpstreams(cfg.TxPool.RPCUpstreams)
	}
	return m
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// writeError writes an error response.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
