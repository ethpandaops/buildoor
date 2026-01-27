package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AuthHandler handles authentication requests for the buildoor web UI.
type AuthHandler struct {
	authKey    string
	userHeader string
	tokenKey   string
}

// NewAuthHandler creates a new authentication handler.
func NewAuthHandler(authKey string, userHeader string, tokenKey string) *AuthHandler {
	return &AuthHandler{
		authKey:    authKey,
		userHeader: userHeader,
		tokenKey:   tokenKey,
	}
}

// HandleAuthentication handles the authentication request.
func (h *AuthHandler) GetToken(w http.ResponseWriter, r *http.Request) {
	headers := r.Header
	authUser := "unauthenticated"

	// Try exact header match first
	if values, ok := headers[h.userHeader]; ok && len(values) > 0 {
		authUser = values[0]
	} else {
		// Try case-insensitive match
		for key, values := range headers {
			if strings.EqualFold(key, h.userHeader) && len(values) > 0 {
				authUser = values[0]
				break
			}
		}
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer:    "buildoor",
		Subject:   authUser,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
	})

	tokenString, err := token.SignedString([]byte(h.authKey))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(map[string]string{
		"token": tokenString,
		"user":  authUser,
		"expr":  fmt.Sprintf("%d", token.Claims.(jwt.RegisteredClaims).ExpiresAt.Time.Unix()),
		"now":   fmt.Sprintf("%d", time.Now().Unix()),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *AuthHandler) GetLogin(w http.ResponseWriter, r *http.Request) {
	// redirect back to the index page
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}
