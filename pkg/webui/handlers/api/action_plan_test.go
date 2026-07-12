package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/slot_results"
	"github.com/ethpandaops/buildoor/pkg/utils"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers/auth"
)

// planTestChain provides the minimal chain.Service surface the plan service
// and the range parser need.
type planTestChain struct {
	chain.Service

	spec        *chain.ChainSpec
	currentSlot phase0.Slot
}

func newPlanTestChain() *planTestChain {
	return &planTestChain{
		spec: &chain.ChainSpec{
			SecondsPerSlot: 12 * time.Second,
			SlotsPerEpoch:  32,
		},
		currentSlot: 1000,
	}
}

func (s *planTestChain) GetChainSpec() *chain.ChainSpec { return s.spec }
func (s *planTestChain) GetCurrentSlot() phase0.Slot    { return s.currentSlot }
func (s *planTestChain) GetEpochOfSlot(slot phase0.Slot) phase0.Epoch {
	return phase0.Epoch(uint64(slot) / s.spec.SlotsPerEpoch)
}

func (s *planTestChain) ActiveForkAtEpoch(_ phase0.Epoch) version.DataVersion {
	return version.DataVersionGloas
}

func (s *planTestChain) SubscribeEpochStats() *utils.Subscription[*chain.EpochStats] {
	return (&utils.Dispatcher[*chain.EpochStats]{}).Subscribe(1, false)
}

type planAPITestEnv struct {
	handler *APIHandler
	planSvc *action_plan.PlanService
	tracker *slot_results.Tracker
	stateDB *db.Database
}

// newPlanAPITestEnv wires an APIHandler with a real plan service and results
// tracker over an open-mode auth handler (no verifier → every request is
// authenticated, matching open deployments).
func newPlanAPITestEnv(t *testing.T) *planAPITestEnv {
	t.Helper()

	log := logrus.New()
	log.SetOutput(io.Discard)

	cfg := config.DefaultConfig()
	cfg.EPBSEnabled = true
	cfg.BuilderAPIEnabled = true
	cfg.APIPort = 8080

	chainSvc := newPlanTestChain()
	planSvc := action_plan.NewPlanService(cfg, chainSvc, log)

	stateDB := db.NewDatabase(&db.Config{File: filepath.Join(t.TempDir(), "state.db")}, log)
	require.NoError(t, stateDB.Init())

	t.Cleanup(func() { _ = stateDB.Close() })

	tracker := slot_results.NewTracker(cfg, chainSvc, stateDB, planSvc, nil, nil, nil, nil, log)

	authHandler, err := auth.NewAuthHandler(context.Background(), "")
	require.NoError(t, err)

	handler := NewAPIHandler(authHandler, nil, stateDB, nil, nil, nil, chainSvc,
		nil, nil, nil, nil, nil, nil, nil, planSvc, tracker)

	return &planAPITestEnv{
		handler: handler,
		planSvc: planSvc,
		tracker: tracker,
		stateDB: stateDB,
	}
}

func TestGetActionPlanRange(t *testing.T) {
	env := newPlanAPITestEnv(t)

	_, err := env.planSvc.ApplyUpdates([]*action_plan.PlanUpdate{{
		Slots: []uint64{2000, 2010},
		Bid:   json.RawMessage(`{"mode":"disabled"}`),
	}}, "test")
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	env.handler.GetActionPlan(rec,
		httptest.NewRequest(http.MethodGet, "/api/buildoor/action-plan?min_slot=2000&max_slot=2005", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	var resp ActionPlanResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Plans, 1)
	assert.Equal(t, phase0.Slot(2000), resp.Plans[0].Slot)
}

func TestGetActionPlanRangeValidation(t *testing.T) {
	env := newPlanAPITestEnv(t)

	tests := []struct {
		name  string
		query string
	}{
		{name: "missing params", query: ""},
		{name: "non-numeric", query: "?min_slot=a&max_slot=b"},
		{name: "inverted range", query: "?min_slot=100&max_slot=50"},
		{name: "oversized range", query: "?min_slot=0&max_slot=99999999"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			env.handler.GetActionPlan(rec,
				httptest.NewRequest(http.MethodGet, "/api/buildoor/action-plan"+tt.query, nil))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func postActionPlan(t *testing.T, env *planAPITestEnv, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/buildoor/action-plan",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	env.handler.UpdateActionPlan(rec, req)

	return rec
}

func TestUpdateActionPlanReturnsAuthoritativeResult(t *testing.T) {
	env := newPlanAPITestEnv(t)

	rec := postActionPlan(t, env,
		`{"updates":[{"slots":[3000],"set":{"bid.bid_min_amount":5000}}]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp UpdateActionPlanResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "updated", resp.Status)
	require.Equal(t, []uint64{3000}, resp.Slots)
	require.Len(t, resp.Plans, 1)
	require.Equal(t, action_plan.ModeCustom, resp.Plans[0].Bid.Mode,
		"path update on absent category must create it as custom")
	require.Equal(t, uint64(5000), *resp.Plans[0].Bid.BidMinAmount)

	// Deleting reports a nil plan.
	rec = postActionPlan(t, env, `{"updates":[{"slots":[3000],"delete":true}]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Plans, 1)
	require.Nil(t, resp.Plans[0])
}

func TestUpdateActionPlanErrorMapping(t *testing.T) {
	env := newPlanAPITestEnv(t)

	// Past slot → 409.
	rec := postActionPlan(t, env, `{"updates":[{"slots":[10],"bid":{"mode":"disabled"}}]}`)
	assert.Equal(t, http.StatusConflict, rec.Code)

	// Frozen slot → 409.
	env.planSvc.Freeze(1500)

	rec = postActionPlan(t, env, `{"updates":[{"slots":[1500],"bid":{"mode":"disabled"}}]}`)
	assert.Equal(t, http.StatusConflict, rec.Code)

	// Validation error → 400.
	rec = postActionPlan(t, env, `{"updates":[{"slots":[2000],"bid":{"mode":"custom","bid_interval":-5}}]}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Unknown top-level field → 400 (strict decoding).
	rec = postActionPlan(t, env, `{"updates":[{"slots":[2000]}],"nope":1}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Malformed body → 400.
	rec = postActionPlan(t, env, `{`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGetSlotResultsRange(t *testing.T) {
	env := newPlanAPITestEnv(t)

	env.tracker.RecordBlockSubmission(2000, "epbs", string(slot_results.SubmissionStatusAccepted), "")
	env.tracker.RecordBlockSubmission(2050, "epbs", string(slot_results.SubmissionStatusAccepted), "")

	rec := httptest.NewRecorder()
	env.handler.GetSlotResults(rec,
		httptest.NewRequest(http.MethodGet, "/api/buildoor/slot-results?min_slot=1990&max_slot=2010", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	var resp SlotResultsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Results, 1)
	assert.Equal(t, phase0.Slot(2000), resp.Results[0].Slot)
	require.NotNil(t, resp.Results[0].AppliedPlan, "results carry the frozen applied plan")
}

func TestArtifactEndpointsNegotiation(t *testing.T) {
	env := newPlanAPITestEnv(t)

	payload := &eth2all.ExecutionPayload{
		Version:     version.DataVersionFulu,
		BlockNumber: 42,
		GasLimit:    30_000_000,
	}
	require.NoError(t, env.tracker.Artifacts().StorePayload(2000, version.DataVersionFulu, payload))

	newRequest := func(accept string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/buildoor/slot-results/2000/payload", nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}

		return mux.SetURLVars(req, map[string]string{"slot": "2000"})
	}

	// Default (no Accept) → JSON envelope with version + headers.
	rec := httptest.NewRecorder()
	env.handler.GetSlotPayloadArtifact(rec, newRequest(""))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "fulu", rec.Header().Get("Eth-Consensus-Version"))
	assert.Equal(t, "Accept", rec.Header().Get("Vary"))

	var envelope struct {
		Version string          `json:"version"`
		Data    json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, "fulu", envelope.Version)
	assert.Contains(t, string(envelope.Data), `"block_number":"42"`)

	// SSZ negotiation returns the exact stored bytes.
	expectedSSZ, err := payload.MarshalSSZ()
	require.NoError(t, err)

	rec = httptest.NewRecorder()
	env.handler.GetSlotPayloadArtifact(rec, newRequest("application/octet-stream"))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
	assert.True(t, bytes.Equal(expectedSSZ, rec.Body.Bytes()), "SSZ body must be byte-exact")

	// q-weighted preference: octet-stream wins at higher quality.
	rec = httptest.NewRecorder()
	env.handler.GetSlotPayloadArtifact(rec, newRequest("application/json;q=0.5, application/octet-stream"))
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))

	// Wildcard prefers JSON.
	rec = httptest.NewRecorder()
	env.handler.GetSlotPayloadArtifact(rec, newRequest("*/*"))
	assert.True(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json"))

	// Unsupported types → 406.
	rec = httptest.NewRecorder()
	env.handler.GetSlotPayloadArtifact(rec, newRequest("text/html"))
	assert.Equal(t, http.StatusNotAcceptable, rec.Code)

	// Missing artifact → 404.
	rec = httptest.NewRecorder()
	req := mux.SetURLVars(
		httptest.NewRequest(http.MethodGet, "/api/buildoor/slot-results/999/payload", nil),
		map[string]string{"slot": "999"})
	env.handler.GetSlotPayloadArtifact(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestBidArtifactListingAndFetch(t *testing.T) {
	env := newPlanAPITestEnv(t)

	bid := &eth2all.SignedExecutionPayloadBid{Version: version.DataVersionGloas}
	env.tracker.RecordBuilderAPIBid(2000, "gloas", bid, 7000, 500,
		string(slot_results.BidStatusServed), "")

	// Listing (JSON only).
	rec := httptest.NewRecorder()
	req := mux.SetURLVars(
		httptest.NewRequest(http.MethodGet, "/api/buildoor/slot-results/2000/bids", nil),
		map[string]string{"slot": "2000"})
	env.handler.GetSlotBidArtifacts(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var listing SlotBidArtifactsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listing))
	require.Len(t, listing.Bids, 1)
	assert.Equal(t, 0, listing.Bids[0].Index)
	assert.Equal(t, uint64(7000), listing.Bids[0].TotalValueGwei)
	assert.Equal(t, uint64(500), listing.Bids[0].ExecutionPaymentGwei)

	// Empty listing for a slot without bids.
	rec = httptest.NewRecorder()
	req = mux.SetURLVars(
		httptest.NewRequest(http.MethodGet, "/api/buildoor/slot-results/2001/bids", nil),
		map[string]string{"slot": "2001"})
	env.handler.GetSlotBidArtifacts(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listing))
	assert.Empty(t, listing.Bids)

	// Individual bid fetch by index (JSON branch decodes the Gloas bid).
	rec = httptest.NewRecorder()
	req = mux.SetURLVars(
		httptest.NewRequest(http.MethodGet, "/api/buildoor/slot-results/2000/bids/0", nil),
		map[string]string{"slot": "2000", "index": "0"})
	env.handler.GetSlotBidArtifact(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "gloas", rec.Header().Get("Eth-Consensus-Version"))

	// Invalid index → 400.
	rec = httptest.NewRecorder()
	req = mux.SetURLVars(
		httptest.NewRequest(http.MethodGet, "/api/buildoor/slot-results/2000/bids/x", nil),
		map[string]string{"slot": "2000", "index": "x"})
	env.handler.GetSlotBidArtifact(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestNegotiateArtifactMatrix(t *testing.T) {
	tests := []struct {
		accept        string
		wantSSZ       bool
		notAcceptable bool
	}{
		{accept: "", wantSSZ: false},
		{accept: "application/json", wantSSZ: false},
		{accept: "application/octet-stream", wantSSZ: true},
		{accept: "*/*", wantSSZ: false},
		{accept: "application/*", wantSSZ: false},
		{accept: "application/octet-stream;q=0.9, application/json;q=0.1", wantSSZ: true},
		{accept: "application/octet-stream;q=0.1, application/json;q=0.9", wantSSZ: false},
		{accept: "application/octet-stream;q=0", notAcceptable: true},
		{accept: "text/html", notAcceptable: true},
		{accept: "text/html;q=0.9, application/octet-stream;q=0.2", wantSSZ: true},
	}

	for _, tt := range tests {
		t.Run(tt.accept, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}

			wantSSZ, notAcceptable := negotiateArtifact(req)
			assert.Equal(t, tt.notAcceptable, notAcceptable, "notAcceptable")

			if !tt.notAcceptable {
				assert.Equal(t, tt.wantSSZ, wantSSZ, "wantSSZ")
			}
		})
	}
}

func TestUpdateSettingsPathBased(t *testing.T) {
	log := logrus.New()
	log.SetOutput(io.Discard)

	stateDB := db.NewDatabase(&db.Config{}, log)
	require.NoError(t, stateDB.Init())

	cfg := config.DefaultConfig()
	defaults := config.DefaultConfig()

	settingsSvc, err := config.NewService(cfg, defaults, map[string]bool{}, stateDB, log)
	require.NoError(t, err)

	authHandler, err := auth.NewAuthHandler(context.Background(), "")
	require.NoError(t, err)

	handler := NewAPIHandler(authHandler, settingsSvc, stateDB, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil)

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/config/settings",
			bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		handler.UpdateSettings(rec, req)

		return rec
	}

	// Partial update of two unrelated settings in one call.
	rec := post(`{"epbs.bid_subsidy": 12345, "schedule.mode": "every_nth"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, uint64(12345), cfg.EPBS.BidSubsidy)
	assert.Equal(t, config.ScheduleModeEveryN, cfg.Schedule.Mode)

	// Unknown key → 400, nothing applied.
	rec = post(`{"epbs.bid_subsidy": 1, "no.such.key": 2}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, uint64(12345), cfg.EPBS.BidSubsidy, "atomic: nothing applied on unknown key")

	// Invalid value → 400.
	rec = post(`{"slot_result_retention_epochs": 0}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Empty body → 400.
	rec = post(`{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
