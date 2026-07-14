package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/slot_results"
)

// maxSlotRangeEpochs caps the slot span one range query may request.
const maxSlotRangeEpochs = 320

// maxPlanUpdateBodyBytes bounds action plan update request bodies.
const maxPlanUpdateBodyBytes = 1 << 20 // 1 MiB

// ActionPlanResponse is the response of the action plan range query.
type ActionPlanResponse struct {
	Plans   []*action_plan.SlotPlan `json:"plans"`
	MinSlot uint64                  `json:"min_slot"`
	MaxSlot uint64                  `json:"max_slot"`
}

// UpdateActionPlanRequest is the bulk plan mutation request. Each update
// targets explicit slots and/or an inclusive range; category members are
// three-state (absent/null/object) and the "set" member applies fine-grained
// path updates (e.g. "bid.bid_min_amount") so partial edits never require the
// full category object.
type UpdateActionPlanRequest struct {
	Updates []*action_plan.PlanUpdate `json:"updates"`
}

// UpdateActionPlanResponse returns the authoritative normalized result of a
// committed plan mutation: the changed slots with their resulting plans (a
// nil plan means the slot's plan was deleted).
type UpdateActionPlanResponse struct {
	Status string                  `json:"status"`
	Slots  []uint64                `json:"slots"`
	Plans  []*action_plan.SlotPlan `json:"plans"`
}

// SlotResultsResponse is the response of the slot results range query.
type SlotResultsResponse struct {
	Results []*slot_results.SlotResult `json:"results"`
	MinSlot uint64                     `json:"min_slot"`
	MaxSlot uint64                     `json:"max_slot"`
}

// parseSlotRange reads and validates the min_slot/max_slot query parameters
// (both required, inclusive), bounding the span to maxSlotRangeEpochs.
func (h *APIHandler) parseSlotRange(w http.ResponseWriter, r *http.Request) (minSlot, maxSlot uint64, ok bool) {
	minStr := r.URL.Query().Get("min_slot")
	maxStr := r.URL.Query().Get("max_slot")

	if minStr == "" || maxStr == "" {
		writeError(w, http.StatusBadRequest, "min_slot and max_slot query parameters are required")
		return 0, 0, false
	}

	minSlot, err := strconv.ParseUint(minStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid min_slot: must be a number")
		return 0, 0, false
	}

	maxSlot, err = strconv.ParseUint(maxStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid max_slot: must be a number")
		return 0, 0, false
	}

	if maxSlot < minSlot {
		writeError(w, http.StatusBadRequest, "max_slot must be >= min_slot")
		return 0, 0, false
	}

	slotsPerEpoch := uint64(32)
	if h.chainSvc != nil {
		if spec := h.chainSvc.GetChainSpec(); spec != nil && spec.SlotsPerEpoch > 0 {
			slotsPerEpoch = spec.SlotsPerEpoch
		}
	}

	if maxRange := maxSlotRangeEpochs * slotsPerEpoch; maxSlot-minSlot+1 > maxRange {
		writeError(w, http.StatusBadRequest,
			"slot range too large: max "+strconv.FormatUint(maxRange, 10)+" slots per request")
		return 0, 0, false
	}

	return minSlot, maxSlot, true
}

// GetActionPlan godoc
// @Id getActionPlan
// @Summary Get per-slot action plans
// @Tags ActionPlan
// @Description Returns all per-slot action plans within the inclusive slot range.
// @Produce json
// @Param min_slot query int true "Range start slot (inclusive)"
// @Param max_slot query int true "Range end slot (inclusive)"
// @Success 200 {object} ActionPlanResponse
// @Failure 400 {object} map[string]string "Bad Request"
// @Failure 503 {object} map[string]string "Plan service unavailable"
// @Router /api/buildoor/action-plan [get]
func (h *APIHandler) GetActionPlan(w http.ResponseWriter, r *http.Request) {
	if h.planSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "action plan service not available")
		return
	}

	minSlot, maxSlot, ok := h.parseSlotRange(w, r)
	if !ok {
		return
	}

	plans := h.planSvc.GetRange(phase0.Slot(minSlot), phase0.Slot(maxSlot))
	if plans == nil {
		plans = []*action_plan.SlotPlan{}
	}

	writeJSON(w, http.StatusOK, &ActionPlanResponse{
		Plans:   plans,
		MinSlot: minSlot,
		MaxSlot: maxSlot,
	})
}

// UpdateActionPlan godoc
// @Id updateActionPlan
// @Summary Update per-slot action plans
// @Tags ActionPlan
// @Description Applies a bulk per-slot action plan mutation atomically (all
// @Description targeted slots update or none). Category members are
// @Description three-state (absent = unchanged, null = clear, object =
// @Description replace); the "set" member supports fine-grained path updates
// @Description like {"bid.bid_min_amount": 5000} for partial edits. Updates
// @Description targeting past or already-frozen slots fail with 409.
// @Description Requires authentication.
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer token"
// @Param request body UpdateActionPlanRequest true "Plan updates"
// @Success 200 {object} UpdateActionPlanResponse "Authoritative normalized result"
// @Failure 400 {object} map[string]string "Validation error"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 409 {object} map[string]string "Slot in the past or frozen"
// @Failure 503 {object} map[string]string "Plan service unavailable"
// @Router /api/buildoor/action-plan [post]
func (h *APIHandler) UpdateActionPlan(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if h.planSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "action plan service not available")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPlanUpdateBodyBytes)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	var req UpdateActionPlanRequest
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	change, err := h.planSvc.ApplyUpdates(req.Updates, actorFromToken(token))
	if err != nil {
		h.audit(r, token, "action_plan.update", "", req, "error: "+err.Error())

		if errors.Is(err, action_plan.ErrSlotLocked) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}

		return
	}

	h.audit(r, token, "action_plan.update", "", req, "ok")

	writeJSON(w, http.StatusOK, &UpdateActionPlanResponse{
		Status: "updated",
		Slots:  change.Slots,
		Plans:  change.Plans,
	})
}

// GetSlotResults godoc
// @Id getSlotResults
// @Summary Get per-slot results
// @Tags ActionPlan
// @Description Returns the recorded outcome history (build, bids, block
// @Description submissions, reveals, inclusion, applied plan) for every
// @Description active slot within the inclusive slot range.
// @Produce json
// @Param min_slot query int true "Range start slot (inclusive)"
// @Param max_slot query int true "Range end slot (inclusive)"
// @Success 200 {object} SlotResultsResponse
// @Failure 400 {object} map[string]string "Bad Request"
// @Failure 503 {object} map[string]string "Results tracker unavailable"
// @Router /api/buildoor/slot-results [get]
func (h *APIHandler) GetSlotResults(w http.ResponseWriter, r *http.Request) {
	if h.resultTracker == nil {
		writeError(w, http.StatusServiceUnavailable, "slot results tracker not available")
		return
	}

	minSlot, maxSlot, ok := h.parseSlotRange(w, r)
	if !ok {
		return
	}

	results := h.resultTracker.GetRange(phase0.Slot(minSlot), phase0.Slot(maxSlot))
	if results == nil {
		results = []*slot_results.SlotResult{}
	}

	writeJSON(w, http.StatusOK, &SlotResultsResponse{
		Results: results,
		MinSlot: minSlot,
		MaxSlot: maxSlot,
	})
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
// @Description objects. Unknown keys are rejected. Requires authentication.
// @Accept json
// @Produce json
// @Param Authorization header string true "Bearer token"
// @Param request body object true "Map of settings key paths to values"
// @Success 200 {object} map[string]string "Success"
// @Failure 400 {object} map[string]string "Unknown key or invalid value"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Router /api/config/settings [post]
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

	if !h.applySettings(w, r, token, "config.settings", req, req) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
