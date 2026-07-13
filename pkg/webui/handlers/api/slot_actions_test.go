package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers/auth"
)

// stubChainService fakes the current slot; every other chain.Service method
// panics via the embedded nil interface (they must not be called here).
type stubChainService struct {
	chain.Service
	currentSlot phase0.Slot
	nextSlot    *phase0.Slot
	calls       int
}

func (s *stubChainService) GetCurrentSlot() phase0.Slot {
	s.calls++
	if s.calls > 1 && s.nextSlot != nil {
		return *s.nextSlot
	}

	return s.currentSlot
}

// epbsUpdateResponse decodes the UpdateEPBS response (success and error).
type epbsUpdateResponse struct {
	Status      string                                `json:"status"`
	SlotActions map[string]*payload_bidder.SlotAction `json:"slot_actions"`
	Error       string                                `json:"error"`
}

// newSlotActionsTestHandler builds an APIHandler in open-auth mode with a
// fake current slot. builderSvc is nil so no event stream manager starts;
// settingsSvc is unused because slot-action-only requests carry no scalar
// updates (an empty update set is a no-op).
func newSlotActionsTestHandler(t *testing.T, currentSlot phase0.Slot,
	store *payload_bidder.SlotActionsStore) *APIHandler {
	t.Helper()

	authHandler, err := auth.NewAuthHandler(context.Background(), "")
	require.NoError(t, err)

	chainSvc := &stubChainService{currentSlot: currentSlot}

	return NewAPIHandler(authHandler, nil, nil, nil, nil, nil, chainSvc,
		nil, nil, nil, nil, nil, nil, nil, store)
}

func postEPBS(t *testing.T, h *APIHandler, body string) (int, epbsUpdateResponse) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/api/config/epbs", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.UpdateEPBS(rec, req)

	var resp epbsUpdateResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))

	return rec.Code, resp
}

func TestUpdateEPBS_SlotActionsReplaceAndClear(t *testing.T) {
	store := payload_bidder.NewSlotActionsStore()
	h := newSlotActionsTestHandler(t, 10, store)

	// Configure two future slots.
	code, resp := postEPBS(t, h,
		`{"slot_actions":{"15":{"reveal":"withhold"},"20":{"reveal":"withhold"}}}`)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "updated", resp.Status)
	require.Len(t, resp.SlotActions, 2)
	assert.Equal(t, payload_bidder.RevealActionWithhold, resp.SlotActions["15"].Reveal)
	assert.Equal(t, payload_bidder.RevealActionWithhold, resp.SlotActions["20"].Reveal)

	// Supplying slot_actions replaces the complete pending future set.
	code, resp = postEPBS(t, h, `{"slot_actions":{"25":{"reveal":"withhold"}}}`)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, resp.SlotActions, 1)
	assert.Contains(t, resp.SlotActions, "25")

	// An empty object clears it.
	code, resp = postEPBS(t, h, `{"slot_actions":{}}`)
	require.Equal(t, http.StatusOK, code)
	assert.Empty(t, resp.SlotActions)
	assert.Empty(t, store.Snapshot())
}

func TestUpdateEPBS_SlotActionsImmutableOnceStarted(t *testing.T) {
	store := payload_bidder.NewSlotActionsStore()
	h := newSlotActionsTestHandler(t, 10, store)

	code, _ := postEPBS(t, h, `{"slot_actions":{"15":{"reveal":"withhold"}}}`)
	require.Equal(t, http.StatusOK, code)

	// Slot 15 has started: reconfiguring it is rejected...
	late := newSlotActionsTestHandler(t, 15, store)
	code, resp := postEPBS(t, late, `{"slot_actions":{"15":{"reveal":"withhold"}}}`)
	require.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "slot_actions: slot 15 is not in the future (current slot 15)", resp.Error)

	// ...and clearing leaves the in-flight action in place.
	code, resp = postEPBS(t, late, `{"slot_actions":{}}`)
	require.Equal(t, http.StatusOK, code)
	require.Len(t, resp.SlotActions, 1)
	assert.Contains(t, resp.SlotActions, "15")
}

func TestUpdateEPBS_SlotActionsRejectsBoundaryCrossing(t *testing.T) {
	store := payload_bidder.NewSlotActionsStore()
	h := newSlotActionsTestHandler(t, 10, store)

	// The first sample sees slot 10, but the commit-point sample sees that the
	// target slot has started. It must not be inserted using the stale sample.
	started := phase0.Slot(15)
	h.chainSvc.(*stubChainService).nextSlot = &started

	code, resp := postEPBS(t, h, `{"slot_actions":{"15":{"reveal":"withhold"}}}`)
	require.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "slot_actions: slot 15 is not in the future (current slot 15)", resp.Error)
	assert.Empty(t, store.Snapshot())
}

func TestUpdateEPBS_SlotActionsValidation(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "malformed slot key",
			body:    `{"slot_actions":{"abc":{"reveal":"withhold"}}}`,
			wantErr: `slot_actions: invalid slot key "abc": must be a decimal slot number`,
		},
		{
			name:    "negative slot key",
			body:    `{"slot_actions":{"-1":{"reveal":"withhold"}}}`,
			wantErr: `slot_actions: invalid slot key "-1": must be a decimal slot number`,
		},
		{
			name:    "fractional slot key",
			body:    `{"slot_actions":{"1.5":{"reveal":"withhold"}}}`,
			wantErr: `slot_actions: invalid slot key "1.5": must be a decimal slot number`,
		},
		{
			name:    "empty slot key",
			body:    `{"slot_actions":{"":{"reveal":"withhold"}}}`,
			wantErr: `slot_actions: invalid slot key "": must be a decimal slot number`,
		},
		{
			name:    "empty action",
			body:    `{"slot_actions":{"15":{}}}`,
			wantErr: `slot_actions: no action specified for slot 15`,
		},
		{
			name:    "unknown action",
			body:    `{"slot_actions":{"15":{"bid":"mutate"}}}`,
			wantErr: `slot_actions: unknown action "bid" for slot 15 (supported: "reveal")`,
		},
		{
			name:    "unknown reveal action",
			body:    `{"slot_actions":{"15":{"reveal":"delay"}}}`,
			wantErr: `slot_actions: unknown reveal action "delay" for slot 15 (supported: "withhold")`,
		},
		{
			name:    "current slot",
			body:    `{"slot_actions":{"10":{"reveal":"withhold"}}}`,
			wantErr: `slot_actions: slot 10 is not in the future (current slot 10)`,
		},
		{
			name:    "past slot",
			body:    `{"slot_actions":{"5":{"reveal":"withhold"}}}`,
			wantErr: `slot_actions: slot 5 is not in the future (current slot 10)`,
		},
		{
			name:    "one bad slot rejects the whole request",
			body:    `{"slot_actions":{"15":{"reveal":"withhold"},"5":{"reveal":"withhold"}}}`,
			wantErr: `slot_actions: slot 5 is not in the future (current slot 10)`,
		},
		{
			name:    "non-object action",
			body:    `{"slot_actions":{"15":"withhold"}}`,
			wantErr: `invalid request body`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := payload_bidder.NewSlotActionsStore()
			h := newSlotActionsTestHandler(t, 10, store)

			code, resp := postEPBS(t, h, tt.body)
			assert.Equal(t, http.StatusBadRequest, code)
			assert.Equal(t, tt.wantErr, resp.Error)
			assert.Empty(t, store.Snapshot(), "a rejected request must not touch the store")
		})
	}
}

func TestUpdateEPBS_SlotActionsAbsentLeavesStore(t *testing.T) {
	store := payload_bidder.NewSlotActionsStore()
	h := newSlotActionsTestHandler(t, 10, store)

	code, _ := postEPBS(t, h, `{"slot_actions":{"15":{"reveal":"withhold"}}}`)
	require.Equal(t, http.StatusOK, code)

	// No slot_actions field → existing actions stay; the response still
	// echoes the effective stored set.
	code, resp := postEPBS(t, h, `{}`)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "updated", resp.Status)
	require.Len(t, resp.SlotActions, 1)
	assert.Contains(t, resp.SlotActions, "15")
	require.Len(t, store.Snapshot(), 1)
}

func TestUpdateEPBS_SlotActionsNotSupported(t *testing.T) {
	// No store wired (Gloas not scheduled) → deterministic rejection.
	h := newSlotActionsTestHandler(t, 10, nil)

	code, resp := postEPBS(t, h, `{"slot_actions":{"15":{"reveal":"withhold"}}}`)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "slot_actions: not supported: Gloas/ePBS is not scheduled on this network", resp.Error)
}

func TestEventStreamManager_BroadcastWithheld(t *testing.T) {
	mgr := NewEventStreamManager(nil, nil, nil, nil, nil, nil, nil, nil, nil)
	client := make(chan *StreamEvent, 1)
	mgr.AddClient(client)

	defer mgr.RemoveClient(client)
	defer mgr.Stop()

	before := time.Now().UnixMilli()
	mgr.BroadcastWithheld(&payload_bidder.RevealResult{
		Slot:          384,
		Withheld:      true,
		Action:        payload_bidder.RevealActionWithhold,
		BuilderIndex:  42,
		BuilderPubkey: "0xabc",
	})
	after := time.Now().UnixMilli()

	select {
	case event := <-client:
		assert.Equal(t, EventTypeIntentionallyWithheld, event.Type)
		assert.GreaterOrEqual(t, event.Timestamp, before)
		assert.LessOrEqual(t, event.Timestamp, after)

		data, ok := event.Data.(WithheldStreamEvent)
		require.True(t, ok)
		assert.Equal(t, uint64(384), data.Slot)
		assert.Equal(t, uint64(42), data.BuilderIndex)
		assert.Equal(t, "0xabc", data.BuilderPubkey)
		assert.Equal(t, payload_bidder.RevealActionWithhold, data.Action)
		assert.Equal(t, "intentionally_withheld", data.Status)
		assert.Equal(t, event.Timestamp, data.Timestamp)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for intentionally_withheld event")
	}
}

func TestConfigWithSlotActions(t *testing.T) {
	cfg := &config.Config{BuilderPrivkey: "secret"}
	store := payload_bidder.NewSlotActionsStore()
	store.ReplaceFuture(map[phase0.Slot]*payload_bidder.SlotAction{
		384: {Reveal: payload_bidder.RevealActionWithhold},
	}, 10)

	out := configWithSlotActions(cfg, store)
	assert.Equal(t, "***", out["builder_privkey"])

	epbs, ok := out["epbs"].(map[string]any)
	require.True(t, ok)
	actions, ok := epbs["slot_actions"].(map[string]*payload_bidder.SlotAction)
	require.True(t, ok)
	require.Contains(t, actions, "384")
	assert.Equal(t, payload_bidder.RevealActionWithhold, actions["384"].Reveal)
}
