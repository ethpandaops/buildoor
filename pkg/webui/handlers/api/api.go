package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/builderapi"
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
	BidsSubmitted  uint64 `json:"bids_submitted"`
	BidsWon        uint64 `json:"bids_won"`
	TotalPaid      uint64 `json:"total_paid_gwei"`
	RevealsSuccess uint64 `json:"reveals_success"`
	RevealsFailed  uint64 `json:"reveals_failed"`
	RevealsSkipped uint64 `json:"reveals_skipped"`
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
}

// UpdateBuilderConfigRequest is the request for updating shared builder config.
type UpdateBuilderConfigRequest struct {
	BuildStartTime    *int64 `json:"build_start_time,omitempty"`
	PayloadBuildDelay *int64 `json:"payload_build_delay,omitempty"`
}

// UpdateBuilderAPIConfigRequest is the request for updating Builder API config.
type UpdateBuilderAPIConfigRequest struct {
	UseProposerFeeRecipient *bool   `json:"use_proposer_fee_recipient,omitempty"`
	BlockValueSubsidyGwei   *uint64 `json:"block_value_subsidy_gwei,omitempty"`
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

	// Get builder index and pubkey from ePBS service if available
	if h.epbsSvc != nil {
		resp.BuilderIndex = h.epbsSvc.GetBuilderIndex()
		pubkey := h.epbsSvc.GetBuilderPubkey()
		resp.BuilderPubkey = pubkey.String()

		// Get pending payments from bid tracker
		if tracker := h.epbsSvc.GetBidTracker(); tracker != nil {
			resp.PendingPayments = tracker.GetTotalPendingPayments()
		}
	}

	// Get lifecycle info if available
	if h.lifecycleMgr != nil {
		resp.LifecycleEnabled = true
		state := h.lifecycleMgr.GetBuilderState()

		if state != nil {
			resp.IsRegistered = state.IsRegistered
			resp.CLBalance = state.Balance
			resp.DepositEpoch = state.DepositEpoch
			resp.WithdrawableEpoch = state.WithdrawableEpoch

			// Calculate effective balance
			if resp.CLBalance > resp.PendingPayments {
				resp.EffectiveBalance = resp.CLBalance - resp.PendingPayments
			}
		}

		// Get wallet info
		if wallet := h.lifecycleMgr.GetWallet(); wallet != nil {
			resp.WalletAddress = wallet.Address().Hex()

			// Get wallet balance
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
		BidsSubmitted:  stats.BidsSubmitted,
		BidsWon:        stats.BidsWon,
		TotalPaid:      stats.TotalPaid,
		RevealsSuccess: stats.RevealsSuccess,
		RevealsFailed:  stats.RevealsFailed,
		RevealsSkipped: stats.RevealsSkipped,
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

	cfg := h.builderSvc.GetConfig()
	cfg.Schedule.Mode = builder.ScheduleMode(req.Mode)

	if req.EveryNth > 0 {
		cfg.Schedule.EveryNth = req.EveryNth
	}

	if req.NextN > 0 {
		cfg.Schedule.NextN = req.NextN
	}

	if req.StartSlot != nil {
		cfg.Schedule.StartSlot = *req.StartSlot
	}

	if err := h.builderSvc.UpdateConfig(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Broadcast config update to connected clients
	if h.eventStreamMgr != nil {
		h.eventStreamMgr.BroadcastConfigUpdate()
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

	cfg := h.builderSvc.GetConfig()

	if req.BuildStartTime != nil {
		cfg.EPBS.BuildStartTime = *req.BuildStartTime
	}

	if req.BidStartTime != nil {
		cfg.EPBS.BidStartTime = *req.BidStartTime
	}

	if req.BidEndTime != nil {
		cfg.EPBS.BidEndTime = *req.BidEndTime
	}

	if req.RevealTime != nil {
		cfg.EPBS.RevealTime = *req.RevealTime
	}

	if req.BidMinAmount != nil {
		cfg.EPBS.BidMinAmount = *req.BidMinAmount
	}

	if req.BidIncrease != nil {
		cfg.EPBS.BidIncrease = *req.BidIncrease
	}

	if req.BidInterval != nil {
		cfg.EPBS.BidInterval = *req.BidInterval
	}

	if req.PayloadBuildDelay != nil {
		cfg.PayloadBuildTime = uint64(*req.PayloadBuildDelay)
	}

	if err := h.builderSvc.UpdateConfig(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Also update EPBS service if available
	if h.epbsSvc != nil {
		h.epbsSvc.UpdateConfig(&cfg.EPBS)
	}

	// Broadcast config update to connected clients
	if h.eventStreamMgr != nil {
		h.eventStreamMgr.BroadcastConfigUpdate()
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

	// Get pending payments from bid tracker
	if h.epbsSvc != nil {
		if tracker := h.epbsSvc.GetBidTracker(); tracker != nil {
			resp.PendingPayments = tracker.GetTotalPendingPayments()

			if resp.Balance > resp.PendingPayments {
				resp.EffectiveBalance = resp.Balance - resp.PendingPayments
			}
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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

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
	regs := h.validatorStore.List()
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
	Enabled                 bool   `json:"enabled"`
	Port                    int    `json:"port"`
	ValidatorCount          int    `json:"validator_count"`
	UseProposerFeeRecipient bool   `json:"use_proposer_fee_recipient"`
	BlockValueSubsidyGwei   uint64 `json:"block_value_subsidy_gwei"`
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
		Enabled:                 cfg.BuilderAPIEnabled,
		Port:                    cfg.BuilderAPI.Port,
		ValidatorCount:          validatorCount,
		UseProposerFeeRecipient: cfg.BuilderAPI.UseProposerFeeRecipient,
		BlockValueSubsidyGwei:   cfg.BuilderAPI.BlockValueSubsidyGwei,
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

	cfg := h.builderSvc.GetConfig()

	if req.BuildStartTime != nil {
		cfg.EPBS.BuildStartTime = *req.BuildStartTime
	}

	if req.PayloadBuildDelay != nil {
		cfg.PayloadBuildTime = uint64(*req.PayloadBuildDelay)
	}

	if err := h.builderSvc.UpdateConfig(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Broadcast config update to connected clients
	if h.eventStreamMgr != nil {
		h.eventStreamMgr.BroadcastConfigUpdate()
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

	cfg := h.builderSvc.GetConfig()

	if req.UseProposerFeeRecipient != nil {
		cfg.BuilderAPI.UseProposerFeeRecipient = *req.UseProposerFeeRecipient
	}

	if req.BlockValueSubsidyGwei != nil {
		cfg.BuilderAPI.BlockValueSubsidyGwei = *req.BlockValueSubsidyGwei
	}

	if err := h.builderSvc.UpdateConfig(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Broadcast config update to connected clients
	if h.eventStreamMgr != nil {
		h.eventStreamMgr.BroadcastConfigUpdate()
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ToggleServiceRequest is the request for toggling services.
type ToggleServiceRequest struct {
	EPBSEnabled   *bool `json:"epbs_enabled,omitempty"`
	LegacyEnabled *bool `json:"legacy_enabled,omitempty"`
}

// ToggleServices toggles the enabled state of ePBS and/or legacy builder services.
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

	if req.EPBSEnabled != nil && h.epbsSvc != nil {
		h.epbsSvc.SetEnabled(*req.EPBSEnabled)
	}

	if req.LegacyEnabled != nil && h.builderAPISvc != nil {
		h.builderAPISvc.SetEnabled(*req.LegacyEnabled)
	}

	// Broadcast updated status to all connected clients
	if h.eventStreamMgr != nil {
		h.eventStreamMgr.BroadcastServiceStatus()
	}

	// Return current status
	status := ServiceStatusEvent{
		EPBSAvailable:   h.epbsSvc != nil,
		EPBSEnabled:     h.epbsSvc != nil && h.epbsSvc.IsEnabled(),
		LegacyAvailable: h.builderAPISvc != nil,
		LegacyEnabled:   h.builderAPISvc != nil && h.builderAPISvc.IsEnabled(),
	}
	writeJSON(w, http.StatusOK, status)
}

// BidsWonResponse is the response for GetBidsWon.
type BidsWonResponse struct {
	BidsWon []builderapi.BidWonEntry `json:"bids_won"`
	Total   int                      `json:"total"`
	Offset  int                      `json:"offset"`
	Limit   int                      `json:"limit"`
}

// GetBidsWon godoc
// @Id getBidsWon
// @Summary Get bids won (successfully delivered blocks)
// @Tags Buildoor
// @Description Returns a paginated list of bids won via Builder API with transaction counts, blob counts, and values.
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

	// Get data from store
	var bidsWon []builderapi.BidWonEntry
	var total int

	if h.builderAPISvc != nil && h.builderAPISvc.GetBidsWonStore() != nil {
		bidsWon, total = h.builderAPISvc.GetBidsWonStore().GetPage(offset, limit)
	} else {
		bidsWon = []builderapi.BidWonEntry{}
		total = 0
	}

	// Build response
	response := BidsWonResponse{
		BidsWon: bidsWon,
		Total:   total,
		Offset:  offset,
		Limit:   limit,
	}

	writeJSON(w, http.StatusOK, response)
}

// configToMap returns the config as a map with sensitive fields redacted.
func configToMap(cfg *builder.Config) map[string]any {
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
