package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/mux"

	txpoolrpc "github.com/ethpandaops/buildoor/pkg/txpool/rpc"
)

// SetRPCIngress attaches the /rpc ingress whose keys, callers and upstreams
// the rpc-access endpoints manage.
func (h *APIHandler) SetRPCIngress(ingress *txpoolrpc.Server, upstreams *txpoolrpc.UpstreamPool, keys *txpoolrpc.KeyStore) {
	h.rpcIngress = ingress
	h.rpcUpstreams = upstreams
	h.rpcKeys = keys
}

// RPCKeyView is an API key without its hash.
type RPCKeyView struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Prefix    string    `json:"prefix"`
	RateLimit *uint64   `json:"rate_limit,omitempty"` // nil = txpool.rpc_key_rate
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

// RPCAccessResponse is the /rpc ingress access state.
type RPCAccessResponse struct {
	Keys      []RPCKeyView              `json:"keys"`
	Callers   []txpoolrpc.CallerStats   `json:"callers"`
	Upstreams []txpoolrpc.UpstreamStats `json:"upstreams"`
}

func rpcKeyView(key *txpoolrpc.APIKey) RPCKeyView {
	return RPCKeyView{
		ID:        key.ID,
		Label:     key.Label,
		Prefix:    key.Prefix,
		RateLimit: key.RateLimit,
		Enabled:   key.Enabled,
		CreatedAt: key.CreatedAt,
	}
}

// isLoggedIn reports whether the request carries a valid login (always true
// when no auth provider is configured: the whole API is open then).
func (h *APIHandler) isLoggedIn(r *http.Request) bool {
	return h.authHandler.CheckAuthToken(r.Header.Get("Authorization")) != nil
}

// requireRPCIngress answers 404 when no ingress exists (no --el-rpc).
func (h *APIHandler) requireRPCIngress(w http.ResponseWriter) bool {
	if h.rpcIngress == nil || h.rpcKeys == nil {
		writeError(w, http.StatusNotFound, "the /rpc ingress is not available (no --el-rpc)")
		return false
	}

	return true
}

// GetRPCAccess godoc
// @Summary /rpc ingress access state
// @Description API keys (without secrets), per-caller call stats (anonymous per IP or shared, per key, static token, WebUI login) and the read upstreams. Requires authentication: the stats carry client IPs.
// @Tags Buildoor
// @Produce json
// @Success 200 {object} RPCAccessResponse "Success"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 404 {object} map[string]string "No ingress"
// @Router /rpc-access [get]
func (h *APIHandler) GetRPCAccess(w http.ResponseWriter, r *http.Request) {
	if h.authHandler.CheckAuthToken(r.Header.Get("Authorization")) == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if !h.requireRPCIngress(w) {
		return
	}

	keys := h.rpcKeys.List()
	views := make([]RPCKeyView, 0, len(keys))

	for _, key := range keys {
		views = append(views, rpcKeyView(key))
	}

	writeJSON(w, http.StatusOK, RPCAccessResponse{
		Keys:      views,
		Callers:   h.rpcIngress.Access().Stats(),
		Upstreams: h.rpcUpstreams.Stats(),
	})
}

// CreateRPCKeyRequest creates an API key.
type CreateRPCKeyRequest struct {
	Label     string  `json:"label"`
	RateLimit *uint64 `json:"rate_limit,omitempty"` // calls per second, 0 = unlimited; absent = default
}

// CreateRPCKeyResponse carries the new key and its secret, shown only once.
type CreateRPCKeyResponse struct {
	Key    RPCKeyView `json:"key"`
	Secret string     `json:"secret"`
}

// CreateRPCKey godoc
// @Summary Create a /rpc API key
// @Description Creates an API key for the /rpc ingress. The secret is returned once and never stored (only its SHA-256). Send it as "X-API-Key: <key>", "Authorization: Bearer <key>" or in the URL as /rpc/<key>.
// @Tags Buildoor
// @Accept json
// @Produce json
// @Param request body CreateRPCKeyRequest true "Key"
// @Success 200 {object} CreateRPCKeyResponse "Success"
// @Failure 400 {object} map[string]string "Bad Request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Router /rpc-access/keys [post]
func (h *APIHandler) CreateRPCKey(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if !h.requireRPCIngress(w) {
		return
	}

	var req CreateRPCKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Label == "" {
		writeError(w, http.StatusBadRequest, "label is required")
		return
	}

	key, secret, err := h.rpcKeys.Create(req.Label, req.RateLimit)
	if err != nil {
		h.audit(r, token, "rpc_key.create", req.Label, req, "error: "+err.Error())
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	h.audit(r, token, "rpc_key.create", key.ID, req, "ok")

	writeJSON(w, http.StatusOK, CreateRPCKeyResponse{Key: rpcKeyView(key), Secret: secret})
}

// UpdateRPCKeyRequest is a partial key update; absent fields stay unchanged.
type UpdateRPCKeyRequest struct {
	Label   *string `json:"label,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
	// RateLimit sets the key's own limit (calls per second, 0 = unlimited);
	// DefaultRateLimit returns it to txpool.rpc_key_rate.
	RateLimit        *uint64 `json:"rate_limit,omitempty"`
	DefaultRateLimit bool    `json:"default_rate_limit,omitempty"`
}

// UpdateRPCKey godoc
// @Summary Update a /rpc API key
// @Description Changes a key's label, enabled flag or rate limit. A disabled key is refused (401) without being deleted.
// @Tags Buildoor
// @Accept json
// @Produce json
// @Param id path string true "Key id"
// @Param request body UpdateRPCKeyRequest true "Update"
// @Success 200 {object} RPCKeyView "Success"
// @Failure 400 {object} map[string]string "Bad Request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 404 {object} map[string]string "Not Found"
// @Router /rpc-access/keys/{id} [patch]
func (h *APIHandler) UpdateRPCKey(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if !h.requireRPCIngress(w) {
		return
	}

	id := mux.Vars(r)["id"]

	var req UpdateRPCKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	key, err := h.rpcKeys.Update(id, txpoolrpc.KeyUpdate{
		Label:          req.Label,
		Enabled:        req.Enabled,
		RateLimit:      req.RateLimit,
		ClearRateLimit: req.DefaultRateLimit,
	})
	if err != nil {
		h.audit(r, token, "rpc_key.update", id, req, "error: "+err.Error())

		status := http.StatusInternalServerError
		if errors.Is(err, txpoolrpc.ErrKeyNotFound) {
			status = http.StatusNotFound
		}

		writeError(w, status, err.Error())

		return
	}

	h.audit(r, token, "rpc_key.update", id, req, "ok")

	writeJSON(w, http.StatusOK, rpcKeyView(key))
}

// DeleteRPCKey godoc
// @Summary Delete a /rpc API key
// @Description Deletes the key; callers using it are refused from the next request on.
// @Tags Buildoor
// @Produce json
// @Param id path string true "Key id"
// @Success 200 {object} map[string]string "Success"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 404 {object} map[string]string "Not Found"
// @Router /rpc-access/keys/{id} [delete]
func (h *APIHandler) DeleteRPCKey(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if !h.requireRPCIngress(w) {
		return
	}

	id := mux.Vars(r)["id"]

	if err := h.rpcKeys.Delete(id); err != nil {
		h.audit(r, token, "rpc_key.delete", id, nil, "error: "+err.Error())
		writeError(w, http.StatusNotFound, err.Error())

		return
	}

	h.audit(r, token, "rpc_key.delete", id, nil, "ok")

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
