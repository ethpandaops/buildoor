package api

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/db"
)

// actorFromToken derives a human-meaningful actor identity for the audit log.
// In open mode (no auth provider) the token carries no claims, so "open" is
// recorded. Otherwise the JWT subject is used.
func actorFromToken(token *jwt.Token) string {
	if token == nil || token.Claims == nil {
		return "open"
	}

	if sub, err := token.Claims.GetSubject(); err == nil && sub != "" {
		return sub
	}

	return "authenticated"
}

// clientIP returns the request's remote address without the port.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

// audit records an authenticated mutating action to the state-db. It is
// best-effort: when persistence is disabled it is a no-op, and storage errors
// are logged but never fail the request.
func (h *APIHandler) audit(r *http.Request, token *jwt.Token, action, target string, detail any, result string) {
	if !h.stateDB.Enabled() {
		return
	}

	detailJSON := ""

	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			detailJSON = string(b)
		}
	}

	entry := db.AuditLog{
		Timestamp:  time.Now().UnixMilli(),
		Actor:      actorFromToken(token),
		RemoteAddr: clientIP(r),
		Action:     action,
		Target:     target,
		Detail:     detailJSON,
		Result:     result,
	}

	if err := h.stateDB.AppendAuditLog(entry); err != nil {
		logrus.WithError(err).WithField("module", "webui-api").Warn("failed to append audit log")
	}
}

// mustJSON marshals a value for use as a settings override; the inputs are
// always simple scalars so marshalling cannot fail in practice.
func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// applySettings applies a batch of UI setting overrides via the settings
// service, broadcasts a config update on success, and records an audit entry.
// It writes an error response and returns false on failure. An empty update set
// is treated as a successful no-op.
func (h *APIHandler) applySettings(w http.ResponseWriter, r *http.Request, token *jwt.Token, action string, detail any, updates map[string]json.RawMessage) bool {
	if len(updates) == 0 {
		return true
	}

	if err := h.settingsSvc.SetMany(updates, actorFromToken(token)); err != nil {
		h.audit(r, token, action, "", detail, "error: "+err.Error())
		writeError(w, http.StatusBadRequest, err.Error())

		return false
	}

	if h.eventStreamMgr != nil {
		h.eventStreamMgr.BroadcastConfigUpdate()
	}

	h.audit(r, token, action, "", detail, "ok")

	return true
}

// AuditLogResponse is the paginated response for GetAuditLog.
type AuditLogResponse struct {
	Entries []db.AuditLog `json:"entries"`
	Total   int           `json:"total"`
	Offset  int           `json:"offset"`
	Limit   int           `json:"limit"`
}

// GetAuditLog godoc
// @Id getAuditLog
// @Summary Get the audit log
// @Tags Buildoor
// @Description Returns a paginated list of authenticated mutating actions. Empty when no state-db is configured.
// @Produce json
// @Param Authorization header string true "Bearer token"
// @Param offset query int false "Offset for pagination" default(0)
// @Param limit query int false "Limit for pagination (max 100)" default(20)
// @Success 200 {object} AuditLogResponse "Success"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 500 {object} map[string]string "Server Error"
// @Router /api/buildoor/audit-log [get]
func (h *APIHandler) GetAuditLog(w http.ResponseWriter, r *http.Request) {
	// The audit log is privileged: require authentication. In open mode
	// CheckAuthToken always returns a valid token, matching the rest of the API.
	if h.authHandler.CheckAuthToken(r.Header.Get("Authorization")) == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	offset := 0
	limit := 20

	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	if limit > 100 {
		limit = 100
	}

	entries, total, err := h.stateDB.GetAuditLogs(offset, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, AuditLogResponse{
		Entries: entries,
		Total:   total,
		Offset:  offset,
		Limit:   limit,
	})
}
