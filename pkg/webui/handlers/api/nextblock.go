package api

import (
	"context"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/gorilla/mux"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
	"github.com/ethpandaops/buildoor/pkg/txpool"
)

// nextBlockMaxAge bounds how long a next block estimate is served before it
// is recomputed: the endpoint is public, and every estimate reads the parent
// state of every candidate sender from the EL.
const nextBlockMaxAge = 2 * time.Second

// nextBlockMaxTxs caps the transaction list of an estimate.
const nextBlockMaxTxs = 200

// NextBlockResponse is the estimate of the next block's testing build: the
// selection the configured transaction source would make on the current head
// right now. The aggregates are public, the transactions need a login.
type NextBlockResponse struct {
	// Slot is the slot of the block that extends the current head (the slot
	// after the head, or the current slot after missed slots).
	Slot       uint64 `json:"slot"`
	ParentHash string `json:"parent_hash"`
	GasLimit   uint64 `json:"gas_limit"`
	MaxBlobs   uint64 `json:"max_blobs"`
	// TxSource is the configured transaction source; Ordering the private
	// pool's order (private_first and txpool).
	TxSource string `json:"tx_source"`
	Ordering string `json:"ordering"`
	// LocalBuildEnabled is false when the testing build is off: the estimate
	// then shows what it would select.
	LocalBuildEnabled bool `json:"local_build_enabled"`
	// PublicMempool is mirrored (the EL mempool is part of the estimate),
	// idle (the source takes it, but the mirror is not running: no build
	// asked for it lately) or unused (the source does not take it).
	PublicMempool string `json:"public_mempool"`
	// Note explains an estimate that cannot be made (the source leaves the
	// choice to the EL, or builds empty blocks).
	Note string `json:"note,omitempty"`
	// Selection carries the aggregates (counts, gas, blobs, skips and the
	// split by origin); its pool transaction hashes need a login.
	Selection *txpool.Summary `json:"selection,omitempty"`
	// Txs are the block's transactions in block order (login only, at most
	// nextBlockMaxTxs).
	Txs        []NextBlockTx `json:"txs,omitempty"`
	TxsTotal   int           `json:"txs_total,omitempty"`
	ComputedAt int64         `json:"computed_at"`
	// Basis is what the estimated block follows: head (a block on the
	// current head), candidate (the slot after our current slot candidate,
	// BasisBlock, assumed included) or head_only (the slot after a current
	// slot block nobody here built: nothing assumed).
	Basis      string `json:"basis"`
	BasisBlock string `json:"basis_block,omitempty"`
}

// NextBlockTx is one transaction of the estimate.
type NextBlockTx struct {
	Hash   string `json:"hash"`
	From   string `json:"from"`
	Nonce  uint64 `json:"nonce"`
	Type   uint8  `json:"type"`
	Origin string `json:"origin"` // inclusion_list, private or public
	Gas    uint64 `json:"gas"`
	// Tip is the effective priority fee per gas over the base fee, wei.
	Tip   string `json:"tip"`
	Blobs int    `json:"blobs,omitempty"`
	// GasUsed is the gas the transaction used (on-chain blocks only).
	GasUsed uint64 `json:"gas_used,omitempty"`
}

// nextBlockCache shares one estimate between the viewers of a head.
type nextBlockCache struct {
	mu        sync.Mutex
	count     uint64
	head      phase0.Root
	slot      phase0.Slot   // the current slot (a missed slot moves the target)
	candidate phase0.Hash32 // our current slot candidate (the second estimate assumes it)
	version   uint64
	at        time.Time
	resp      *NextBlockEstimates
}

// NextBlockEstimates are the estimates of the blocks to come: the block on
// the current head, and, while the current slot's block is not on chain
// yet, the slot after it.
type NextBlockEstimates struct {
	Estimates []*NextBlockResponse `json:"estimates"`
	// Version counts the estimate sets: the next_block event carries it, so
	// a logged-in reader fetches the transactions of exactly that set.
	Version    uint64 `json:"version"`
	ComputedAt int64  `json:"computed_at"`
}

// public is the set without the transactions and the pool hashes.
func (e *NextBlockEstimates) public() *NextBlockEstimates {
	out := *e
	out.Estimates = make([]*NextBlockResponse, len(e.Estimates))

	for i, estimate := range e.Estimates {
		out.Estimates[i] = estimate.public()
	}

	return &out
}

// GetNextBlock godoc
// @Id getNextBlock
// @Summary Estimate the next blocks
// @Tags LocalBuild
// @Description Runs the testing build's transaction selection with the live
// @Description settings — the inclusion list first, then the configured
// @Description transaction source (txpool, private_first or most_profitable)
// @Description — without building: for the block on the current head and,
// @Description while the current slot's block is not on chain yet, for the
// @Description slot after it, assuming our current slot candidate gets
// @Description included (its transactions out, their senders' nonces and
// @Description balances past them, the base fee after it). Public: the
// @Description aggregates; the transactions and pool hashes need a login.
// @Description Estimates are shared between viewers and pushed as the
// @Description next_block event.
// @Produce json
// @Success 200 {object} NextBlockEstimates
// @Failure 404 {object} map[string]string "Pool not configured or no head"
// @Failure 500 {object} map[string]string "Selection failed"
// @Router /next-block [get]
func (h *APIHandler) GetNextBlock(w http.ResponseWriter, r *http.Request) {
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

	estimates, err := h.nextBlock(r.Context(), pool)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "selection failed: "+err.Error())
		return
	}

	if !h.isLoggedIn(r) {
		estimates = estimates.public()
	}

	writeJSON(w, http.StatusOK, estimates)
}

// public is the estimate without the transactions and the pool hashes.
func (r *NextBlockResponse) public() *NextBlockResponse {
	public := *r
	public.Txs = nil

	if r.Selection != nil {
		selection := *r.Selection
		selection.Hashes = nil
		public.Selection = &selection
	}

	return &public
}

// nextBlockFeedTick is how often the next_block feed checks for a new head,
// a new slot, a new candidate or changed pools.
const nextBlockFeedTick = 250 * time.Millisecond

// runNextBlockFeed keeps the next_block event current while viewers are
// connected: a new estimate set at once on a new head, slot or candidate, at
// most every nextBlockMaxAge on pool changes (the shared estimate's own
// rule).
func (h *APIHandler) runNextBlockFeed(ctx context.Context) {
	pool := h.builderSvc.TxPool()
	if pool == nil || h.chainSvc == nil {
		return
	}

	ticker := time.NewTicker(nextBlockFeedTick)
	defer ticker.Stop()

	var last *NextBlockEstimates

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if h.eventStreamMgr.ClientCount() == 0 {
			continue
		}

		if tracker := h.chainSvc.GetHeadTracker(); tracker == nil || tracker.CurrentHead() == nil {
			continue
		}

		estimates, err := h.nextBlock(ctx, pool)
		if err != nil || estimates == last {
			continue
		}

		last = estimates
		h.eventStreamMgr.BroadcastNextBlock(estimates.public())
	}
}

// nextBlock returns the shared estimate set, recomputed when the head, the
// slot or the current slot's candidate moved, or the pools changed and the
// last set is older than nextBlockMaxAge.
func (h *APIHandler) nextBlock(ctx context.Context, pool *txpool.Pool) (*NextBlockEstimates, error) {
	head := h.chainSvc.GetHeadTracker().CurrentHead()
	currentSlot := h.chainSvc.GetCurrentSlot()
	candidate := h.builderSvc.GetPayloadCache().Get(currentSlot)
	contentVersion := pool.ContentVersion()

	var candidateHash phase0.Hash32
	if candidate != nil {
		candidateHash = candidate.BlockHash
	}

	h.nextBlockCache.mu.Lock()
	defer h.nextBlockCache.mu.Unlock()

	cache := &h.nextBlockCache
	if cache.resp != nil && cache.head == head.Root && cache.slot == currentSlot && cache.candidate == candidateHash &&
		(cache.version == contentVersion || time.Since(cache.at) < nextBlockMaxAge) {
		return cache.resp, nil
	}

	// The block on the current head: the slot after it, or the current slot
	// when slots were missed since.
	first := max(head.Slot+1, currentSlot)

	onHead, err := h.estimateNextBlock(ctx, pool, first, nil, nil)
	if err != nil {
		return nil, err
	}

	onHead.Basis = "head"
	estimates := []*NextBlockResponse{onHead}

	// The current slot's block is not on chain yet: the slot after it,
	// assuming our candidate for it gets included.
	if first == currentSlot {
		preceding, baseFee := precedingBlock(pool, candidate, currentSlot+1)

		next, err := h.estimateNextBlock(ctx, pool, currentSlot+1, preceding, baseFee)
		if err != nil {
			return nil, err
		}

		next.Basis = "head_only"
		if candidate != nil {
			next.Basis = "candidate"
			next.BasisBlock = common.Hash(candidate.BlockHash).Hex()
		}

		estimates = append(estimates, next)
	}

	cache.count++

	resp := &NextBlockEstimates{Estimates: estimates, Version: cache.count, ComputedAt: time.Now().UnixMilli()}
	cache.head, cache.slot, cache.candidate, cache.version, cache.at, cache.resp =
		head.Root, currentSlot, candidateHash, contentVersion, time.Now(), resp

	return resp, nil
}

// precedingBlock is a candidate's transactions (senders recovered) and the
// base fee of the block of slot after it.
func precedingBlock(
	pool *txpool.Pool, candidate *payload_builder.Payload, slot phase0.Slot,
) ([]*txpool.PooledTx, *big.Int) {
	if candidate == nil || candidate.ExecutionPayload == nil {
		return nil, nil
	}

	exec := candidate.ExecutionPayload
	txs := make([]*txpool.PooledTx, 0, len(exec.Transactions))

	for _, encoded := range exec.Transactions {
		tx := new(types.Transaction)
		if err := tx.UnmarshalBinary(encoded); err != nil {
			continue
		}

		sender, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
		if err != nil {
			continue
		}

		txs = append(txs, &txpool.PooledTx{Hash: tx.Hash(), Tx: tx, Sender: sender})
	}

	var baseFee *big.Int
	if exec.BaseFeePerGas != nil {
		baseFee = pool.NextBaseFee(&types.Header{
			GasLimit: exec.GasLimit,
			GasUsed:  exec.GasUsed,
			BaseFee:  exec.BaseFeePerGas.ToBig(),
			Time:     exec.Timestamp,
		}, slot)
	}

	return txs, baseFee
}

// estimateNextBlock runs the selection for a block of the given slot on the
// current head, after the preceding transactions (none: the head's block is
// the parent).
func (h *APIHandler) estimateNextBlock(
	ctx context.Context,
	pool *txpool.Pool,
	slot phase0.Slot,
	preceding []*txpool.PooledTx,
	baseFee *big.Int,
) (*NextBlockResponse, error) {
	head := h.chainSvc.GetHeadTracker().CurrentHead()
	cfg := h.builderSvc.GetConfig()
	availability := h.builderSvc.LocalBuildAvailability()
	maxBlobs := h.chainSvc.GetChainSpec().MaxBlobsPerBlockAt(h.chainSvc.GetEpochOfSlot(slot))
	source := cfg.LocalBuild.NormalizedTxSource()

	resp := &NextBlockResponse{
		Slot:              uint64(slot),
		ParentHash:        common.Hash(head.ExecutionBlockHash).Hex(),
		GasLimit:          head.GasLimit,
		MaxBlobs:          maxBlobs,
		TxSource:          source,
		Ordering:          cfg.TxPool.NormalizedOrdering(),
		LocalBuildEnabled: cfg.LocalBuild.Enabled,
		PublicMempool:     "unused",
		ComputedAt:        time.Now().UnixMilli(),
	}

	switch {
	case source == config.TxSourceELMempool:
		resp.Note = "The transaction source is el_mempool: the EL picks the transactions."
		return resp, nil
	case !config.TxSourceSelects(source):
		resp.Note = "The transaction source builds empty blocks."
		return resp, nil
	}

	// The inclusion list of a block on the head is known; the one of the
	// slot after a block not on chain yet is not.
	var inclusionList [][]byte
	if len(preceding) == 0 && slot == head.Slot+1 {
		inclusionList = h.nextInclusionList(ctx, cfg, slot-1, head.Root)
	}

	params := &txpool.SelectParams{
		ParentHash:     common.Hash(head.ExecutionBlockHash),
		GasLimit:       head.GasLimit,
		MaxBlobs:       maxBlobs,
		MaxTxs:         cfg.TxPool.MaxTxsPerBlock,
		GasFillPct:     cfg.TxPool.EffectiveGasFillPct(),
		BaseFeeCeiling: gweiToWei(cfg.TxPool.BaseFeeCeilingGwei),
		Ordering:       resp.Ordering,
		Slot:           slot,
		Seed:           uint64(slot),
		InclusionList:  inclusionList,
		IncludeBlobTxs: availability.BlobBundle || cfg.LocalBuild.AllowBlobsWithoutBundle,
		// The estimate never reaches the EL: the canonical encoding needs no
		// sidecar, so the mirrored blob transactions count as a build that
		// fetches their blobs would take them.
		BlobEncoding:   config.BlobEncodingCanonical,
		CellProofs:     pool.CellProofsActive(),
		MostProfitable: source == config.TxSourceMostProfitable,
		Preceding:      preceding,
		BaseFee:        baseFee,
	}

	if config.TxSourceUsesELMempool(source) {
		if pending, active := pool.ELMempoolIfActive(); active {
			params.Fill = pending
			resp.PublicMempool = "mirrored"
		} else {
			resp.PublicMempool = "idle"
		}
	}

	selection, err := pool.Select(ctx, params)
	if err != nil {
		return nil, err
	}

	resp.Selection = selection.Summary()
	resp.TxsTotal = len(selection.Ordered)
	resp.Txs = nextBlockTxs(selection)

	return resp, nil
}

// nextInclusionList is the inclusion list view (of list slot ilSlot) the
// builds of the slot after it carry on parentRoot (Heze on; none in omit
// mode).
func (h *APIHandler) nextInclusionList(ctx context.Context, cfg *config.Config, ilSlot phase0.Slot, parentRoot phase0.Root) [][]byte {
	store := h.builderSvc.InclusionLists()
	if store == nil || h.chainSvc.ActiveForkAtEpoch(h.chainSvc.GetEpochOfSlot(ilSlot+1)) < version.DataVersionHeze {
		return nil
	}

	if config.NormalizedInclusionListMode(cfg.Build.InclusionListMode, config.InclusionListInclude) == config.InclusionListOmit {
		return nil
	}

	return store.View(ctx, ilSlot, parentRoot).Transactions
}

// nextBlockTxs lists the selection's transactions in block order with their
// origin.
func nextBlockTxs(selection *txpool.Selection) []NextBlockTx {
	origin := make(map[common.Hash]string, len(selection.Ordered))

	for _, tx := range selection.InclusionList {
		origin[tx.Hash] = "inclusion_list"
	}

	for _, tx := range selection.Fill {
		origin[tx.Hash] = "public"
	}

	txs := make([]NextBlockTx, 0, min(len(selection.Ordered), nextBlockMaxTxs))

	for _, tx := range selection.Ordered[:min(len(selection.Ordered), nextBlockMaxTxs)] {
		from := origin[tx.Hash]
		if from == "" {
			from = "private"
		}

		txs = append(txs, NextBlockTx{
			Hash:   tx.Hash.Hex(),
			From:   tx.Sender.Hex(),
			Nonce:  tx.Tx.Nonce(),
			Type:   tx.Tx.Type(),
			Origin: from,
			Gas:    tx.Tx.Gas(),
			Tip:    txpool.EffectiveTip(tx.Tx, selection.BaseFee).String(),
			Blobs:  len(tx.Tx.BlobHashes()),
		})
	}

	return txs
}

// PayloadTransactionsResponse lists a block's transactions.
type PayloadTransactionsResponse struct {
	BlockHash string `json:"block_hash"`
	// Ours marks one of our payloads (classified by its build's own
	// accounting); OnChain that the EL has the block.
	Ours     bool   `json:"ours"`
	OnChain  bool   `json:"on_chain"`
	GasLimit uint64 `json:"gas_limit"`
	GasUsed  uint64 `json:"gas_used"`
	BaseFee  string `json:"base_fee"`
	// Value is what the block pays its fee recipient in priority fees, wei:
	// our build's reported block value, or the effective tips times the gas
	// the transactions used (empty while unknown).
	Value string `json:"value,omitempty"`
	// Txs are in block order. Origin is inclusion_list (on the list the
	// block had to satisfy), private (from our private mempool) or public.
	Txs []NextBlockTx `json:"txs"`
}

// GetPayloadTransactions godoc
// @Id getPayloadTransactions
// @Summary List a block's transactions
// @Tags LocalBuild
// @Description The transactions of one of our recent payloads or of any
// @Description recent block on chain, in block order, with their origin
// @Description (inclusion list, private, public), effective tip and gas
// @Description used. A testing build's payload may hold private
// @Description transactions: before it is on chain it needs a login.
// @Produce json
// @Param blockHash path string true "Block hash"
// @Success 200 {object} PayloadTransactionsResponse
// @Failure 400 {object} map[string]string "Invalid block hash"
// @Failure 401 {object} map[string]string "Testing build payload not on chain yet, without a login"
// @Failure 404 {object} map[string]string "Unknown block"
// @Router /payloads/{blockHash}/transactions [get]
func (h *APIHandler) GetPayloadTransactions(w http.ResponseWriter, r *http.Request) {
	raw, err := hexutil.Decode(mux.Vars(r)["blockHash"])
	if err != nil || len(raw) != 32 {
		writeError(w, http.StatusBadRequest, "invalid block hash")
		return
	}

	hash := common.Hash(raw)

	payload := h.builderSvc.GetPayloadCache().GetByBlockHash(phase0.Hash32(raw))
	if payload != nil && payload.ExecutionPayload == nil {
		payload = nil
	}

	pool := h.builderSvc.TxPool()

	var block *execution.BlockTxList

	if pool != nil {
		if b, found, err := pool.IncludedBlock(r.Context(), hash); err == nil && found {
			block = b
		}
	}

	testing := payload != nil && payload.Build.Kind == payload_builder.BuildKindTesting

	switch {
	case payload == nil && block == nil:
		writeError(w, http.StatusNotFound, "block not known (recent payloads and blocks the EL has)")
		return
	case testing && block == nil && !h.isLoggedIn(r):
		writeError(w, http.StatusUnauthorized, "custom-built payloads may hold private transactions: log in to see them before they are on chain")
		return
	}

	resp := &PayloadTransactionsResponse{BlockHash: hash.Hex(), Ours: payload != nil, OnChain: block != nil}

	var (
		txs     []*types.Transaction
		senders []common.Address
		listed  map[common.Hash]struct{}
		private = make(map[common.Hash]struct{})
		baseFee = new(big.Int)
	)

	if payload != nil {
		exec := payload.ExecutionPayload
		resp.GasLimit, resp.GasUsed = exec.GasLimit, exec.GasUsed

		if exec.BaseFeePerGas != nil {
			baseFee = exec.BaseFeePerGas.ToBig()
		}

		if payload.BlockValue != nil {
			resp.Value = payload.BlockValue.String()
		}

		for _, encoded := range exec.Transactions {
			tx := new(types.Transaction)
			if err := tx.UnmarshalBinary(encoded); err != nil {
				continue
			}

			sender, _ := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
			txs, senders = append(txs, tx), append(senders, sender)
		}

		listed = payload.InclusionList.TransactionHashes()

		if payload.Local != nil && payload.Local.Selection != nil {
			for _, selected := range payload.Local.Selection.Hashes {
				private[common.HexToHash(selected)] = struct{}{}
			}
		}
	} else {
		resp.GasLimit, resp.GasUsed = block.GasLimit, block.GasUsed

		if block.BaseFee != nil {
			baseFee = block.BaseFee
		}

		for i, tx := range block.Txs {
			if tx != nil {
				txs, senders = append(txs, tx), append(senders, block.Senders[i])
			}
		}

		listed = h.headInclusionList(r.Context(), hash)

		for _, tx := range txs {
			if pool.Get(tx.Hash()) != nil {
				private[tx.Hash()] = struct{}{}
			}
		}
	}

	resp.BaseFee = baseFee.String()

	var gasUsed []uint64
	if block != nil {
		gasUsed, _ = pool.IncludedBlockGasUsed(r.Context(), hash)
	}

	value := new(big.Int)
	resp.Txs = make([]NextBlockTx, 0, len(txs))

	for i, tx := range txs {
		origin := "public"

		switch {
		case hasHash(listed, tx.Hash()):
			origin = "inclusion_list"
		case hasHash(private, tx.Hash()):
			origin = "private"
		}

		tip := txpool.EffectiveTip(tx, baseFee)
		entry := NextBlockTx{
			Hash:   tx.Hash().Hex(),
			From:   senders[i].Hex(),
			Nonce:  tx.Nonce(),
			Type:   tx.Type(),
			Origin: origin,
			Gas:    tx.Gas(),
			Tip:    tip.String(),
			Blobs:  len(tx.BlobHashes()),
		}

		if len(gasUsed) == len(txs) {
			entry.GasUsed = gasUsed[i]
			value.Add(value, new(big.Int).Mul(tip, new(big.Int).SetUint64(gasUsed[i])))
		}

		resp.Txs = append(resp.Txs, entry)
	}

	if resp.Value == "" && len(gasUsed) == len(txs) {
		resp.Value = value.String()
	}

	if testing && block == nil {
		w.Header().Set("Cache-Control", "private, no-store")
	}

	writeJSON(w, http.StatusOK, resp)
}

// headInclusionList is the inclusion list view the head block's payload had
// to satisfy, when the block is the head's (Heze on; nil otherwise).
func (h *APIHandler) headInclusionList(ctx context.Context, blockHash common.Hash) map[common.Hash]struct{} {
	store := h.builderSvc.InclusionLists()
	head := h.chainSvc.GetHeadTracker().CurrentHead()

	if store == nil || head == nil || head.Slot == 0 || common.Hash(head.ExecutionBlockHash) != blockHash ||
		h.chainSvc.ActiveForkAtEpoch(h.chainSvc.GetEpochOfSlot(head.Slot)) < version.DataVersionHeze {
		return nil
	}

	hashes := make(map[common.Hash]struct{})
	for _, tx := range store.View(ctx, head.Slot-1, head.ParentRoot).Transactions {
		hashes[crypto.Keccak256Hash(tx)] = struct{}{}
	}

	return hashes
}

func hasHash(set map[common.Hash]struct{}, hash common.Hash) bool {
	_, ok := set[hash]
	return ok
}
