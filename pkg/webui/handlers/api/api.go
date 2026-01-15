package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/version"
)

// StatusResponse is the response for the status endpoint.
type StatusResponse struct {
	Running       bool   `json:"running"`
	BuilderIndex  uint64 `json:"builder_index"`
	BuilderPubkey string `json:"builder_pubkey"`
	CurrentSlot   uint64 `json:"current_slot"`
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
	BuildStartTime *int64  `json:"build_start_time,omitempty"`
	BidStartTime   *int64  `json:"bid_start_time,omitempty"`
	BidEndTime     *int64  `json:"bid_end_time,omitempty"`
	RevealTime     *int64  `json:"reveal_time,omitempty"`
	BidMinAmount   *uint64 `json:"bid_min_amount,omitempty"`
	BidIncrease    *uint64 `json:"bid_increase,omitempty"`
	BidInterval    *int64  `json:"bid_interval,omitempty"`
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

// GetStatus returns the current builder status.
func (h *APIHandler) GetStatus(w http.ResponseWriter, _ *http.Request) {
	resp := StatusResponse{
		Running:     true,
		CurrentSlot: uint64(h.builderSvc.GetCurrentSlot()),
	}

	// Get builder index and pubkey from ePBS service if available
	if h.epbsSvc != nil {
		resp.BuilderIndex = h.epbsSvc.GetBuilderIndex()
		pubkey := h.epbsSvc.GetBuilderPubkey()
		resp.BuilderPubkey = pubkey.String()
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetStats returns builder statistics.
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

// GetConfig returns the current configuration.
func (h *APIHandler) GetConfig(w http.ResponseWriter, _ *http.Request) {
	cfg := h.builderSvc.GetConfig()
	writeJSON(w, http.StatusOK, cfg)
}

// UpdateSchedule updates the schedule configuration.
func (h *APIHandler) UpdateSchedule(w http.ResponseWriter, r *http.Request) {
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

// UpdateEPBS updates the EPBS configuration.
func (h *APIHandler) UpdateEPBS(w http.ResponseWriter, r *http.Request) {
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

// GetLifecycleStatus returns the lifecycle status.
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

// PostDeposit triggers a deposit.
func (h *APIHandler) PostDeposit(w http.ResponseWriter, r *http.Request) {
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

// PostTopup triggers a balance top-up.
func (h *APIHandler) PostTopup(w http.ResponseWriter, _ *http.Request) {
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

// PostExit triggers a voluntary exit.
func (h *APIHandler) PostExit(w http.ResponseWriter, _ *http.Request) {
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
