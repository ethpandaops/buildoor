package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/config"
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
	WalletNonce       uint64 `json:"wallet_nonce,omitempty"`
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
	Mode     string `json:"mode"`
	EveryNth uint64 `json:"every_nth,omitempty"`
	NextN    uint64 `json:"next_n,omitempty"`
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
			resp.WalletNonce = wallet.PendingNonce()

			// Get wallet balance
			if balance, err := wallet.GetBalance(r.Context()); err == nil && balance != nil {
				resp.WalletBalance = balance.String()
			}
		}
	}

	// If lifecycle isn't providing wallet details, fall back to legacy builder wallet (when configured).
	if resp.WalletAddress == "" && h.legacyBuilderSvc != nil {
		if wallet := h.legacyBuilderSvc.GetWallet(); wallet != nil {
			resp.WalletAddress = wallet.Address().Hex()
			resp.WalletNonce = h.legacyBuilderSvc.GetConfirmedNonce()

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
// @Summary Get current configuration
// @Tags Config
// @Description Returns the current builder configuration including schedule and EPBS settings.
// @Produce json
// @Success 200 {object} builder.Config "Success"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /api/config [get]
func (h *APIHandler) GetConfig(w http.ResponseWriter, _ *http.Request) {
	cfg := h.builderSvc.GetConfig()
	writeJSON(w, http.StatusOK, cfg)
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
		cfg.EPBS.PayloadBuildDelay = *req.PayloadBuildDelay
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

// LegacyBuilderStatusResponse is the response for the legacy builder status endpoint.
type LegacyBuilderStatusResponse struct {
	Enabled            bool     `json:"enabled"`
	RelayURLs          []string `json:"relay_urls"`
	ValidatorsTracked  uint64   `json:"validators_tracked"`
	BlocksSubmitted    uint64   `json:"blocks_submitted"`
	BlocksAccepted     uint64   `json:"blocks_accepted"`
	SubmissionFailures uint64   `json:"submission_failures"`
	ScheduleMode       string   `json:"schedule_mode"`
	ScheduleEveryNth   uint64   `json:"schedule_every_nth"`
	ScheduleNextN      uint64   `json:"schedule_next_n"`
	SubmitStartTime    int64    `json:"submit_start_time"`
	SubmitEndTime      int64    `json:"submit_end_time"`
	SubmitInterval     int64    `json:"submit_interval"`
	BidIncrease        uint64   `json:"bid_increase"`
	PayloadBuildDelay  int64    `json:"payload_build_delay"`
	PaymentMode        string   `json:"payment_mode"`
	FixedPayment       string   `json:"fixed_payment,omitempty"`
	PaymentPercentage  uint64   `json:"payment_percentage,omitempty"`
}

// UpdateLegacyBuilderRequest is the request for updating legacy builder config.
type UpdateLegacyBuilderRequest struct {
	ScheduleMode      *string `json:"schedule_mode,omitempty"`
	ScheduleEveryNth  *uint64 `json:"schedule_every_nth,omitempty"`
	ScheduleNextN     *uint64 `json:"schedule_next_n,omitempty"`
	SubmitStartTime   *int64  `json:"submit_start_time,omitempty"`
	SubmitEndTime     *int64  `json:"submit_end_time,omitempty"`
	SubmitInterval    *int64  `json:"submit_interval,omitempty"`
	BidIncrease       *uint64 `json:"bid_increase,omitempty"`
	PayloadBuildDelay *int64  `json:"payload_build_delay,omitempty"`
	PaymentMode       *string `json:"payment_mode,omitempty"`
	FixedPayment      *string `json:"fixed_payment,omitempty"`
	PaymentPercentage *uint64 `json:"payment_percentage,omitempty"`
}

// GetLegacyBuilderStatus returns the legacy builder status and statistics.
func (h *APIHandler) GetLegacyBuilderStatus(w http.ResponseWriter, _ *http.Request) {
	if h.legacyBuilderSvc == nil {
		writeError(w, http.StatusNotFound, "legacy builder not enabled")
		return
	}

	stats := h.legacyBuilderSvc.GetStats()
	cfg := h.legacyBuilderSvc.GetConfig()

	resp := LegacyBuilderStatusResponse{
		Enabled:            true,
		RelayURLs:          cfg.RelayURLs,
		ValidatorsTracked:  stats.ValidatorsTracked,
		BlocksSubmitted:    stats.BlocksSubmitted,
		BlocksAccepted:     stats.BlocksAccepted,
		SubmissionFailures: stats.SubmissionFailures,
		ScheduleMode:       string(cfg.Schedule.Mode),
		ScheduleEveryNth:   cfg.Schedule.EveryNth,
		ScheduleNextN:      cfg.Schedule.NextN,
		SubmitStartTime:    cfg.SubmitStartTime,
		SubmitEndTime:      cfg.SubmitEndTime,
		SubmitInterval:     cfg.SubmitInterval,
		BidIncrease:        cfg.BidIncrease,
		PayloadBuildDelay:  cfg.PayloadBuildDelay,
		PaymentMode:        cfg.PaymentMode,
		FixedPayment:       cfg.FixedPayment,
		PaymentPercentage:  cfg.PaymentPercentage,
	}

	writeJSON(w, http.StatusOK, resp)
}

// UpdateLegacyBuilder updates the legacy builder configuration at runtime.
func (h *APIHandler) UpdateLegacyBuilder(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if h.legacyBuilderSvc == nil {
		writeError(w, http.StatusNotFound, "legacy builder not enabled")
		return
	}

	var req UpdateLegacyBuilderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	cfg := h.legacyBuilderSvc.GetConfig()

	if req.ScheduleMode != nil {
		cfg.Schedule.Mode = config.ScheduleMode(*req.ScheduleMode)
	}

	if req.ScheduleEveryNth != nil {
		cfg.Schedule.EveryNth = *req.ScheduleEveryNth
	}

	if req.ScheduleNextN != nil {
		cfg.Schedule.NextN = *req.ScheduleNextN
	}

	if req.SubmitStartTime != nil {
		cfg.SubmitStartTime = *req.SubmitStartTime
	}

	if req.SubmitEndTime != nil {
		cfg.SubmitEndTime = *req.SubmitEndTime
	}

	if req.SubmitInterval != nil {
		cfg.SubmitInterval = *req.SubmitInterval
	}

	if req.BidIncrease != nil {
		cfg.BidIncrease = *req.BidIncrease
	}

	if req.PayloadBuildDelay != nil {
		cfg.PayloadBuildDelay = *req.PayloadBuildDelay
	}

	if req.PaymentMode != nil {
		cfg.PaymentMode = *req.PaymentMode
	}

	if req.FixedPayment != nil {
		cfg.FixedPayment = *req.FixedPayment
	}

	if req.PaymentPercentage != nil {
		cfg.PaymentPercentage = *req.PaymentPercentage
	}

	h.legacyBuilderSvc.UpdateConfig(cfg)

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

// ToggleServiceResponse is the response for the toggle endpoint.
type ToggleServiceResponse struct {
	EPBSAvailable   bool `json:"epbs_available"`
	EPBSEnabled     bool `json:"epbs_enabled"`
	LegacyAvailable bool `json:"legacy_available"`
	LegacyEnabled   bool `json:"legacy_enabled"`
}

// ToggleServices enables or disables ePBS and/or legacy builder at runtime.
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

	if req.LegacyEnabled != nil && h.legacyBuilderSvc != nil {
		h.legacyBuilderSvc.SetEnabled(*req.LegacyEnabled)
	}

	// Broadcast updated info to connected clients
	if h.eventStreamMgr != nil {
		h.eventStreamMgr.BroadcastServiceStatus()
	}

	resp := ToggleServiceResponse{
		EPBSAvailable:   h.epbsSvc != nil,
		LegacyAvailable: h.legacyBuilderSvc != nil,
	}
	if h.epbsSvc != nil {
		resp.EPBSEnabled = h.epbsSvc.IsEnabled()
	}
	if h.legacyBuilderSvc != nil {
		resp.LegacyEnabled = h.legacyBuilderSvc.IsEnabled()
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetServiceStatus returns the current enabled/disabled status of services.
func (h *APIHandler) GetServiceStatus(w http.ResponseWriter, _ *http.Request) {
	resp := ToggleServiceResponse{
		EPBSAvailable:   h.epbsSvc != nil,
		LegacyAvailable: h.legacyBuilderSvc != nil,
	}
	if h.epbsSvc != nil {
		resp.EPBSEnabled = h.epbsSvc.IsEnabled()
	}
	if h.legacyBuilderSvc != nil {
		resp.LegacyEnabled = h.legacyBuilderSvc.IsEnabled()
	}

	writeJSON(w, http.StatusOK, resp)
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
