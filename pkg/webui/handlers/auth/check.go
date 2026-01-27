package auth

import (
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

func (h *AuthHandler) CheckAuthToken(tokenStr string) *jwt.Token {
	var token *jwt.Token = nil

	// Extract token from "Bearer <token>"
	parts := strings.SplitN(tokenStr, " ", 2)
	if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
		tokenStr = parts[1]
	}

	if h.tokenKey != "" {
		token, _ = jwt.ParseWithClaims(tokenStr, &jwt.RegisteredClaims{}, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return []byte(h.tokenKey), nil
		})
	}

	if token == nil {
		token, _ = jwt.ParseWithClaims(tokenStr, &jwt.RegisteredClaims{}, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return []byte(h.authKey), nil
		})
	}

	return token
}
