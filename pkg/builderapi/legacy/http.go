package legacy

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/ethpandaops/go-eth2-client/spec/version"
)

// writeError writes a JSON error response of the form {"code": ..., "message": ...}.
func writeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message})
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
