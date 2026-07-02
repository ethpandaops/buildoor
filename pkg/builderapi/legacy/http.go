package legacy

import (
	"encoding/json"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/ethpandaops/go-eth2-client/spec/version"
)

// writeError writes a JSON error response of the form {"code": ..., "message": ...}.
func writeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message})
}

// preferSSZ reports whether an Accept header prefers the SSZ
// (application/octet-stream) representation over JSON. Wildcard ranges count
// towards JSON (the default representation); on ties JSON wins.
func preferSSZ(accept string) bool {
	sszQ, jsonQ := 0.0, 0.0

	for _, part := range strings.Split(accept, ",") {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}

		q := 1.0
		if qs, ok := params["q"]; ok {
			if parsed, err := strconv.ParseFloat(qs, 64); err == nil {
				q = parsed
			}
		}

		switch mediaType {
		case "application/octet-stream":
			sszQ = max(sszQ, q)
		case "application/json", "application/*", "*/*":
			jsonQ = max(jsonQ, q)
		}
	}

	return sszQ > jsonQ
}

// parseConsensusVersion parses an Eth-Consensus-Version header value
// (e.g. "electra") into a version.DataVersion. Matching is case-insensitive.
func parseConsensusVersion(s string) (version.DataVersion, error) {
	var v version.DataVersion
	if err := v.UnmarshalJSON([]byte(strconv.Quote(s))); err != nil {
		return version.DataVersionUnknown, err
	}

	return v, nil
}

// trimHex strips an optional 0x/0X prefix from a hex string.
func trimHex(s string) string {
	if len(s) >= 2 && (s[0:2] == "0x" || s[0:2] == "0X") {
		return s[2:]
	}

	return s
}
