package api

import (
	"math/big"
	"net/http"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gorilla/mux"

	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/tx_plan_verifier"
	"github.com/ethpandaops/buildoor/pkg/txpool"
)

// LocalBuildStatusResponse is the local build extension's status: the probed
// EL availability plus the effective global settings.
type LocalBuildStatusResponse struct {
	Availability  payload_builder.LocalBuildAvailability `json:"availability"`
	Enabled       bool                                   `json:"enabled"`
	PayloadSource string                                 `json:"payload_source"`
	TxSource      string                                 `json:"tx_source"`
	EngineEnabled bool                                   `json:"engine_enabled"`
	TxPool        TxPoolStatus                           `json:"txpool"`
	// PlanChecks is the process-lifetime tally of post-inclusion checks of
	// locally built blocks (nil without an EL RPC).
	PlanChecks *tx_plan_verifier.Counters `json:"plan_checks,omitempty"`
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
	writeJSON(w, http.StatusOK, localBuildStatus(h.builderSvc, h.planVerifier))
}

// localBuildStatus assembles the local build status (REST and the SSE
// local_build_status event).
func localBuildStatus(builderSvc *payload_builder.Service, verifier *tx_plan_verifier.Verifier) *LocalBuildStatusResponse {
	if builderSvc == nil {
		return &LocalBuildStatusResponse{TxPool: txPoolStatus(nil)}
	}

	cfg := builderSvc.GetConfig()

	resp := &LocalBuildStatusResponse{
		Availability:  builderSvc.LocalBuildAvailability(),
		Enabled:       cfg.LocalBuild.Enabled,
		PayloadSource: cfg.LocalBuild.NormalizedPayloadSource(),
		TxSource:      cfg.LocalBuild.NormalizedTxSource(),
		EngineEnabled: cfg.Build.EngineEnabled,
		TxPool:        txPoolStatus(builderSvc),
	}

	if verifier != nil {
		counters := verifier.Counters()
		resp.PlanChecks = &counters
	}

	return resp
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
		h.eventStreamMgr.sendLocalBuildStatus(true)
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
// @Failure 401 {object} map[string]string "Unauthorized (the pool holds private transactions)"
// @Failure 404 {object} map[string]string "Pool not configured"
// @Router /api/buildoor/txpool [get]
func (h *APIHandler) GetTxPool(w http.ResponseWriter, r *http.Request) {
	// The pool holds private transactions: only logged-in readers see it.
	if h.authHandler.CheckAuthToken(r.Header.Get("Authorization")) == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	pool := h.builderSvc.TxPool()
	if pool == nil {
		writeError(w, http.StatusNotFound, "transaction pool not configured (no --el-rpc)")
		return
	}

	opts, ok := listOptions(w, r)
	if !ok {
		return
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
		Offset: opts.Offset,
		Limit:  opts.Limit,
	})
}

// ELMempoolResponse is a page of the EL mempool mirror plus its stats.
type ELMempoolResponse struct {
	Stats  txpool.MirrorStats `json:"stats"`
	Txs    []txpool.TxSummary `json:"txs"`
	Total  int                `json:"total"`
	Offset int                `json:"offset"`
	Limit  int                `json:"limit"`
}

// GetELMempool godoc
// @Id getELMempool
// @Summary List the EL mempool mirror
// @Tags LocalBuild
// @Description Returns the state of the mirror of the EL mempool's pending
// @Description transactions (the testing build's fill source) and a page of
// @Description its content. Public: these are the EL's public transactions.
// @Description Reading it never starts the mirror, which runs only while
// @Description builds use it.
// @Produce json
// @Param offset query int false "Offset"
// @Param limit query int false "Limit (max 500)"
// @Param sender query string false "Sender address filter"
// @Param sort query string false "arrival | sender | tip"
// @Success 200 {object} ELMempoolResponse
// @Failure 404 {object} map[string]string "Pool not configured"
// @Router /api/buildoor/txpool/el-mempool [get]
func (h *APIHandler) GetELMempool(w http.ResponseWriter, r *http.Request) {
	pool := h.builderSvc.TxPool()
	if pool == nil {
		writeError(w, http.StatusNotFound, "transaction pool not configured (no --el-rpc)")
		return
	}

	opts, ok := listOptions(w, r)
	if !ok {
		return
	}

	txs, total := pool.ListELMempool(opts)

	summaries := make([]txpool.TxSummary, 0, len(txs))
	for _, tx := range txs {
		summaries = append(summaries, tx.Summary())
	}

	writeJSON(w, http.StatusOK, ELMempoolResponse{
		Stats:  pool.ELMempoolStats(),
		Txs:    summaries,
		Total:  total,
		Offset: opts.Offset,
		Limit:  opts.Limit,
	})
}

// GetTxPoolFees godoc
// @Id getTxPoolFees
// @Summary Pending gas by max fee
// @Tags LocalBuild
// @Description Buckets the pending transactions of the EL mempool mirror by
// @Description max fee per gas (log scale) against the next base fee and the
// @Description block gas limit. buildoor's own (private) transactions are
// @Description included, as their own series, only for logged-in readers.
// @Produce json
// @Success 200 {object} txpool.FeeDistribution
// @Failure 404 {object} map[string]string "Pool not configured or no head"
// @Failure 500 {object} map[string]string "EL unavailable"
// @Router /api/buildoor/txpool/fees [get]
func (h *APIHandler) GetTxPoolFees(w http.ResponseWriter, r *http.Request) {
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

	loggedIn := h.authHandler.CheckAuthToken(r.Header.Get("Authorization")) != nil

	dist, err := pool.FeeDistribution(r.Context(), common.Hash(headTracker.CurrentHead().ExecutionBlockHash), loggedIn)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, dist)
}

// listOptions parses the offset/limit/sender/sort query of a listing; false
// when it already answered 400.
func listOptions(w http.ResponseWriter, r *http.Request) (txpool.ListOptions, bool) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	if limit <= 0 {
		limit = 50
	}

	opts := txpool.ListOptions{Sort: r.URL.Query().Get("sort"), Offset: max(offset, 0), Limit: min(limit, 500)}

	if sender := r.URL.Query().Get("sender"); sender != "" {
		if !common.IsHexAddress(sender) {
			writeError(w, http.StatusBadRequest, "invalid sender address")
			return opts, false
		}

		addr := common.HexToAddress(sender)
		opts.Sender = &addr
	}

	return opts, true
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
// @Failure 401 {object} map[string]string "Unauthorized (the pool holds private transactions)"
// @Failure 404 {object} map[string]string "Pool not configured or no head"
// @Failure 500 {object} map[string]string "Selection failed"
// @Router /api/buildoor/txpool/preview [get]
func (h *APIHandler) GetTxPoolPreview(w http.ResponseWriter, r *http.Request) {
	// The pool holds private transactions: only logged-in readers see it.
	if h.authHandler.CheckAuthToken(r.Header.Get("Authorization")) == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

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
		BaseFeeCeiling: gweiToWei(cfg.TxPool.BaseFeeCeilingGwei),
		Ordering:       cfg.TxPool.NormalizedOrdering(),
		Seed:           uint64(slot),
		IncludeBlobTxs: availability.BlobBundle || cfg.LocalBuild.AllowBlobsWithoutBundle,
		BlobEncoding:   availability.BlobEncoding,
		CellProofs:     pool.CellProofsActive(),
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

// gweiToWei converts a gwei amount to wei (nil for 0).
func gweiToWei(gwei uint64) *big.Int {
	if gwei == 0 {
		return nil
	}

	return new(big.Int).Mul(new(big.Int).SetUint64(gwei), big.NewInt(1_000_000_000))
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
