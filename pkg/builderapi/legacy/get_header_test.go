package legacy

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	legacytypes "github.com/ethpandaops/buildoor/pkg/builderapi/legacy/types"
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

// getHeaderTestEnv bundles a fully wired legacy handler with an action plan
// service and a stub recorder for the plan-aware getHeader tests.
type getHeaderTestEnv struct {
	cfg      *config.Config
	handler  *Handler
	planSvc  *action_plan.PlanService
	chainSvc *stubChainService
	recorder *stubSlotResultRecorder
	pubkey   phase0.BLSPubKey
}

// newGetHeaderTestEnv wires a legacy handler backed by a plan service sharing
// one config; bid serving is decided exclusively by the plan service (frozen
// per-slot plans over the cfg.BuilderAPIEnabled baseline — the handler's
// enabled flag is deliberately never set). A validator registration and a
// slot-1 payload (blockValueWei) are seeded.
func newGetHeaderTestEnv(t *testing.T, enabled bool, blockValueWei *big.Int) *getHeaderTestEnv {
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
		currentFork: version.DataVersionFulu,
		chainSpec: &chain.ChainSpec{
			SecondsPerSlot: 12 * time.Second,
			SlotsPerEpoch:  32,
		},
	}

	planSvc := action_plan.NewPlanService(cfg, chainSvc, log)

	store := memstore.New[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration]()
	h := NewHandler(&cfg.BuilderAPI, log, chainSvc, planSvc, payload_builder.NewPayloadCache(10),
		store, blsSigner)

	recorder := &stubSlotResultRecorder{}
	h.SetResultRecorder(recorder)

	pk := blsSigner.PublicKey()
	store.Put(pk, &apiv1.SignedValidatorRegistration{})
	seedPayload(h, blockValueWei)

	return &getHeaderTestEnv{
		cfg:      cfg,
		handler:  h,
		planSvc:  planSvc,
		chainSvc: chainSvc,
		recorder: recorder,
		pubkey:   pk,
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

// newGetHeaderRequestFor builds a slot-1 getHeader request for the pubkey.
func newGetHeaderRequestFor(pubkey phase0.BLSPubKey) *http.Request {
	return newGetHeaderRequestCtx(context.Background(), pubkey)
}

// newGetHeaderRequestForSlot builds a getHeader request for an arbitrary slot.
func newGetHeaderRequestForSlot(pubkey phase0.BLSPubKey, slot uint64) *http.Request {
	zeroHash := "0x0000000000000000000000000000000000000000000000000000000000000000"
	slotStr := strconv.FormatUint(slot, 10)
	req := httptest.NewRequest(http.MethodGet,
		"/eth/v1/builder/header/"+slotStr+"/"+zeroHash+"/0x"+hex.EncodeToString(pubkey[:]), nil)

	return mux.SetURLVars(req, map[string]string{
		"slot":        slotStr,
		"parent_hash": zeroHash,
		"pubkey":      "0x" + hex.EncodeToString(pubkey[:]),
	})
}

// newGetHeaderRequestCtx builds a slot-1 getHeader request with the given
// context (the mux vars must be set after the context, they live inside it).
func newGetHeaderRequestCtx(ctx context.Context, pubkey phase0.BLSPubKey) *http.Request {
	zeroHash := "0x0000000000000000000000000000000000000000000000000000000000000000"
	req := httptest.NewRequest(http.MethodGet,
		"/eth/v1/builder/header/1/"+zeroHash+"/0x"+hex.EncodeToString(pubkey[:]), nil).WithContext(ctx)

	return mux.SetURLVars(req, map[string]string{
		"slot":        "1",
		"parent_hash": zeroHash,
		"pubkey":      "0x" + hex.EncodeToString(pubkey[:]),
	})
}

// decodeSignedBuilderBid parses a JSON getHeader response body.
func decodeSignedBuilderBid(t *testing.T, body []byte, fork version.DataVersion) *legacytypes.SignedBuilderBid {
	t.Helper()

	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))

	bid := &legacytypes.SignedBuilderBid{Version: fork}
	require.NoError(t, json.Unmarshal(envelope.Data, bid))
	require.NotNil(t, bid.Message)

	return bid
}

// TestHandleGetHeader_PlanEffectiveEnable verifies the frozen plan overrides
// the global enable flag in both directions and that the outcome is recorded.
func TestHandleGetHeader_PlanEffectiveEnable(t *testing.T) {
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
			env := newGetHeaderTestEnv(t, test.enabled, big.NewInt(1_000_000_000))

			if test.planJSON != "" {
				applyBuilderAPIPlan(t, env.planSvc, 1, test.planJSON)
			}

			rec := httptest.NewRecorder()
			env.handler.HandleGetHeader(rec, newGetHeaderRequestFor(env.pubkey))

			assert.Equal(t, test.wantCode, rec.Code)

			calls := env.recorder.bidCalls()
			require.Len(t, calls, 1)
			assert.Equal(t, phase0.Slot(1), calls[0].slot)
			assert.Equal(t, "fulu", calls[0].fork)
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

// TestHandleGetHeader_PlanValueResolution verifies the frozen per-slot value
// settings: an absolute total replaces blockValue+subsidy entirely (including
// totals whose wei amount exceeds uint64), and a per-slot subsidy override is
// added to the block value. All wei math must happen in uint256.
func TestHandleGetHeader_PlanValueResolution(t *testing.T) {
	gwei := big.NewInt(1_000_000_000)

	tests := []struct {
		name          string
		planJSON      string
		blockValueWei *big.Int
		wantValueWei  *big.Int
		wantGweiTotal uint64
	}{
		{
			name:          "absolute total above block value",
			planJSON:      `{"mode":"custom","total_value_override_gwei":5000}`,
			blockValueWei: big.NewInt(1_000_000_000), // 1 gwei
			wantValueWei:  new(big.Int).Mul(big.NewInt(5000), gwei),
			wantGweiTotal: 5000,
		},
		{
			name:          "absolute total overflowing uint64 wei",
			planJSON:      `{"mode":"custom","total_value_override_gwei":20000000000}`,
			blockValueWei: big.NewInt(1_000_000_000),
			// 2e19 wei > math.MaxUint64 — must survive the uint256 path.
			wantValueWei:  new(big.Int).Mul(big.NewInt(20_000_000_000), gwei),
			wantGweiTotal: 20_000_000_000,
		},
		{
			name:          "per-slot subsidy added to block value",
			planJSON:      `{"mode":"custom","value_subsidy_gwei":7}`,
			blockValueWei: big.NewInt(1_000_000_000), // 1 gwei
			wantValueWei:  new(big.Int).Mul(big.NewInt(8), gwei),
			wantGweiTotal: 8,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newGetHeaderTestEnv(t, true, test.blockValueWei)
			applyBuilderAPIPlan(t, env.planSvc, 1, test.planJSON)

			rec := httptest.NewRecorder()
			env.handler.HandleGetHeader(rec, newGetHeaderRequestFor(env.pubkey))

			require.Equal(t, http.StatusOK, rec.Code)

			bid := decodeSignedBuilderBid(t, rec.Body.Bytes(), version.DataVersionFulu)
			assert.Equal(t, 0, bid.Message.Value.ToBig().Cmp(test.wantValueWei),
				"bid value: got %s wei, want %s wei", bid.Message.Value.ToBig(), test.wantValueWei)

			calls := env.recorder.bidCalls()
			require.Len(t, calls, 1)
			assert.Equal(t, bidStatusServed, calls[0].status)
			assert.Equal(t, test.wantGweiTotal, calls[0].totalValueGwei)
		})
	}
}

// TestHandleGetHeader_PlanResponseDelay verifies a planned response delay is
// waited out before serving, and that a request cancelled during the delay
// records "cancelled" and writes no body.
func TestHandleGetHeader_PlanResponseDelay(t *testing.T) {
	t.Run("cancelled during delay writes no body", func(t *testing.T) {
		env := newGetHeaderTestEnv(t, true, big.NewInt(1_000_000_000))
		applyBuilderAPIPlan(t, env.planSvc, 1, `{"mode":"custom","response_delay_ms":5000}`)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // already cancelled: the delay select must take the ctx branch

		rec := httptest.NewRecorder()
		start := time.Now()
		env.handler.HandleGetHeader(rec, newGetHeaderRequestCtx(ctx, env.pubkey))

		assert.Less(t, time.Since(start), 2*time.Second, "cancelled delay must not be waited out")
		assert.Zero(t, rec.Body.Len(), "no response body may be written after cancellation")
		assert.Empty(t, rec.Header().Get("Eth-Consensus-Version"), "no response headers may be set")

		calls := env.recorder.bidCalls()
		require.Len(t, calls, 1)
		assert.Equal(t, bidStatusCancelled, calls[0].status)
		assert.NotNil(t, calls[0].signedBid, "the constructed bid stays inspectable on cancellation")
	})

	t.Run("delay elapses then serves", func(t *testing.T) {
		env := newGetHeaderTestEnv(t, true, big.NewInt(1_000_000_000))
		applyBuilderAPIPlan(t, env.planSvc, 1, `{"mode":"custom","response_delay_ms":100}`)

		rec := httptest.NewRecorder()
		start := time.Now()
		env.handler.HandleGetHeader(rec, newGetHeaderRequestFor(env.pubkey))

		assert.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond, "the delay must be waited out")
		require.Equal(t, http.StatusOK, rec.Code)

		calls := env.recorder.bidCalls()
		require.Len(t, calls, 1)
		assert.Equal(t, bidStatusServed, calls[0].status)
	})
}

// failingResponseWriter fails every body write (headers still work).
type failingResponseWriter struct {
	http.ResponseWriter
}

func (f *failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("connection lost")
}

// TestHandleGetHeader_ServedRecordedOnlyAfterWrite verifies "served" is only
// recorded when the response write succeeded: a failing writer yields a
// "failed" record instead.
func TestHandleGetHeader_ServedRecordedOnlyAfterWrite(t *testing.T) {
	env := newGetHeaderTestEnv(t, true, big.NewInt(1_000_000_000))

	req := newGetHeaderRequestFor(env.pubkey)
	req.Header.Set("Accept", "application/octet-stream")

	rec := httptest.NewRecorder()
	env.handler.HandleGetHeader(&failingResponseWriter{ResponseWriter: rec}, req)

	calls := env.recorder.bidCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, bidStatusFailed, calls[0].status)
	assert.Contains(t, calls[0].errMsg, "failed to write")
	assert.NotNil(t, calls[0].signedBid)
}

// TestHandleGetHeader_RecordingDedupe verifies repeated identical getHeader
// responses are recorded (and thus captured as artifacts) only once while
// every request is still served normally.
func TestHandleGetHeader_RecordingDedupe(t *testing.T) {
	env := newGetHeaderTestEnv(t, true, big.NewInt(1_000_000_000))

	for range 3 {
		rec := httptest.NewRecorder()
		env.handler.HandleGetHeader(rec, newGetHeaderRequestFor(env.pubkey))
		require.Equal(t, http.StatusOK, rec.Code, "every poll must still be served")
	}

	calls := env.recorder.bidCalls()
	require.Len(t, calls, 1, "identical served bids must be recorded once")
	assert.Equal(t, bidStatusServed, calls[0].status)
	assert.Equal(t, uint64(3), env.handler.HeadersRequested())
}

// TestHandleGetHeader_GateFailureRecorded verifies an availability gate
// blocking an effectively-enabled slot records a "failed" outcome with the
// observed gate.
func TestHandleGetHeader_GateFailureRecorded(t *testing.T) {
	env := newGetHeaderTestEnv(t, true, big.NewInt(1_000_000_000))

	// Unknown proposer pubkey → registration gate blocks with 204.
	var unknown phase0.BLSPubKey
	unknown[0] = 0x99

	rec := httptest.NewRecorder()
	env.handler.HandleGetHeader(rec, newGetHeaderRequestFor(unknown))

	assert.Equal(t, http.StatusNoContent, rec.Code)

	calls := env.recorder.bidCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, bidStatusFailed, calls[0].status)
	assert.Contains(t, calls[0].errMsg, "not registered")
}

// TestHandleGetHeader_SlotHorizonBound verifies far-ahead requests are
// rejected with 400 BEFORE the slot's plan is frozen, so arbitrary clients
// cannot lock future plans against edits, while current+1 stays servable.
func TestHandleGetHeader_SlotHorizonBound(t *testing.T) {
	tests := []struct {
		name        string
		currentSlot phase0.Slot
		requestSlot uint64
		wantCode    int
		wantFrozen  bool
	}{
		{
			name:        "current slot is served",
			currentSlot: 1, requestSlot: 1,
			wantCode: http.StatusOK, wantFrozen: true,
		},
		{
			name:        "next slot is served",
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
			env := newGetHeaderTestEnv(t, true, big.NewInt(1_000_000_000))
			env.chainSvc.currentSlot = tt.currentSlot

			// Seed the payload cache for the requested slot so in-horizon
			// requests can actually serve.
			seedPayloadAtSlot(env.handler, phase0.Slot(tt.requestSlot), big.NewInt(1_000_000_000))

			rec := httptest.NewRecorder()
			env.handler.HandleGetHeader(rec, newGetHeaderRequestForSlot(env.pubkey, tt.requestSlot))

			assert.Equal(t, tt.wantCode, rec.Code)
			assert.Equal(t, tt.wantFrozen, env.planSvc.IsFrozen(phase0.Slot(tt.requestSlot)),
				"freeze marker state")

			if tt.wantCode == http.StatusBadRequest {
				assert.Empty(t, env.recorder.bidCalls(), "rejected requests must not record")
			}
		})
	}
}
