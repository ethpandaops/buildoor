package auth

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ethpandaops/service-authenticatoor/pkg/auth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/sirupsen/logrus"
)

// openToken is the sentinel returned in open mode. Callers only inspect
// `token != nil && token.Valid`, so an empty *jwt.Token with Valid set is
// enough for them to treat the request as authenticated.
var openToken = &jwt.Token{Valid: true}

// CheckAuthToken validates a bearer token (with or without the "Bearer "
// prefix). In open mode it always returns a valid token; in remote mode
// it delegates to the JWKS verifier and returns nil on any failure. A valid
// token has basic access only: handlers ask for what they need with Require
// or Authorize.
func (h *AuthHandler) CheckAuthToken(tokenStr string) *jwt.Token {
	// Open mode — no verifier configured, every request is authorized.
	if h.verifier == nil {
		return openToken
	}

	parts := strings.SplitN(tokenStr, " ", 2)
	if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
		tokenStr = parts[1]
	}
	if tokenStr == "" {
		return nil
	}

	claims, err := h.verifier.Verify(tokenStr)
	if err != nil {
		return nil
	}
	return &jwt.Token{Valid: true, Claims: claims}
}

// HasFeature reports whether a token CheckAuthToken returned allows a
// feature of this service. FeatureBasic (or "") is true for every valid
// token; any other feature needs its grant in the token's "services" claim
// (default deny). In open mode everything is allowed.
func (h *AuthHandler) HasFeature(token *jwt.Token, feature string) bool {
	if token == nil || !token.Valid {
		return false
	}

	if h.verifier == nil || feature == "" || feature == FeatureBasic {
		return true
	}

	claims, ok := TokenClaims(token)

	return ok && claims.HasFeature(h.service, feature, auth.FeatureDefaultDeny)
}

// TokenClaims returns the authenticatoor claims of a token CheckAuthToken
// returned; the open mode token has none.
func TokenClaims(token *jwt.Token) (*auth.Claims, bool) {
	if token == nil {
		return nil, false
	}

	claims, ok := token.Claims.(*auth.Claims)

	return claims, ok && claims != nil
}

// Authorize checks the request's bearer token against a feature. It returns
// the token and http.StatusOK when the request may proceed,
// http.StatusUnauthorized without a valid token for this service, and
// http.StatusForbidden (with the token, for the caller's log) when the token
// is valid but lacks the feature.
func (h *AuthHandler) Authorize(r *http.Request, feature string) (*jwt.Token, int) {
	token := h.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		return nil, http.StatusUnauthorized
	}

	if !h.HasFeature(token, feature) {
		return token, http.StatusForbidden
	}

	return token, http.StatusOK
}

// Require is Authorize for a handler: when the request may not proceed it
// answers 401 {"error":"unauthorized"} or 403 {"error":"forbidden: this
// login lacks the feature <service>:<feature>"} itself and returns false.
// A refused request is logged at debug level; it is not an audited action.
func (h *AuthHandler) Require(w http.ResponseWriter, r *http.Request, feature string) (*jwt.Token, bool) {
	token, status := h.Authorize(r, feature)

	switch status {
	case http.StatusOK:
		return token, true
	case http.StatusForbidden:
		subject := ""
		if claims, ok := TokenClaims(token); ok {
			subject = claims.Subject
		}

		logrus.WithFields(logrus.Fields{
			"module":  "webui-auth",
			"subject": subject,
			"feature": h.FeatureName(feature),
			"method":  r.Method,
			"path":    r.URL.Path,
		}).Debug("request refused: the login lacks the feature")

		writeError(w, status, "forbidden: this login lacks the feature "+h.FeatureName(feature))
	default:
		writeError(w, status, "unauthorized")
	}

	return nil, false
}

// FeatureName is the feature as a grant names it: "<service>:<feature>".
func (h *AuthHandler) FeatureName(feature string) string {
	return h.service + ":" + feature
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
