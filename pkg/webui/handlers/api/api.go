package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/p2p_bidder"
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

// UpdateScheduleRequest is the request for updating schedule config.
type UpdateScheduleRequest struct {
	Mode      string  `json:"mode"`
	EveryNth  uint64  `json:"every_nth,omitempty"`
	NextN     uint64  `json:"next_n,omitempty"`
	StartSlot *uint64 `json:"start_slot,omitempty"`
}

// UpdateEPBSRequest is the request for updating EPBS config.
type UpdateEPBSRequest struct {
	BuildStartTime    *int64  `json:"build_start_time,omitempty"`
	BidStartTime      *int64  `json:"bid_start_time,omitempty"`
	BidEndTime        *int64  `json:"bid_end_time,omitempty"`
	RevealTime        *int64  `json:"reveal_time,omitempty"`
	BidMinAmount      *uint64 `json:"bid_min_amount,omitempty"`
	BidIncrease       *uint64 `json:"bid_increase,omitempty"`
	BidInterval       *int64  `json:"bid_interval,omitempty"`
	PayloadBuildDelay *int64  `json:"payload_build_delay,omitempty"`
	BidSubsidy        *uint64 `json:"bid_subsidy,omitempty"`
}

// UpdateBuilderConfigRequest is the request for updating shared builder config.
type UpdateBuilderConfigRequest struct {
	BuildStartTime    *int64  `json:"build_start_time,omitempty"`
	PayloadBuildDelay *int64  `json:"payload_build_delay,omitempty"`
	ExtraData         *string `json:"extra_data,omitempty"`
}

// UpdateBuilderAPIConfigRequest is the request for updating Builder API config.
type UpdateBuilderAPIConfigRequest struct {
	BlockValueSubsidyGwei *uint64 `json:"block_value_subsidy_gwei,omitempty"`
}

// UpdateLifecycleConfigRequest is the request for updating lifecycle config.
type UpdateLifecycleConfigRequest struct {
	TopupThreshold *uint64 `json:"topup_threshold,omitempty"` // Gwei
	TopupAmount    *uint64 `json:"topup_amount,omitempty"`    // Gwei
}

// LifecycleStatusResponse is the response for lifecycle status.
type LifecycleStatusResponse struct {
	IsRegistered      bool   `json:"is_registered"`
	BuilderIndex      uint64 `json:"builder_index"`
	Balance           uint64 `json:"balance_gwei"`
	EffectiveBalance  uint64 `json:"effective_balance_gwei"`
	PendingPayments   uint64 `json:"pending_payments_gwei"`
	DepositEpoch      uint64 `json:"deposit_epoch"`
	WithdrawableEpoch uint64 `json:"withdrawable_epoch"`
}

// GetVersion godoc
// @Id getVersion
// @Summary Get the current version
// @Tags Version
// @Description Returns the current version
// @Produce json
// @Success 200 {string} string "Success"
// @Failure 500 {string} string "Server Error"
// @Router /api/version [get]
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
// @Router /api/status [get]
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

	// Get wallet info from lifecycle manager (only when lifecycle is enabled)
	if h.lifecycleMgr != nil {
		resp.LifecycleEnabled = true

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
// @Router /api/stats [get]
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
// @Description Returns the buildoor configuration in use. Sensitive fields (builder key, wallet key, JWT secret) are redacted.
// @Produce json
// @Success 200 {object} map[string]interface{} "Success"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /api/config [get]
func (h *APIHandler) GetConfig(w http.ResponseWriter, _ *http.Request) {
	cfg := h.builderSvc.GetConfig()
	// Redact sensitive fields before returning
	out := configToMap(cfg)
	writeJSON(w, http.StatusOK, out)
}

// UpdateSchedule godoc
// @Id updateSchedule
// @Summary Update schedule configuration
// @Tags Config
// @Description Updates the builder schedule configuration including mode, every_nth, and next_n
// @Description settings. Requires authentication.
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer token"
// @Param request body UpdateScheduleRequest true "Schedule configuration"
// @Success 200 {object} map[string]string "Success"
// @Failure 400 {object} map[string]string "Bad Request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /api/config/schedule [post]
func (h *APIHandler) UpdateSchedule(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req UpdateScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	updates := map[string]json.RawMessage{}
	if req.Mode != "" {
		updates[config.KeyScheduleMode] = mustJSON(req.Mode)
	}

	if req.EveryNth > 0 {
		updates[config.KeyScheduleEveryNth] = mustJSON(req.EveryNth)
	}

	if req.NextN > 0 {
		updates[config.KeyScheduleNextN] = mustJSON(req.NextN)
	}

	if req.StartSlot != nil {
		updates[config.KeyScheduleStartSlot] = mustJSON(*req.StartSlot)
	}

	if !h.applySettings(w, r, token, "config.schedule", req, updates) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// UpdateEPBS godoc
// @Id updateEPBS
// @Summary Update EPBS configuration
// @Tags Config
// @Description Updates the EPBS (enshrined PBS) configuration including timing and bid
// @Description parameters. Requires authentication.
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer token"
// @Param request body UpdateEPBSRequest true "EPBS configuration"
// @Success 200 {object} map[string]string "Success"
// @Failure 400 {object} map[string]string "Bad Request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /api/config/epbs [post]
func (h *APIHandler) UpdateEPBS(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req UpdateEPBSRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	updates := map[string]json.RawMessage{}
	if req.BuildStartTime != nil {
		updates[config.KeyEPBSBuildStartTime] = mustJSON(*req.BuildStartTime)
	}

	if req.BidStartTime != nil {
		updates[config.KeyEPBSBidStartTime] = mustJSON(*req.BidStartTime)
	}

	if req.BidEndTime != nil {
		updates[config.KeyEPBSBidEndTime] = mustJSON(*req.BidEndTime)
	}

	if req.RevealTime != nil {
		updates[config.KeyEPBSRevealTime] = mustJSON(*req.RevealTime)
	}

	if req.BidMinAmount != nil {
		updates[config.KeyEPBSBidMinAmount] = mustJSON(*req.BidMinAmount)
	}

	if req.BidIncrease != nil {
		updates[config.KeyEPBSBidIncrease] = mustJSON(*req.BidIncrease)
	}

	if req.BidInterval != nil {
		updates[config.KeyEPBSBidInterval] = mustJSON(*req.BidInterval)
	}

	if req.PayloadBuildDelay != nil {
		updates[config.KeyPayloadBuildTime] = mustJSON(uint64(*req.PayloadBuildDelay))
	}

	if req.BidSubsidy != nil {
		updates[config.KeyEPBSBidSubsidy] = mustJSON(*req.BidSubsidy)
	}

	if !h.applySettings(w, r, token, "config.epbs", req, updates) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// GetLifecycleStatus godoc
// @Id getLifecycleStatus
// @Summary Get lifecycle status
// @Tags Lifecycle
// @Description Returns the builder lifecycle status including registration state, balance,
// @Description pending payments, and epoch information.
// @Produce json
// @Success 200 {object} LifecycleStatusResponse "Success"
// @Failure 404 {object} map[string]string "Lifecycle management not enabled"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /api/lifecycle/status [get]
func (h *APIHandler) GetLifecycleStatus(w http.ResponseWriter, _ *http.Request) {
	if h.lifecycleMgr == nil {
		writeError(w, http.StatusNotFound, "lifecycle management not enabled")
		return
	}

	state := h.lifecycleMgr.GetBuilderState()

	resp := LifecycleStatusResponse{
		IsRegistered:      state.IsRegistered,
		BuilderIndex:      state.Index,
		Balance:           state.Balance,
		DepositEpoch:      state.DepositEpoch,
		WithdrawableEpoch: state.WithdrawableEpoch,
	}

	// Get pending payments from the shared payment tracker
	if h.payments != nil {
		resp.PendingPayments = h.payments.GetTotalPendingPayments()

		if resp.Balance > resp.PendingPayments {
			resp.EffectiveBalance = resp.Balance - resp.PendingPayments
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// PostDeposit godoc
// @Id postDeposit
// @Summary Trigger builder deposit
// @Tags Lifecycle
// @Description Initiates a builder registration deposit. If the builder is not yet
// @Description registered, this will register it with the specified amount. Requires authentication.
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer token"
// @Param request body object{amount_gwei=uint64} true "Deposit amount in gwei"
// @Success 200 {object} map[string]string "Success"
// @Failure 400 {object} map[string]string "Bad Request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 404 {object} map[string]string "Lifecycle management not enabled"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /api/lifecycle/deposit [post]
func (h *APIHandler) PostDeposit(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if h.lifecycleMgr == nil {
		writeError(w, http.StatusNotFound, "lifecycle management not enabled")
		return
	}

	var req struct {
		Amount uint64 `json:"amount_gwei"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.lifecycleMgr.EnsureBuilderRegistered(context.Background()); err != nil {
		h.audit(r, token, "lifecycle.deposit", "", req, "error: "+err.Error())
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	h.audit(r, token, "lifecycle.deposit", "", req, "ok")

	writeJSON(w, http.StatusOK, map[string]string{"status": "deposit initiated"})
}

// PostTopup godoc
// @Id postTopup
// @Summary Trigger balance top-up
// @Tags Lifecycle
// @Description Checks the builder balance and initiates a top-up if needed based on
// @Description configured thresholds. Requires authentication.
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer token"
// @Success 200 {object} map[string]string "Success"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 404 {object} map[string]string "Lifecycle management not enabled"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /api/lifecycle/topup [post]
func (h *APIHandler) PostTopup(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if h.lifecycleMgr == nil {
		writeError(w, http.StatusNotFound, "lifecycle management not enabled")
		return
	}

	if err := h.lifecycleMgr.CheckAndTopup(context.Background()); err != nil {
		h.audit(r, token, "lifecycle.topup", "", nil, "error: "+err.Error())
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	h.audit(r, token, "lifecycle.topup", "", nil, "ok")

	writeJSON(w, http.StatusOK, map[string]string{"status": "topup initiated"})
}

// PostExit godoc
// @Id postExit
// @Summary Trigger voluntary exit
// @Tags Lifecycle
// @Description Initiates a voluntary exit for the builder. This will begin the withdrawal
// @Description process and the builder will stop being eligible for block building after
// @Description the exit is processed. Requires authentication.
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer token"
// @Success 200 {object} map[string]string "Success"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 404 {object} map[string]string "Lifecycle management not enabled"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /api/lifecycle/exit [post]
func (h *APIHandler) PostExit(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if h.lifecycleMgr == nil {
		writeError(w, http.StatusNotFound, "lifecycle management not enabled")
		return
	}

	if err := h.lifecycleMgr.InitiateExit(context.Background()); err != nil {
		h.audit(r, token, "lifecycle.exit", "", nil, "error: "+err.Error())
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	h.audit(r, token, "lifecycle.exit", "", nil, "ok")

	writeJSON(w, http.StatusOK, map[string]string{"status": "exit initiated"})
}

// GetValidators godoc
// @Id getValidators
// @Summary List registered validators
// @Tags Buildoor
// @Description Returns the list of validators registered via the Builder API (fee recipient preferences). Not paginated.
// @Produce json
// @Success 200 {object} map[string][]object "Success"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /api/buildoor/validators [get]
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
// @Router /api/buildoor/validators [get]
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
	Enabled               bool   `json:"enabled"`
	ValidatorCount        int    `json:"validator_count"`
	BlockValueSubsidyGwei uint64 `json:"block_value_subsidy_gwei"`
}

// GetBuilderAPIStatus godoc
// @Id getBuilderAPIStatus
// @Summary Get Builder API status
// @Tags Buildoor
// @Description Returns the current status of the Builder API including configuration and validator count.
// @Produce json
// @Success 200 {object} BuilderAPIStatusResponse "Success"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /api/buildoor/builder-api-status [get]
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

// UpdateBuilderConfig updates the shared builder configuration (build start time, payload build delay).
func (h *APIHandler) UpdateBuilderConfig(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req UpdateBuilderConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	updates := map[string]json.RawMessage{}
	if req.BuildStartTime != nil {
		updates[config.KeyEPBSBuildStartTime] = mustJSON(*req.BuildStartTime)
	}

	if req.PayloadBuildDelay != nil {
		updates[config.KeyPayloadBuildTime] = mustJSON(uint64(*req.PayloadBuildDelay))
	}

	if req.ExtraData != nil {
		updates[config.KeyExtraData] = mustJSON(*req.ExtraData)
	}

	if !h.applySettings(w, r, token, "config.builder", req, updates) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// UpdateBuilderAPIConfig updates the Builder API configuration.
func (h *APIHandler) UpdateBuilderAPIConfig(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req UpdateBuilderAPIConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	updates := map[string]json.RawMessage{}
	if req.BlockValueSubsidyGwei != nil {
		updates[config.KeyBuilderAPISubsidy] = mustJSON(*req.BlockValueSubsidyGwei)
	}

	if !h.applySettings(w, r, token, "config.builder-api", req, updates) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// UpdateLifecycleConfig updates the lifecycle configuration (topup threshold/amount).
func (h *APIHandler) UpdateLifecycleConfig(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req UpdateLifecycleConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	updates := map[string]json.RawMessage{}
	if req.TopupThreshold != nil {
		updates[config.KeyTopupThreshold] = mustJSON(*req.TopupThreshold)
	}

	if req.TopupAmount != nil {
		updates[config.KeyTopupAmount] = mustJSON(*req.TopupAmount)
	}

	if !h.applySettings(w, r, token, "config.lifecycle", req, updates) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ToggleServiceRequest is the request for toggling services.
type ToggleServiceRequest struct {
	EPBSEnabled       *bool `json:"epbs_enabled,omitempty"`
	BuilderAPIEnabled *bool `json:"builder_api_enabled,omitempty"`
	LifecycleEnabled  *bool `json:"lifecycle_enabled,omitempty"`
}

// ToggleServices toggles the enabled state of ePBS and/or Builder API services.
func (h *APIHandler) ToggleServices(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req ToggleServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// The enable flags are ordinary settings; persist them through the settings
	// service (which triggers the module SetEnabled callbacks via OnChange).
	// Only toggle modules that are actually available.
	updates := map[string]json.RawMessage{}
	if req.EPBSEnabled != nil && h.epbsSvc != nil {
		updates[config.KeyEPBSEnabled] = mustJSON(*req.EPBSEnabled)
	}

	if req.BuilderAPIEnabled != nil && h.builderAPISvc != nil {
		updates[config.KeyBuilderAPIEnabled] = mustJSON(*req.BuilderAPIEnabled)
	}

	if req.LifecycleEnabled != nil && h.lifecycleMgr != nil {
		updates[config.KeyLifecycleEnabled] = mustJSON(*req.LifecycleEnabled)
	}

	if len(updates) > 0 {
		if err := h.settingsSvc.SetMany(updates, actorFromToken(token)); err != nil {
			h.audit(r, token, "services.toggle", "", req, "error: "+err.Error())
			writeError(w, http.StatusBadRequest, err.Error())

			return
		}
	}

	h.audit(r, token, "services.toggle", "", req, "ok")

	// Broadcast updated status to all connected clients
	if h.eventStreamMgr != nil {
		h.eventStreamMgr.BroadcastServiceStatus()
	}

	// Return current status
	regState := "unknown"
	if h.epbsSvc != nil {
		regState = p2p_bidder.RegistrationStateName(h.epbsSvc.GetRegistrationState())
	}

	status := ServiceStatusEvent{
		EPBSAvailable:         h.epbsSvc != nil,
		EPBSEnabled:           h.epbsSvc != nil && h.epbsSvc.IsEnabled(),
		EPBSRegistrationState: regState,
		BuilderAPIAvailable:   h.builderAPISvc != nil,
		BuilderAPIEnabled:     h.builderAPISvc != nil && h.builderAPISvc.IsEnabled(),
		LifecycleAvailable:    h.lifecycleMgr != nil,
		LifecycleEnabled:      h.lifecycleMgr != nil && h.lifecycleMgr.IsEnabled(),
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
// @Router /api/buildoor/bids-won [get]
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
	if h.inclusionTracker != nil {
		bidsWon, total = h.inclusionTracker.GetWonBlocks(offset, limit)
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
// @Router /api/buildoor/proposer-preferences [get]
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
// @Router /api/buildoor/builder-preferences [get]
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

// configToMap returns the config as a map with sensitive fields redacted.
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
	redact := func(key string) {
		if v, ok := m[key].(string); ok && v != "" {
			m[key] = "***"
		}
	}
	redact("builder_privkey")
	redact("wallet_privkey")
	redact("el_jwt_secret")
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
