package legacy

import (
	"encoding/json"
	"net/http"
)

// writeError writes a JSON error response of the form {"code": ..., "message": ...}.
func writeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message})
}

// trimHex strips an optional 0x/0X prefix from a hex string.
func trimHex(s string) string {
	if len(s) >= 2 && (s[0:2] == "0x" || s[0:2] == "0X") {
		return s[2:]
	}

	return s
}
