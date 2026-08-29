package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/mux"

	"github.com/ethpandaops/buildoor/pkg/builder_keys"
	"github.com/ethpandaops/buildoor/pkg/config"
)

// BuilderKeysResponse is the full view of the managed builder key set.
type BuilderKeysResponse struct {
	Keys      []*builder_keys.State  `json:"keys"`
	Aggregate builder_keys.Aggregate `json:"aggregate"`
	Settings  BuilderKeysSettings    `json:"settings"`
}

// BuilderKeysSettings mirrors the mutable key-set configuration so the UI can
// render and edit it without a second request.
type BuilderKeysSettings struct {
	TargetCount uint64 `json:"target_count"`
	MaxIndex    uint64 `json:"max_index"`
	AutoDeposit bool   `json:"auto_deposit"`
	AutoExit    bool   `json:"auto_exit"`
}

// UpdateBuilderKeyTargetRequest sets how many builder keys are kept registered
// and funded.
type UpdateBuilderKeyTargetRequest struct {
	Target uint64 `json:"target"`
}

// BuilderKeyExitRequest asks for a builder key exit. LowerTarget (the default
// from the UI) decrements the target count in the same write, so the reconciler
// does not immediately deposit a replacement.
type BuilderKeyExitRequest struct {
	LowerTarget bool `json:"lower_target"`
}

// BuilderKeyTopupRequest tops a key up; AmountGwei of 0 uses the configured
// top-up amount.
type BuilderKeyTopupRequest struct {
	AmountGwei uint64 `json:"amount_gwei,omitempty"`
}

// keyRegistry returns the managed key set. It is available even without
// lifecycle management — the keys are derived either way; only deposits, exits
// and top-ups need the lifecycle manager.
func (h *APIHandler) keyRegistry() *builder_keys.Registry {
	return h.keys
}

// requireLifecycle reports whether lifecycle management is available, writing a
// 404 when it is not. Every mutating key operation needs it.
func (h *APIHandler) requireLifecycle(w http.ResponseWriter) bool {
	if h.lifecycleMgr == nil {
		writeError(w, http.StatusNotFound, "lifecycle management not enabled")
		return false
	}

	return true
}

// GetBuilderKeys godoc
// @Id getBuilderKeys
// @Summary Get the managed builder key set
// @Tags Builder Keys
// @Description Returns every managed builder key with its lifecycle status,
// @Description on-chain builder index, balances and usage history, plus fleet
// @Description aggregates and the mutable key-set settings.
// @Produce json
// @Success 200 {object} BuilderKeysResponse
// @Failure 404 {object} map[string]string "Lifecycle management not enabled"
// @Router /api/buildoor/builder-keys [get]
func (h *APIHandler) GetBuilderKeys(w http.ResponseWriter, _ *http.Request) {
	registry := h.keyRegistry()
	if registry == nil {
		writeError(w, http.StatusNotFound, "builder key registry not available")
		return
	}

	writeJSON(w, http.StatusOK, &BuilderKeysResponse{
		Keys:      registry.States(),
		Aggregate: registry.Aggregate(),
		Settings:  h.builderKeysSettings(),
	})
}

// builderKeysSettings snapshots the mutable key-set configuration.
func (h *APIHandler) builderKeysSettings() BuilderKeysSettings {
	cfg := h.settingsSvc.Current()

	return BuilderKeysSettings{
		TargetCount: cfg.BuilderKeys.EffectiveTargetCount(),
		MaxIndex:    cfg.BuilderKeys.MaxIndex,
		AutoDeposit: cfg.BuilderKeys.AutoDeposit,
		AutoExit:    cfg.BuilderKeys.AutoExit,
	}
}

// UpdateBuilderKeyTarget godoc
// @Id updateBuilderKeyTarget
// @Summary Set the target builder key count
// @Tags Builder Keys
// @Description Sets how many builder keys are kept registered and funded.
// @Description Raising it deposits new keys; lowering it exits surplus keys
// @Description when auto-exit is on — an exited key can never be reactivated.
// @Accept json
// @Produce json
// @Param request body UpdateBuilderKeyTargetRequest true "Target key count"
// @Success 200 {object} BuilderKeysResponse
// @Failure 400 {object} map[string]string "Invalid request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Router /api/buildoor/builder-keys/target [post]
func (h *APIHandler) UpdateBuilderKeyTarget(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	registry := h.keyRegistry()
	if registry == nil {
		writeError(w, http.StatusNotFound, "builder key registry not available")
		return
	}

	var req UpdateBuilderKeyTargetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Target == 0 {
		writeError(w, http.StatusBadRequest, "target must be at least 1")
		return
	}

	if !h.applyKeyTarget(w, r, token, "builder_keys.target", req, req.Target) {
		return
	}

	writeJSON(w, http.StatusOK, &BuilderKeysResponse{
		Keys:      registry.States(),
		Aggregate: registry.Aggregate(),
		Settings:  h.builderKeysSettings(),
	})
}

// applyKeyTarget writes the target through the settings service, so persistence,
// CLI/UI recency resolution and the audit log all follow the same path as every
// other setting. It reports whether the write succeeded.
func (h *APIHandler) applyKeyTarget(
	w http.ResponseWriter, r *http.Request, token *jwt.Token, action string, detail any, target uint64,
) bool {
	encoded, err := json.Marshal(target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode target")
		return false
	}

	return h.applySettings(w, r, token, action, detail, map[string]json.RawMessage{
		config.KeyBuilderKeysTargetCount: encoded,
	})
}

// resolveKeyParam parses the {index} path parameter into a managed key.
func (h *APIHandler) resolveKeyParam(w http.ResponseWriter, r *http.Request) *builder_keys.Key {
	registry := h.keyRegistry()
	if registry == nil {
		writeError(w, http.StatusNotFound, "builder key registry not available")
		return nil
	}

	keyIndex, err := strconv.ParseUint(mux.Vars(r)["index"], 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid key index")
		return nil
	}

	key, err := registry.Key(keyIndex)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil
	}

	return key
}

// DepositBuilderKey godoc
// @Id depositBuilderKey
// @Summary Deposit for a builder key
// @Tags Builder Keys
// @Description Submits a builder deposit for the given key and waits for it to
// @Description register on the beacon chain.
// @Produce json
// @Param index path int true "Internal builder key index"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string "Invalid key index or deposit failed"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Router /api/buildoor/builder-keys/{index}/deposit [post]
func (h *APIHandler) DepositBuilderKey(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if !h.requireLifecycle(w) {
		return
	}

	key := h.resolveKeyParam(w, r)
	if key == nil {
		return
	}

	detail := map[string]uint64{"key_index": key.KeyIndex()}

	if err := h.lifecycleMgr.EnsureBuilderRegistered(context.Background(), key); err != nil {
		h.audit(r, token, "builder_keys.deposit", "", detail, "error: "+err.Error())
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	h.audit(r, token, "builder_keys.deposit", "", detail, "ok")
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"key":    fmt.Sprintf("#%d", key.KeyIndex()),
	})
}

// TopupBuilderKey godoc
// @Id topupBuilderKey
// @Summary Top up a builder key
// @Tags Builder Keys
// @Description Submits a top-up deposit for the given key when its balance is
// @Description below the configured threshold.
// @Accept json
// @Produce json
// @Param index path int true "Internal builder key index"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string "Invalid key index or top-up failed"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Router /api/buildoor/builder-keys/{index}/topup [post]
func (h *APIHandler) TopupBuilderKey(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if !h.requireLifecycle(w) {
		return
	}

	key := h.resolveKeyParam(w, r)
	if key == nil {
		return
	}

	req := BuilderKeyTopupRequest{}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	detail := map[string]uint64{"key_index": key.KeyIndex(), "amount_gwei": req.AmountGwei}

	if err := h.lifecycleMgr.TopupKey(context.Background(), key, req.AmountGwei); err != nil {
		h.audit(r, token, "builder_keys.topup", "", detail, "error: "+err.Error())
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	h.audit(r, token, "builder_keys.topup", "", detail, "ok")
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"key":    fmt.Sprintf("#%d", key.KeyIndex()),
	})
}

// ExitBuilderKey godoc
// @Id exitBuilderKey
// @Summary Exit a builder key
// @Tags Builder Keys
// @Description Submits a builder exit request for the given key. Irreversible:
// @Description an exited key cannot be reactivated until its registry entry is
// @Description reused by another builder's deposit. With lower_target the
// @Description target key count is decremented in the same write, so the
// @Description reconciler does not deposit a replacement.
// @Accept json
// @Produce json
// @Param index path int true "Internal builder key index"
// @Param request body BuilderKeyExitRequest false "Exit options"
// @Success 200 {object} map[string]string
// @Failure 400 {object} map[string]string "Invalid key index"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 409 {object} map[string]string "Key cannot be exited yet"
// @Router /api/buildoor/builder-keys/{index}/exit [post]
func (h *APIHandler) ExitBuilderKey(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if !h.requireLifecycle(w) {
		return
	}

	key := h.resolveKeyParam(w, r)
	if key == nil {
		return
	}

	// The body is optional; an absent one exits without touching the target.
	req := BuilderKeyExitRequest{}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	detail := map[string]any{"key_index": key.KeyIndex(), "lower_target": req.LowerTarget}

	if err := h.lifecycleMgr.InitiateExit(context.Background(), key); err != nil {
		h.audit(r, token, "builder_keys.exit", "", detail, "error: "+err.Error())
		writeError(w, http.StatusConflict, err.Error())

		return
	}

	h.audit(r, token, "builder_keys.exit", "", detail, "ok")

	// Lower the target after the exit landed, so a failed exit never shrinks
	// the fleet the operator asked for.
	if req.LowerTarget {
		target := h.settingsSvc.Current().BuilderKeys.EffectiveTargetCount()
		if target > 1 {
			h.applyKeyTarget(w, r, token, "builder_keys.target", detail, target-1)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"key":    fmt.Sprintf("#%d", key.KeyIndex()),
	})
}
