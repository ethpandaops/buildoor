package epbs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/bellatrix"
	gloasspec "github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// recordedBidCall captures one RecordBuilderAPIBid invocation.
type recordedBidCall struct {
	slot                 phase0.Slot
	fork                 string
	signedBid            any
	totalValueGwei       uint64
	executionPaymentGwei uint64
	status               string
	errMsg               string
}

// recordedSubmissionCall captures one RecordBlockSubmission invocation.
type recordedSubmissionCall struct {
	slot    phase0.Slot
	dialect string
	status  string
	errMsg  string
}

// stubSlotResultRecorder records all recorder calls for assertions.
type stubSlotResultRecorder struct {
	mu          sync.Mutex
	bids        []recordedBidCall
	submissions []recordedSubmissionCall
}

var _ SlotResultRecorder = (*stubSlotResultRecorder)(nil)

func (r *stubSlotResultRecorder) RecordBuilderAPIBid(slot phase0.Slot, forkName string, signedBid any,
	totalValueGwei, executionPaymentGwei uint64, status string, errMsg string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.bids = append(r.bids, recordedBidCall{
		slot:                 slot,
		fork:                 forkName,
		signedBid:            signedBid,
		totalValueGwei:       totalValueGwei,
		executionPaymentGwei: executionPaymentGwei,
		status:               status,
		errMsg:               errMsg,
	})
}

func (r *stubSlotResultRecorder) RecordBlockSubmission(slot phase0.Slot, dialect, status, errMsg string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.submissions = append(r.submissions, recordedSubmissionCall{
		slot:    slot,
		dialect: dialect,
		status:  status,
		errMsg:  errMsg,
	})
}

func (r *stubSlotResultRecorder) bidCalls() []recordedBidCall {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]recordedBidCall(nil), r.bids...)
}

func (r *stubSlotResultRecorder) submissionCalls() []recordedSubmissionCall {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]recordedSubmissionCall(nil), r.submissions...)
}

// testProposerPubkey is the proposer pubkey used by the payload-bid tests
// (0x11 repeated, matching the request path parameter).
func testProposerPubkey() phase0.BLSPubKey {
	var pk phase0.BLSPubKey
	for i := range pk {
		pk[i] = 0x11
	}

	return pk
}

// payloadBidTestEnv bundles a fully wired post-Gloas handler with an action
// plan service and a stub recorder for the plan-aware payload-bid tests.
type payloadBidTestEnv struct {
	cfg      *config.Config
	handler  *Handler
	planSvc  *action_plan.PlanService
	chainSvc *stubChainService
	recorder *stubSlotResultRecorder
}

// newPayloadBidTestEnv wires a bid-servable slot-1 environment: active
// builder, Gloas fork schedule, proposer preferences, cached payload
// (block value 2000 gwei), plan service, and recorder.
func newPayloadBidTestEnv(t *testing.T, enabled bool) *payloadBidTestEnv {
	t.Helper()

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	blsSigner, err := signer.NewBLSSigner("0x0000000000000000000000000000000000000000000000000000000000000001")
	require.NoError(t, err)

	cfg := &config.Config{
		APIPort:           8080,
		BuilderAPIEnabled: enabled,
	}

	chainSvc := &stubChainService{
		genesisTime:    time.Now().Add(-4 * time.Second),
		slotDuration:   4 * time.Second,
		currentFork:    version.DataVersionGloas,
		finalizedEpoch: 10,
		builderInfo: &chain.BuilderInfo{
			DepositEpoch:      1,
			WithdrawableEpoch: chain.FarFutureEpoch,
		},
		forkSchedule: []chain.ForkSchedule{
			{Fork: version.DataVersionGloas, Version: phase0.Version{0x06, 0x00, 0x00, 0x00}},
		},
	}

	// Bid serving is decided exclusively by the plan service (frozen per-slot
	// plans over the cfg.BuilderAPIEnabled baseline) — the handler's enabled
	// flag is deliberately never set.
	planSvc := action_plan.NewPlanService(cfg, chainSvc, log)

	h := NewHandler(&cfg.BuilderAPI, log, chainSvc, planSvc,
		payload_builder.NewPayloadCache(10), blsSigner)

	recorder := &stubSlotResultRecorder{}
	h.SetResultRecorder(recorder)

	propPrefs := memstore.New[phase0.Slot, *gloasspec.SignedProposerPreferences]()
	propPrefs.Put(1, &gloasspec.SignedProposerPreferences{
		Message: &gloasspec.ProposerPreferences{
			ProposalSlot: 1,
			FeeRecipient: bellatrix.ExecutionAddress{0x42},
		},
	})
	h.SetProposerPreferencesStore(propPrefs)

	seedGloasPayload(h, 1, phase0.Hash32{0xab}) // block value: 2000 gwei

	return &payloadBidTestEnv{
		cfg:      cfg,
		handler:  h,
		planSvc:  planSvc,
		chainSvc: chainSvc,
		recorder: recorder,
	}
}

// applyBuilderAPIPlan stores a builder_api category plan for the slot.
func applyBuilderAPIPlan(t *testing.T, planSvc *action_plan.PlanService, slot uint64, planJSON string) {
	t.Helper()

	_, err := planSvc.ApplyUpdates([]*action_plan.PlanUpdate{{
		Slots:      []uint64{slot},
		BuilderAPI: json.RawMessage(planJSON),
	}}, "test")
	require.NoError(t, err)
}

// newPayloadBidRequestCtx builds a slot-1 getExecutionPayloadBid request with
// the given context (the mux vars must be set after the context, they live
// inside it).
func newPayloadBidRequestCtx(ctx context.Context) *http.Request {
	zeroHash := "0x0000000000000000000000000000000000000000000000000000000000000000"
	proposerPubkey := "0x" + strings.Repeat("11", 48)
	req := httptest.NewRequest(http.MethodPost,
		"/eth/v1/builder/execution_payload_bid/1/"+zeroHash+"/"+zeroHash+"/"+proposerPubkey, nil).
		WithContext(ctx)

	return mux.SetURLVars(req, map[string]string{
		"slot":            "1",
		"parent_hash":     zeroHash,
		"parent_root":     zeroHash,
		"proposer_pubkey": proposerPubkey,
	})
}

func newPayloadBidRequest() *http.Request {
	return newPayloadBidRequestCtx(context.Background())
}

// newPayloadBidRequestForSlot builds a getExecutionPayloadBid request for an
// arbitrary slot.
func newPayloadBidRequestForSlot(slot uint64) *http.Request {
	zeroHash := "0x0000000000000000000000000000000000000000000000000000000000000000"
	proposerPubkey := "0x" + strings.Repeat("11", 48)
	slotStr := strconv.FormatUint(slot, 10)
	req := httptest.NewRequest(http.MethodPost,
		"/eth/v1/builder/execution_payload_bid/"+slotStr+"/"+zeroHash+"/"+zeroHash+"/"+proposerPubkey, nil)

	return mux.SetURLVars(req, map[string]string{
		"slot":            slotStr,
		"parent_hash":     zeroHash,
		"parent_root":     zeroHash,
		"proposer_pubkey": proposerPubkey,
	})
}

// decodeSignedExecutionPayloadBid parses a JSON getExecutionPayloadBid
// response body.
func decodeSignedExecutionPayloadBid(t *testing.T, body []byte) *eth2all.SignedExecutionPayloadBid {
	t.Helper()

	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))

	bid := &eth2all.SignedExecutionPayloadBid{Version: version.DataVersionGloas}
	require.NoError(t, json.Unmarshal(envelope.Data, bid))
	require.NotNil(t, bid.Message)

	return bid
}

// TestHandleGetExecutionPayloadBid_PlanEffectiveEnable verifies the frozen
// plan overrides the global enable flag in both directions and that the
// outcome is recorded.
func TestHandleGetExecutionPayloadBid_PlanEffectiveEnable(t *testing.T) {
	tests := []struct {
		name           string
		enabled        bool
		planJSON       string // empty = no plan for the slot
		wantCode       int
		wantStatus     string
		wantBidPresent bool
	}{
		{
			name:           "custom plan force-serves although globally disabled",
			enabled:        false,
			planJSON:       `{"mode":"custom"}`,
			wantCode:       http.StatusOK,
			wantStatus:     bidStatusServed,
			wantBidPresent: true,
		},
		{
			name:       "disabled plan suppresses although globally enabled",
			enabled:    true,
			planJSON:   `{"mode":"disabled"}`,
			wantCode:   http.StatusNoContent,
			wantStatus: bidStatusSuppressed,
		},
		{
			name:       "no plan inherits global disable",
			enabled:    false,
			wantCode:   http.StatusNoContent,
			wantStatus: bidStatusSuppressed,
		},
		{
			name:           "no plan inherits global enable",
			enabled:        true,
			wantCode:       http.StatusOK,
			wantStatus:     bidStatusServed,
			wantBidPresent: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newPayloadBidTestEnv(t, test.enabled)

			if test.planJSON != "" {
				applyBuilderAPIPlan(t, env.planSvc, 1, test.planJSON)
			}

			rec := httptest.NewRecorder()
			env.handler.HandleGetExecutionPayloadBid(rec, newPayloadBidRequest())

			assert.Equal(t, test.wantCode, rec.Code)

			calls := env.recorder.bidCalls()
			require.Len(t, calls, 1)
			assert.Equal(t, phase0.Slot(1), calls[0].slot)
			assert.Equal(t, "gloas", calls[0].fork)
			assert.Equal(t, test.wantStatus, calls[0].status)

			if test.wantBidPresent {
				assert.NotNil(t, calls[0].signedBid, "served record must carry the signed bid")
			} else {
				assert.Nil(t, calls[0].signedBid)
				assert.Zero(t, calls[0].totalValueGwei)
			}
		})
	}
}

// TestHandleGetExecutionPayloadBid_PlanValueResolution verifies the frozen
// per-slot value settings: the absolute total replaces blockValue+subsidy
// BEFORE the execution-payment split, and a per-slot subsidy override is
// added to the block value. The seeded payload's block value is 2000 gwei.
func TestHandleGetExecutionPayloadBid_PlanValueResolution(t *testing.T) {
	tests := []struct {
		name                string
		planJSON            string
		maxExecutionPayment phase0.Gwei // builder preferences for the proposer
		wantBidValueGwei    phase0.Gwei // trustless on-chain part after the split
		wantTotalGwei       uint64
		wantExecGwei        uint64
	}{
		{
			name:             "absolute total without execution payment cap",
			planJSON:         `{"mode":"custom","total_value_override_gwei":3000}`,
			wantBidValueGwei: 3000,
			wantTotalGwei:    3000,
		},
		{
			name:                "absolute total replaces value before the payment split",
			planJSON:            `{"mode":"custom","total_value_override_gwei":3000}`,
			maxExecutionPayment: 1000,
			wantBidValueGwei:    2000, // 3000 total - 1000 execution payment
			wantTotalGwei:       3000,
			wantExecGwei:        1000,
		},
		{
			name:             "per-slot subsidy added to block value",
			planJSON:         `{"mode":"custom","value_subsidy_gwei":500}`,
			wantBidValueGwei: 2500, // 2000 block value + 500 subsidy
			wantTotalGwei:    2500,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newPayloadBidTestEnv(t, true)
			applyBuilderAPIPlan(t, env.planSvc, 1, test.planJSON)

			if test.maxExecutionPayment > 0 {
				env.handler.GetBuilderPreferencesStore().Set(testProposerPubkey(), test.maxExecutionPayment)
			}

			rec := httptest.NewRecorder()
			env.handler.HandleGetExecutionPayloadBid(rec, newPayloadBidRequest())

			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

			bid := decodeSignedExecutionPayloadBid(t, rec.Body.Bytes())
			assert.Equal(t, test.wantBidValueGwei, bid.Message.Value)

			calls := env.recorder.bidCalls()
			require.Len(t, calls, 1)
			assert.Equal(t, bidStatusServed, calls[0].status)
			assert.Equal(t, test.wantTotalGwei, calls[0].totalValueGwei)
			assert.Equal(t, test.wantExecGwei, calls[0].executionPaymentGwei)
		})
	}
}

// TestHandleGetExecutionPayloadBid_PlanResponseDelay verifies a planned
// response delay is waited out before serving, and that a request cancelled
// during the delay records "cancelled" and writes no body.
func TestHandleGetExecutionPayloadBid_PlanResponseDelay(t *testing.T) {
	t.Run("cancelled during delay writes no body", func(t *testing.T) {
		env := newPayloadBidTestEnv(t, true)
		applyBuilderAPIPlan(t, env.planSvc, 1, `{"mode":"custom","response_delay_ms":3000}`)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // already cancelled: the delay select must take the ctx branch

		rec := httptest.NewRecorder()
		start := time.Now()
		env.handler.HandleGetExecutionPayloadBid(rec, newPayloadBidRequestCtx(ctx))

		assert.Less(t, time.Since(start), 2*time.Second, "cancelled delay must not be waited out")
		assert.Zero(t, rec.Body.Len(), "no response body may be written after cancellation")
		assert.Empty(t, rec.Header().Get("Eth-Consensus-Version"), "no response headers may be set")

		calls := env.recorder.bidCalls()
		require.Len(t, calls, 1)
		assert.Equal(t, bidStatusCancelled, calls[0].status)
		assert.NotNil(t, calls[0].signedBid, "the constructed bid stays inspectable on cancellation")
	})

	t.Run("delay elapses then serves", func(t *testing.T) {
		env := newPayloadBidTestEnv(t, true)
		applyBuilderAPIPlan(t, env.planSvc, 1, `{"mode":"custom","response_delay_ms":100}`)

		rec := httptest.NewRecorder()
		start := time.Now()
		env.handler.HandleGetExecutionPayloadBid(rec, newPayloadBidRequest())

		assert.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond, "the delay must be waited out")
		require.Equal(t, http.StatusOK, rec.Code)

		calls := env.recorder.bidCalls()
		require.Len(t, calls, 1)
		assert.Equal(t, bidStatusServed, calls[0].status)
	})
}

// TestHandleGetExecutionPayloadBid_RecordingDedupe verifies repeated
// identical bid responses are recorded (and thus captured as artifacts) only
// once while every request is still served normally.
func TestHandleGetExecutionPayloadBid_RecordingDedupe(t *testing.T) {
	env := newPayloadBidTestEnv(t, true)

	for range 3 {
		rec := httptest.NewRecorder()
		env.handler.HandleGetExecutionPayloadBid(rec, newPayloadBidRequest())
		require.Equal(t, http.StatusOK, rec.Code, "every poll must still be served")
	}

	calls := env.recorder.bidCalls()
	require.Len(t, calls, 1, "identical served bids must be recorded once")
	assert.Equal(t, bidStatusServed, calls[0].status)
	assert.Equal(t, uint64(3), env.handler.BidsRequested())
}

// TestHandleGetExecutionPayloadBid_GateFailureRecorded verifies an
// availability gate blocking an effectively-enabled slot records a "failed"
// outcome with the observed gate.
func TestHandleGetExecutionPayloadBid_GateFailureRecorded(t *testing.T) {
	env := newPayloadBidTestEnv(t, true)

	// Evict the slot's payload → the cache gate blocks with 204.
	env.handler.payloadCache = payload_builder.NewPayloadCache(10)

	rec := httptest.NewRecorder()
	env.handler.HandleGetExecutionPayloadBid(rec, newPayloadBidRequest())

	assert.Equal(t, http.StatusNoContent, rec.Code)

	calls := env.recorder.bidCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, bidStatusFailed, calls[0].status)
	assert.Contains(t, calls[0].errMsg, "no cached payload")
}

// TestHandleSubmitBeaconBlock_RecordsSubmissions verifies beacon block
// submissions record "received" after decode, "accepted" after broadcast +
// reveal handoff, and "failed" on a broadcast error.
func TestHandleSubmitBeaconBlock_RecordsSubmissions(t *testing.T) {
	t.Run("success records received then accepted", func(t *testing.T) {
		env := newBeaconBlockTestEnv(t, 4*time.Second, 3500)
		recorder := &stubSlotResultRecorder{}
		env.handler.SetResultRecorder(recorder)

		require.NoError(t, env.revealSvc.Start(context.Background()))
		defer env.revealSvc.Stop()

		slot := phase0.Slot(1)
		blockHash := phase0.Hash32{0xab}
		seedGloasPayload(env.handler, slot, blockHash)

		rec := postBeaconBlock(env.handler, signedBeaconBlockJSON(t, slot, blockHash))
		require.Equal(t, http.StatusAccepted, rec.Code)

		calls := recorder.submissionCalls()
		require.Len(t, calls, 2)
		assert.Equal(t, recordedSubmissionCall{slot: slot, dialect: submissionDialect,
			status: submissionStatusReceived}, calls[0])
		assert.Equal(t, recordedSubmissionCall{slot: slot, dialect: submissionDialect,
			status: submissionStatusAccepted}, calls[1])
	})

	t.Run("broadcast failure records received then failed", func(t *testing.T) {
		env := newBeaconBlockTestEnv(t, 4*time.Second, 3500)
		env.broadcaster.err = assert.AnError

		recorder := &stubSlotResultRecorder{}
		env.handler.SetResultRecorder(recorder)

		require.NoError(t, env.revealSvc.Start(context.Background()))
		defer env.revealSvc.Stop()

		slot := phase0.Slot(1)
		blockHash := phase0.Hash32{0xab}
		seedGloasPayload(env.handler, slot, blockHash)

		rec := postBeaconBlock(env.handler, signedBeaconBlockJSON(t, slot, blockHash))
		require.Equal(t, http.StatusInternalServerError, rec.Code)

		calls := recorder.submissionCalls()
		require.Len(t, calls, 2)
		assert.Equal(t, submissionStatusReceived, calls[0].status)
		assert.Equal(t, submissionStatusFailed, calls[1].status)
		assert.Contains(t, calls[1].errMsg, "failed to broadcast")
	})
}

// TestHandleGetExecutionPayloadBid_SlotHorizonBound verifies far-ahead
// requests are rejected with 400 BEFORE the slot's plan is frozen, so
// arbitrary clients cannot lock future plans against edits.
func TestHandleGetExecutionPayloadBid_SlotHorizonBound(t *testing.T) {
	tests := []struct {
		name        string
		currentSlot phase0.Slot
		requestSlot uint64
		wantCode    int
		wantFrozen  bool
	}{
		{
			name:        "next slot is within the horizon",
			currentSlot: 0, requestSlot: 1,
			wantCode: http.StatusOK, wantFrozen: true,
		},
		{
			name:        "two slots ahead is rejected without freezing",
			currentSlot: 1, requestSlot: 3,
			wantCode: http.StatusBadRequest, wantFrozen: false,
		},
		{
			name:        "far future is rejected without freezing",
			currentSlot: 1, requestSlot: 1_000_000,
			wantCode: http.StatusBadRequest, wantFrozen: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newPayloadBidTestEnv(t, true)
			env.chainSvc.currentSlot = tt.currentSlot

			rec := httptest.NewRecorder()
			env.handler.HandleGetExecutionPayloadBid(rec, newPayloadBidRequestForSlot(tt.requestSlot))

			require.Equal(t, tt.wantCode, rec.Code)
			require.Equal(t, tt.wantFrozen, env.planSvc.IsFrozen(phase0.Slot(tt.requestSlot)),
				"freeze marker state")

			if tt.wantCode == http.StatusBadRequest {
				require.Empty(t, env.recorder.bidCalls(), "rejected requests must not record")
			}
		})
	}
}
