package api

import (
	"net/http"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gorilla/mux"

	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/txpool"
)

// LocalBuildStatusResponse is the local build extension's status: the probed
// EL availability plus the effective global settings.
type LocalBuildStatusResponse struct {
	Availability   payload_builder.LocalBuildAvailability `json:"availability"`
	Enabled        bool                                   `json:"enabled"`
	PayloadSource  string                                 `json:"payload_source"`
	TxSource       string                                 `json:"tx_source"`
	BuildELPayload bool                                   `json:"build_el_payload"`
	TxPool         TxPoolStatus                           `json:"txpool"`
}

// TxPoolStatus is the pool's availability and toggle state.
type TxPoolStatus struct {
	Available bool   `json:"available"`
	Enabled   bool   `json:"enabled"`
	Reason    string `json:"reason,omitempty"`
	// IngressPath is where generators submit transactions (relative to the
	// API base URL).
	IngressPath string `json:"ingress_path,omitempty"`
}

// txPoolStatus derives the pool status from the builder service's wiring.
func txPoolStatus(builderSvc *payload_builder.Service) TxPoolStatus {
	if builderSvc == nil {
		return TxPoolStatus{Reason: "builder unavailable"}
	}

	pool := builderSvc.TxPool()
	if pool == nil {
		return TxPoolStatus{Reason: "no --el-rpc configured"}
	}

	available, reason := pool.Available()

	return TxPoolStatus{
		Available:   available,
		Enabled:     pool.Enabled(),
		Reason:      reason,
		IngressPath: "/rpc",
	}
}

// fillLocalBuildStatus adds the local build / pool fields to a service status.
func fillLocalBuildStatus(status *ServiceStatusEvent, builderSvc *payload_builder.Service) {
	if builderSvc == nil {
		return
	}

	availability := builderSvc.LocalBuildAvailability()
	status.LocalBuildAvailable = availability.Available
	status.LocalBuildEnabled = builderSvc.GetConfig().LocalBuild.Enabled
	status.LocalBuildReason = availability.Reason

	pool := txPoolStatus(builderSvc)
	status.TxPoolAvailable = pool.Available
	status.TxPoolEnabled = pool.Enabled
}

// GetLocalBuildStatus godoc
// @Id getLocalBuildStatus
// @Summary Get the local build (testing_buildBlockV1) status
// @Tags LocalBuild
// @Description Returns whether the EL exposes testing_buildBlockV1 (probed),
// @Description the per-EL blob handling, the effective local build settings
// @Description and the transaction pool's availability and toggle state.
// @Produce json
// @Success 200 {object} LocalBuildStatusResponse
// @Router /api/buildoor/local-build/status [get]
func (h *APIHandler) GetLocalBuildStatus(w http.ResponseWriter, _ *http.Request) {
	cfg := h.builderSvc.GetConfig()

	writeJSON(w, http.StatusOK, LocalBuildStatusResponse{
		Availability:   h.builderSvc.LocalBuildAvailability(),
		Enabled:        cfg.LocalBuild.Enabled,
		PayloadSource:  cfg.LocalBuild.NormalizedPayloadSource(),
		TxSource:       cfg.LocalBuild.NormalizedTxSource(),
		BuildELPayload: cfg.LocalBuild.BuildELPayload,
		TxPool:         txPoolStatus(h.builderSvc),
	})
}

// ProbeLocalBuild godoc
// @Id probeLocalBuild
// @Summary Re-probe the EL for testing_buildBlockV1
// @Tags LocalBuild
// @Description Probes the EL RPC for the testing namespace now (instead of
// @Description waiting for the periodic check) and returns the resulting
// @Description availability. Requires authentication; audited.
// @Produce json
// @Success 200 {object} payload_builder.LocalBuildAvailability
// @Failure 401 {object} map[string]string "Unauthorized"
// @Router /api/buildoor/local-build/probe [post]
func (h *APIHandler) ProbeLocalBuild(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	availability := h.builderSvc.ProbeLocalBuild(r.Context())

	h.audit(r, token, "local_build.probe", "local_build", availability, "ok")

	if h.eventStreamMgr != nil {
		h.eventStreamMgr.BroadcastServiceStatus()
	}

	writeJSON(w, http.StatusOK, availability)
}

// TxPoolResponse is a page of the pool's content plus its stats.
type TxPoolResponse struct {
	Stats  txpool.Stats       `json:"stats"`
	Txs    []txpool.TxSummary `json:"txs"`
	Total  int                `json:"total"`
	Offset int                `json:"offset"`
	Limit  int                `json:"limit"`
}

// GetTxPool godoc
// @Id getTxPool
// @Summary List the transaction pool content
// @Tags LocalBuild
// @Description Returns the pool's aggregate stats (pending count, senders, gas
// @Description sum, value, blobs, lifetime counters) and a page of queued
// @Description transactions.
// @Produce json
// @Param offset query int false "Offset (default 0)"
// @Param limit query int false "Limit (default 50, max 500)"
// @Param sender query string false "Filter by sender address"
// @Param sort query string false "arrival (default) | sender | tip"
// @Success 200 {object} TxPoolResponse
// @Failure 404 {object} map[string]string "Pool not configured"
// @Router /api/buildoor/txpool [get]
func (h *APIHandler) GetTxPool(w http.ResponseWriter, r *http.Request) {
	pool := h.builderSvc.TxPool()
	if pool == nil {
		writeError(w, http.StatusNotFound, "transaction pool not configured (no --el-rpc)")
		return
	}

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}

	if limit > 500 {
		limit = 500
	}

	opts := txpool.ListOptions{Sort: r.URL.Query().Get("sort"), Offset: offset, Limit: limit}

	if sender := r.URL.Query().Get("sender"); sender != "" {
		if !common.IsHexAddress(sender) {
			writeError(w, http.StatusBadRequest, "invalid sender address")
			return
		}

		addr := common.HexToAddress(sender)
		opts.Sender = &addr
	}

	txs, total := pool.List(opts)

	summaries := make([]txpool.TxSummary, 0, len(txs))
	for _, tx := range txs {
		summaries = append(summaries, tx.Summary())
	}

	writeJSON(w, http.StatusOK, TxPoolResponse{
		Stats:  pool.Stats(),
		Txs:    summaries,
		Total:  total,
		Offset: offset,
		Limit:  limit,
	})
}

// TxPoolPreviewResponse is a dry-run selection against the current head.
type TxPoolPreviewResponse struct {
	Selection  *txpool.Summary `json:"selection"`
	ParentHash string          `json:"parent_hash"`
	GasLimit   uint64          `json:"gas_limit"`
	MaxBlobs   uint64          `json:"max_blobs"`
	Ordering   string          `json:"ordering"`
}

// GetTxPoolPreview godoc
// @Id getTxPoolPreview
// @Summary Preview the next local block's selection
// @Tags LocalBuild
// @Description Runs the pool selection against the current head with the
// @Description live settings and returns what a local block built now would
// @Description contain (count, gas sum, skipped-by-reason), without building.
// @Produce json
// @Success 200 {object} TxPoolPreviewResponse
// @Failure 404 {object} map[string]string "Pool not configured or no head"
// @Failure 500 {object} map[string]string "Selection failed"
// @Router /api/buildoor/txpool/preview [get]
func (h *APIHandler) GetTxPoolPreview(w http.ResponseWriter, r *http.Request) {
	pool := h.builderSvc.TxPool()
	if pool == nil {
		writeError(w, http.StatusNotFound, "transaction pool not configured (no --el-rpc)")
		return
	}

	headTracker := h.chainSvc.GetHeadTracker()
	if headTracker == nil || headTracker.CurrentHead() == nil {
		writeError(w, http.StatusNotFound, "no chain head yet")
		return
	}

	head := headTracker.CurrentHead()
	cfg := h.builderSvc.GetConfig()
	availability := h.builderSvc.LocalBuildAvailability()
	slot := h.chainSvc.GetCurrentSlot() + 1
	maxBlobs := h.chainSvc.GetChainSpec().MaxBlobsPerBlockAt(h.chainSvc.GetEpochOfSlot(slot))

	params := &txpool.SelectParams{
		ParentHash:     common.Hash(head.ExecutionBlockHash),
		GasLimit:       head.GasLimit,
		MaxBlobs:       maxBlobs,
		MaxTxs:         cfg.TxPool.MaxTxsPerBlock,
		GasFillPct:     cfg.TxPool.EffectiveGasFillPct(),
		Ordering:       cfg.TxPool.NormalizedOrdering(),
		Seed:           uint64(slot),
		IncludeBlobTxs: availability.BlobBundle || cfg.LocalBuild.AllowBlobsWithoutBundle,
		BlobEncoding:   availability.BlobEncoding,
	}

	selection, err := pool.Select(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "selection failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, TxPoolPreviewResponse{
		Selection:  selection.Summary(),
		ParentHash: common.Hash(head.ExecutionBlockHash).Hex(),
		GasLimit:   head.GasLimit,
		MaxBlobs:   maxBlobs,
		Ordering:   params.Ordering,
	})
}

// ClearTxPool godoc
// @Id clearTxPool
// @Summary Drop every queued transaction
// @Tags LocalBuild
// @Description Clears the transaction pool. Requires authentication; audited.
// @Produce json
// @Success 200 {object} map[string]int "dropped count"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 404 {object} map[string]string "Pool not configured"
// @Router /api/buildoor/txpool [delete]
func (h *APIHandler) ClearTxPool(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	pool := h.builderSvc.TxPool()
	if pool == nil {
		writeError(w, http.StatusNotFound, "transaction pool not configured (no --el-rpc)")
		return
	}

	dropped := pool.Clear()

	h.audit(r, token, "txpool.clear", "txpool", map[string]int{"dropped": dropped}, "ok")

	writeJSON(w, http.StatusOK, map[string]int{"dropped": dropped})
}

// DropTxPoolTx godoc
// @Id dropTxPoolTx
// @Summary Drop one queued transaction
// @Tags LocalBuild
// @Description Removes the transaction with the given hash from the pool.
// @Description Requires authentication; audited.
// @Produce json
// @Param hash path string true "Transaction hash"
// @Success 200 {object} map[string]bool "removed"
// @Failure 400 {object} map[string]string "Bad Request"
// @Failure 401 {object} map[string]string "Unauthorized"
// @Failure 404 {object} map[string]string "Not found"
// @Router /api/buildoor/txpool/{hash} [delete]
func (h *APIHandler) DropTxPoolTx(w http.ResponseWriter, r *http.Request) {
	token := h.authHandler.CheckAuthToken(r.Header.Get("Authorization"))
	if token == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	pool := h.builderSvc.TxPool()
	if pool == nil {
		writeError(w, http.StatusNotFound, "transaction pool not configured (no --el-rpc)")
		return
	}

	hashHex := mux.Vars(r)["hash"]
	if len(hashHex) != 66 || hashHex[:2] != "0x" {
		writeError(w, http.StatusBadRequest, "invalid transaction hash")
		return
	}

	hash := common.HexToHash(hashHex)

	removed := pool.Remove(hash)

	h.audit(r, token, "txpool.drop", hash.Hex(), nil, map[bool]string{true: "ok", false: "not_found"}[removed])

	if !removed {
		writeError(w, http.StatusNotFound, "transaction not in pool")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"removed": true})
}
