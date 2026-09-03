package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/tx_intake"
	"github.com/ethpandaops/buildoor/pkg/tx_plan_verifier"
)

// TxQueueSenderEntry is one queued transaction in the API view.
type TxQueueSenderEntry struct {
	Hash    string `json:"hash"`
	Nonce   uint64 `json:"nonce"`
	Gas     uint64 `json:"gas"`
	Blobs   int    `json:"blobs,omitempty"`
	AgeMs   int64  `json:"age_ms"`
	Strikes int    `json:"strikes,omitempty"`
}

// TxQueueResponse is the intake queue view.
type TxQueueResponse struct {
	Enabled   bool                            `json:"enabled"`
	Source    string                          `json:"source"`
	Stats     *tx_intake.Stats                `json:"stats,omitempty"`
	Senders   map[string][]TxQueueSenderEntry `json:"senders,omitempty"`
	Evictions []tx_intake.Eviction            `json:"recent_evictions,omitempty"`
	Checks    *tx_plan_verifier.Counters      `json:"plan_checks,omitempty"`
}

// maxQueueEntriesInView bounds the per-sender listing so a full queue does
// not turn the endpoint into a multi-megabyte dump.
const maxQueueEntriesInView = 2000

// GetTxQueue returns the tx intake queue: stats, per-sender nonce chains,
// recent evictions and the tx plan verification tally.
//
//	@Summary		Tx intake queue
//	@Tags			buildoor
//	@Produce		json
//	@Param			senders	query		bool	false	"include per-sender entries"
//	@Success		200		{object}	TxQueueResponse
//	@Router			/buildoor/tx-queue [get]
func (h *APIHandler) GetTxQueue(w http.ResponseWriter, r *http.Request) {
	resp := TxQueueResponse{Source: h.settingsSvc.Load().Build.Source}

	tb := h.builderSvc.TestingBuilder()
	if tb == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.Enabled = true
	stats := tb.Queue().Stats()
	resp.Stats = &stats
	resp.Evictions = tb.Queue().RecentEvictions()

	if h.verifier != nil {
		counters := h.verifier.Counters()
		resp.Checks = &counters
	}

	if include, _ := strconv.ParseBool(r.URL.Query().Get("senders")); include {
		resp.Senders = make(map[string][]TxQueueSenderEntry)
		now := time.Now()
		listed := 0

		for sender, entries := range tb.Queue().Snapshot() {
			for _, e := range entries {
				if listed >= maxQueueEntriesInView {
					break
				}

				resp.Senders[sender.Hex()] = append(resp.Senders[sender.Hex()], TxQueueSenderEntry{
					Hash:    e.Hash().Hex(),
					Nonce:   e.Tx.Nonce(),
					Gas:     e.Tx.Gas(),
					Blobs:   len(e.Tx.BlobHashes()),
					AgeMs:   now.Sub(e.AddedAt).Milliseconds(),
					Strikes: e.Strikes(),
				})
				listed++
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// FlushTxQueue drops every queued transaction.
//
//	@Summary		Flush the tx intake queue
//	@Tags			buildoor
//	@Produce		json
//	@Success		200	{object}	map[string]any
//	@Router			/buildoor/tx-queue [delete]
func (h *APIHandler) FlushTxQueue(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	tb := h.builderSvc.TestingBuilder()
	if tb == nil {
		writeError(w, http.StatusNotFound, "tx intake is not configured (needs --el-rpc)")
		return
	}

	flushed := tb.Queue().Flush()
	h.audit(r, token, "tx_queue.flush", "", map[string]int{"flushed": flushed}, "ok")
	writeJSON(w, http.StatusOK, map[string]any{"status": "flushed", "flushed": flushed})
}

// UpdateTestingConfigRequest is the request for updating the build source
// and testing build settings.
type UpdateTestingConfigRequest struct {
	Source             *string `json:"source,omitempty"`
	FillGasPct         *uint64 `json:"fill_gas_pct,omitempty"`
	MaxTxs             *uint64 `json:"max_txs,omitempty"`
	MaxBlobs           *uint64 `json:"max_blobs,omitempty"`
	Policy             *string `json:"policy,omitempty"`
	BaseFeeCeilingGwei *uint64 `json:"base_fee_ceiling_gwei,omitempty"`
	BuildDeadlineMs    *int64  `json:"build_deadline_ms,omitempty"`
	OnFailure          *string `json:"on_failure,omitempty"`
	QueueMaxAgeSlots   *uint64 `json:"queue_max_age_slots,omitempty"`
	MaxAttempts        *uint64 `json:"max_attempts,omitempty"`
	MaxStrikes         *uint64 `json:"max_strikes,omitempty"`
}

// UpdateTestingConfig updates the build source and the testing build
// settings. Switching the source to testing requires a configured tx
// intake (--el-rpc) whose EL serves the testing namespace.
//
//	@Summary		Update testing build settings
//	@Tags			config
//	@Accept			json
//	@Produce		json
//	@Param			body	body		UpdateTestingConfigRequest	true	"settings"
//	@Success		200		{object}	map[string]string
//	@Router			/config/testing [post]
func (h *APIHandler) UpdateTestingConfig(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req UpdateTestingConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Source != nil && *req.Source == config.BuildSourceTesting {
		tb := h.builderSvc.TestingBuilder()
		if tb == nil {
			writeError(w, http.StatusBadRequest, "testing source needs --el-rpc pointing at a geth HTTP RPC with the testing namespace")
			return
		}

		if err := tb.Probe(r.Context()); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	updates := map[string]json.RawMessage{}
	set := func(key string, v any) { updates[key] = mustJSON(v) }

	if req.Source != nil {
		set(config.KeyBuildSource, *req.Source)
	}

	if req.FillGasPct != nil {
		set(config.KeyTestingFillGasPct, *req.FillGasPct)
	}

	if req.MaxTxs != nil {
		set(config.KeyTestingMaxTxs, *req.MaxTxs)
	}

	if req.MaxBlobs != nil {
		set(config.KeyTestingMaxBlobs, *req.MaxBlobs)
	}

	if req.Policy != nil {
		set(config.KeyTestingPolicy, *req.Policy)
	}

	if req.BaseFeeCeilingGwei != nil {
		set(config.KeyTestingBaseFeeCeilingGwei, *req.BaseFeeCeilingGwei)
	}

	if req.BuildDeadlineMs != nil {
		set(config.KeyTestingBuildDeadlineMs, *req.BuildDeadlineMs)
	}

	if req.OnFailure != nil {
		set(config.KeyTestingOnFailure, *req.OnFailure)
	}

	if req.QueueMaxAgeSlots != nil {
		set(config.KeyTestingQueueMaxAgeSlots, *req.QueueMaxAgeSlots)
	}

	if req.MaxAttempts != nil {
		set(config.KeyTestingMaxAttempts, *req.MaxAttempts)
	}

	if req.MaxStrikes != nil {
		set(config.KeyTestingMaxStrikes, *req.MaxStrikes)
	}

	if !h.applySettings(w, r, token, "config.testing", req, updates) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
