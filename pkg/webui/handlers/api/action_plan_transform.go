package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/jqtransform"
	"github.com/ethpandaops/buildoor/pkg/slot_results"
)

// transform targets accepted by the test endpoint (match the plan's
// transforms.* keys).
const (
	transformTargetPayload  = "payload"
	transformTargetBid      = "bid"
	transformTargetEnvelope = "envelope"
)

const testTransformTimeout = 2 * time.Second

// TestTransformRequest is the body of POST /api/buildoor/action-plan/test-transform.
type TestTransformRequest struct {
	// Target is one of payload | bid | envelope.
	Target string `json:"target"`
	// Expression is the jq program to evaluate.
	Expression string `json:"expression"`
	// SampleSlot, when > 0, sources the input from that slot's captured
	// artifact (falling back to a template when it is unavailable).
	SampleSlot uint64 `json:"sample_slot,omitempty"`
}

// TestTransformResponse reports the transform result against the sample input.
// Input and Output are pretty-printed JSON as STRINGS (not embedded JSON), so
// the UI renders them verbatim in a text area.
type TestTransformResponse struct {
	Target string `json:"target"`
	// Input is the JSON the expression ran against (message JSON for bid /
	// envelope; the payload for payload), pretty-printed as a string.
	Input string `json:"input"`
	// InputSource is "artifact:slot-N" or "template".
	InputSource string `json:"input_source"`
	// Output is the transform result, pretty-printed (present when Error is empty).
	Output string `json:"output,omitempty"`
	// Error is the parse/eval error message (present when the expression fails).
	Error string `json:"error,omitempty"`
}

// TestTransform godoc
// @Id testActionPlanTransform
// @Summary Evaluate a jq transform against a sample builder object
// @Tags ActionPlan
// @Description Runs an operator jq expression against a sample payload / bid /
// @Description envelope (a captured artifact when sample_slot is given and
// @Description available, otherwise a zero-value template of the object) and
// @Description returns the transformed JSON, so expressions can be built and
// @Description tested live before being saved to a slot plan. Bid and envelope
// @Description transforms operate on the MESSAGE, matching production.
// @Accept json
// @Produce json
// @Param request body TestTransformRequest true "Target, expression and optional sample slot"
// @Success 200 {object} TestTransformResponse
// @Failure 400 {object} map[string]string "Bad Request"
// @Router /api/buildoor/action-plan/test-transform [post]
func (h *APIHandler) TestTransform(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPlanUpdateBodyBytes)

	var req TestTransformRequest

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.Target != transformTargetPayload && req.Target != transformTargetBid &&
		req.Target != transformTargetEnvelope {
		writeError(w, http.StatusBadRequest,
			"target must be one of: payload, bid, envelope")

		return
	}

	// Validate first so parse errors are reported distinctly from an empty
	// (identity) expression.
	if err := jqtransform.Validate(req.Expression); err != nil {
		writeJSON(w, http.StatusOK, &TestTransformResponse{Target: req.Target, Error: err.Error()})
		return
	}

	input, source, err := h.transformSampleInput(req.Target, req.SampleSlot)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp := &TestTransformResponse{
		Target:      req.Target,
		Input:       indentJSON(input),
		InputSource: source,
	}

	ctx, cancel := context.WithTimeout(r.Context(), testTransformTimeout)
	defer cancel()

	out, err := jqtransform.Apply(ctx, req.Expression, input)
	if err != nil {
		resp.Error = err.Error()
		writeJSON(w, http.StatusOK, resp)

		return
	}

	resp.Output = indentJSON(out)
	writeJSON(w, http.StatusOK, resp)
}

// transformSampleInput resolves the JSON the expression runs against, in order
// of preference: the given slot's captured artifact, then the most recent
// captured artifact of that kind, then an illustrative template. Bid / envelope
// artifacts are reduced to their message to match production.
func (h *APIHandler) transformSampleInput(target string, sampleSlot uint64) (
	input []byte, source string, err error,
) {
	kind := artifactKindForTarget(target)

	if h.resultTracker != nil {
		store := h.resultTracker.Artifacts()

		if sampleSlot > 0 {
			if a, err := store.Get(phase0.Slot(sampleSlot), kind, 0); err == nil && a != nil {
				if in, ok := artifactToSample(target, a); ok {
					return in, fmt.Sprintf("artifact:slot-%d", sampleSlot), nil
				}
			}
		}

		if a, ok := store.LatestByKind(kind); ok {
			if in, ok := artifactToSample(target, a); ok {
				return in, fmt.Sprintf("artifact:slot-%d", a.Slot), nil
			}
		}
	}

	return transformTemplate(target), "template", nil
}

func artifactKindForTarget(target string) string {
	switch target {
	case transformTargetBid:
		return slot_results.ArtifactKindBid
	case transformTargetEnvelope:
		return slot_results.ArtifactKindEnvelope
	default:
		return slot_results.ArtifactKindPayload
	}
}

// artifactToSample decodes a captured artifact to the JSON the transform
// operates on (bid / envelope reduced to their message).
func artifactToSample(target string, artifact *db.SlotArtifact) ([]byte, bool) {
	decoded, err := decodeArtifact(artifact)
	if err != nil {
		return nil, false
	}

	full, err := json.Marshal(decoded)
	if err != nil {
		return nil, false
	}

	if target == transformTargetBid || target == transformTargetEnvelope {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(full, &obj); err != nil {
			return nil, false
		}

		if msg, ok := obj["message"]; ok {
			return msg, true
		}
	}

	return full, true
}

// indentJSON pretty-prints JSON as a string for display; on failure returns
// the input as-is so the caller always has something to show.
func indentJSON(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}

	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}

	return string(buf)
}
