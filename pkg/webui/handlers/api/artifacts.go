package api

import (
	"encoding/json"
	"mime"
	"net/http"
	"strconv"
	"strings"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/gorilla/mux"

	legacytypes "github.com/ethpandaops/buildoor/pkg/builderapi/legacy/types"
	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/slot_results"
)

// BidArtifactMetaEntry is one entry of the bid artifact listing.
type BidArtifactMetaEntry struct {
	Index                int    `json:"index"`
	Fork                 string `json:"fork"`
	Transport            string `json:"transport,omitempty"`
	TotalValueGwei       uint64 `json:"total_value_gwei,omitempty"`
	ExecutionPaymentGwei uint64 `json:"execution_payment_gwei,omitempty"`
	At                   int64  `json:"at,omitempty"` // unix milliseconds
}

// SlotBidArtifactsResponse lists a slot's stored bid artifacts.
type SlotBidArtifactsResponse struct {
	Slot uint64                 `json:"slot"`
	Bids []BidArtifactMetaEntry `json:"bids"`
}

// negotiateArtifact resolves the Accept header (q-values respected) between
// application/octet-stream (raw SSZ) and application/json (versioned JSON
// envelope). An absent Accept header defaults to JSON; an Accept header
// matching neither yields 406.
func negotiateArtifact(r *http.Request) (wantSSZ, notAcceptable bool) {
	accept := r.Header.Get("Accept")
	if accept == "" {
		return false, false
	}

	bestSSZ, bestJSON := -1.0, -1.0

	for part := range strings.SplitSeq(accept, ",") {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}

		q := 1.0
		if qStr, ok := params["q"]; ok {
			if parsed, err := strconv.ParseFloat(qStr, 64); err == nil {
				q = parsed
			}
		}

		if q <= 0 {
			continue
		}

		switch mediaType {
		case "application/octet-stream":
			bestSSZ = max(bestSSZ, q)
		case "application/json":
			bestJSON = max(bestJSON, q)
		case "*/*", "application/*":
			bestJSON = max(bestJSON, q)
			bestSSZ = max(bestSSZ, q-0.0001) // wildcard prefers JSON at equal quality
		}
	}

	if bestSSZ < 0 && bestJSON < 0 {
		return false, true
	}

	return bestSSZ > bestJSON, false
}

// decodeArtifact rebuilds the typed container from a stored artifact's SSZ
// bytes for the JSON response branch.
func decodeArtifact(artifact *db.SlotArtifact) (any, error) {
	fork := version.DataVersion(artifact.Fork)

	switch artifact.Kind {
	case slot_results.ArtifactKindPayload:
		payload := &eth2all.ExecutionPayload{Version: fork}
		if err := payload.UnmarshalSSZ(artifact.Data); err != nil {
			return nil, err
		}

		return payload, nil
	case slot_results.ArtifactKindBid:
		if fork >= version.DataVersionGloas {
			bid := &eth2all.SignedExecutionPayloadBid{Version: fork}
			if err := bid.UnmarshalSSZ(artifact.Data); err != nil {
				return nil, err
			}

			return bid, nil
		}

		bid := &legacytypes.SignedBuilderBid{Version: fork}
		if err := bid.UnmarshalSSZ(artifact.Data); err != nil {
			return nil, err
		}

		return bid, nil
	default: // slot_results.ArtifactKindEnvelope
		envelope := &eth2all.SignedExecutionPayloadEnvelope{Version: fork}
		if err := envelope.UnmarshalSSZ(artifact.Data); err != nil {
			return nil, err
		}

		return envelope, nil
	}
}

// writeArtifact serves one artifact with beacon-API-style content
// negotiation: raw SSZ for application/octet-stream, a {"version", "data"}
// JSON envelope otherwise. Every success carries Eth-Consensus-Version and
// Vary: Accept.
func writeArtifact(w http.ResponseWriter, r *http.Request, artifact *db.SlotArtifact) {
	if artifact == nil {
		writeError(w, http.StatusNotFound, "artifact not found")
		return
	}

	wantSSZ, notAcceptable := negotiateArtifact(r)
	if notAcceptable {
		writeError(w, http.StatusNotAcceptable,
			"acceptable content types: application/octet-stream, application/json")
		return
	}

	forkName := version.DataVersion(artifact.Fork).String()

	w.Header().Set("Eth-Consensus-Version", forkName)
	w.Header().Set("Vary", "Accept")

	if wantSSZ {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(artifact.Data)

		return
	}

	decoded, err := decodeArtifact(artifact)
	if err != nil {
		// A stored artifact that no longer decodes indicates a codec bug,
		// not a client error.
		writeError(w, http.StatusInternalServerError, "failed to decode stored artifact: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"version": forkName,
		"data":    decoded,
	})
}

// parseArtifactSlot reads the {slot} path variable.
func parseArtifactSlot(w http.ResponseWriter, r *http.Request) (phase0.Slot, bool) {
	slotU64, err := strconv.ParseUint(mux.Vars(r)["slot"], 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid slot: must be a number")
		return 0, false
	}

	return phase0.Slot(slotU64), true
}

// GetSlotPayloadArtifact godoc
// @Id getSlotPayloadArtifact
// @Summary Get the built execution payload of a slot
// @Tags ActionPlan
// @Description Returns the exact execution payload built for the slot. With
// @Description "Accept: application/octet-stream" the raw SSZ bytes are
// @Description served; otherwise a {"version", "data"} JSON envelope. The
// @Description Eth-Consensus-Version response header carries the fork name.
// @Produce json,application/octet-stream
// @Param slot path int true "Slot"
// @Success 200 {object} map[string]any "Versioned payload (or raw SSZ)"
// @Failure 400 {object} map[string]string "Bad Request"
// @Failure 404 {object} map[string]string "No artifact for this slot"
// @Failure 406 {object} map[string]string "No acceptable content type"
// @Router /api/buildoor/slot-results/{slot}/payload [get]
func (h *APIHandler) GetSlotPayloadArtifact(w http.ResponseWriter, r *http.Request) {
	h.serveArtifact(w, r, slot_results.ArtifactKindPayload, 0)
}

// GetSlotEnvelopeArtifact godoc
// @Id getSlotEnvelopeArtifact
// @Summary Get the signed payload envelope of a slot
// @Tags ActionPlan
// @Description Returns the signed execution payload envelope constructed for
// @Description the slot's reveal (stored at construction time, so failed
// @Description publishes remain inspectable). Content negotiation as with the
// @Description payload artifact endpoint.
// @Produce json,application/octet-stream
// @Param slot path int true "Slot"
// @Success 200 {object} map[string]any "Versioned envelope (or raw SSZ)"
// @Failure 400 {object} map[string]string "Bad Request"
// @Failure 404 {object} map[string]string "No artifact for this slot"
// @Failure 406 {object} map[string]string "No acceptable content type"
// @Router /api/buildoor/slot-results/{slot}/envelope [get]
func (h *APIHandler) GetSlotEnvelopeArtifact(w http.ResponseWriter, r *http.Request) {
	h.serveArtifact(w, r, slot_results.ArtifactKindEnvelope, 0)
}

// GetSlotBidArtifact godoc
// @Id getSlotBidArtifact
// @Summary Get one signed bid of a slot
// @Tags ActionPlan
// @Description Returns one signed bid created for the slot, addressed by its
// @Description artifact index (see the bid listing endpoint). Pre-Gloas bids
// @Description are SignedBuilderBid, Gloas+ bids SignedExecutionPayloadBid.
// @Description Content negotiation as with the payload artifact endpoint.
// @Produce json,application/octet-stream
// @Param slot path int true "Slot"
// @Param index path int true "Bid artifact index"
// @Success 200 {object} map[string]any "Versioned signed bid (or raw SSZ)"
// @Failure 400 {object} map[string]string "Bad Request"
// @Failure 404 {object} map[string]string "No artifact for this slot/index"
// @Failure 406 {object} map[string]string "No acceptable content type"
// @Router /api/buildoor/slot-results/{slot}/bids/{index} [get]
func (h *APIHandler) GetSlotBidArtifact(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.Atoi(mux.Vars(r)["index"])
	if err != nil || index < 0 {
		writeError(w, http.StatusBadRequest, "invalid index: must be a non-negative number")
		return
	}

	h.serveArtifact(w, r, slot_results.ArtifactKindBid, index)
}

func (h *APIHandler) serveArtifact(w http.ResponseWriter, r *http.Request, kind string, index int) {
	if h.resultTracker == nil {
		writeError(w, http.StatusServiceUnavailable, "slot results tracker not available")
		return
	}

	slot, ok := parseArtifactSlot(w, r)
	if !ok {
		return
	}

	artifact, err := h.resultTracker.Artifacts().Get(slot, kind, index)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load artifact: "+err.Error())
		return
	}

	writeArtifact(w, r, artifact)
}

// GetSlotBidArtifacts godoc
// @Id getSlotBidArtifacts
// @Summary List a slot's signed bid artifacts
// @Tags ActionPlan
// @Description Lists the metadata of every signed bid stored for the slot
// @Description (JSON only; fetch individual bids by index for SSZ). Returns
// @Description an empty list for slots without bid artifacts.
// @Produce json
// @Param slot path int true "Slot"
// @Success 200 {object} SlotBidArtifactsResponse
// @Failure 400 {object} map[string]string "Bad Request"
// @Failure 503 {object} map[string]string "Results tracker unavailable"
// @Router /api/buildoor/slot-results/{slot}/bids [get]
func (h *APIHandler) GetSlotBidArtifacts(w http.ResponseWriter, r *http.Request) {
	if h.resultTracker == nil {
		writeError(w, http.StatusServiceUnavailable, "slot results tracker not available")
		return
	}

	slot, ok := parseArtifactSlot(w, r)
	if !ok {
		return
	}

	metas, err := h.resultTracker.Artifacts().ListBids(slot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list bid artifacts: "+err.Error())
		return
	}

	entries := make([]BidArtifactMetaEntry, 0, len(metas))

	for _, meta := range metas {
		entry := BidArtifactMetaEntry{
			Index: meta.Idx,
			Fork:  version.DataVersion(meta.Fork).String(),
		}

		if meta.Meta != "" {
			var bidMeta slot_results.BidArtifactMeta
			if err := json.Unmarshal([]byte(meta.Meta), &bidMeta); err == nil {
				entry.Transport = bidMeta.Transport
				entry.TotalValueGwei = bidMeta.TotalValueGwei
				entry.ExecutionPaymentGwei = bidMeta.ExecutionPaymentGwei
				entry.At = bidMeta.At
			}
		}

		entries = append(entries, entry)
	}

	writeJSON(w, http.StatusOK, &SlotBidArtifactsResponse{
		Slot: uint64(slot),
		Bids: entries,
	})
}
