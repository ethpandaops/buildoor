package legacy

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"

	legacytypes "github.com/ethpandaops/buildoor/pkg/builderapi/legacy/types"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// GetHeaderResponse is the JSON response for getHeader:
// { "version": "<fork>", "data": SignedBuilderBid }.
type GetHeaderResponse struct {
	Version string                        `json:"version"`
	Data    *legacytypes.SignedBuilderBid `json:"data"`
}

// HandleGetHeader handles GET /eth/v1/builder/header/{slot}/{parent_hash}/{pubkey}.
// Returns 200 with the active fork's SignedBuilderBid, or 204 if no bid, or 400 on
// invalid params / unregistered proposer.
//
// Whether a bid is served for the slot is decided exclusively by the frozen
// per-slot action plan: a plan may force-serve a slot although the dialect is
// globally disabled, or suppress an enabled one. All other availability gates
// (fork, registration, payload cache, parent hash) stay authoritative.
func (h *Handler) HandleGetHeader(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithField("path", "/eth/v1/builder/header/...")

	if h.payloadCache == nil || h.blsSigner == nil {
		log.Warn("getHeader: returning 204 — payload cache or BLS signer not available")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// The legacy dialect ends at Gloas: post-Gloas proposers must use the
	// execution_payload_bid flow instead.
	fork := h.chainSvc.GetCurrentFork()
	if fork >= version.DataVersionGloas {
		log.WithField("fork", fork.String()).Info(
			"getHeader: returning 204 — legacy Builder API dialect not served post-Gloas")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	vars := mux.Vars(r)
	slotStr := vars["slot"]
	parentHashStr := vars["parent_hash"]
	pubkeyStr := vars["pubkey"]

	log = log.WithFields(logrus.Fields{
		"slot":        slotStr,
		"parent_hash": parentHashStr,
		"pubkey":      pubkeyStr,
	})
	log.Debug("getHeader request received")

	slotU64, err := strconv.ParseUint(slotStr, 10, 64)
	if err != nil {
		log.WithError(err).Warn("getHeader: invalid slot")
		writeError(w, http.StatusBadRequest, "invalid slot: must be a number")
		return
	}

	slot := phase0.Slot(slotU64)

	// Bound the request horizon BEFORE freezing the slot's plan: proposers
	// only ever request the current or next slot, and freezing arbitrary
	// far-future slots would permanently lock their plans against edits.
	if currentSlot := h.chainSvc.GetCurrentSlot(); slot > currentSlot+1 {
		log.WithField("current_slot", currentSlot).Warn("getHeader: slot too far ahead")
		writeError(w, http.StatusBadRequest, "slot too far ahead: max current slot + 1")

		return
	}

	// Effective enable: freeze the slot's action plan (idempotent) and act on
	// the snapshot — the plan overrides the global enable flag in both
	// directions for this slot.
	frozenSettings, serve := h.frozenBuilderAPISettings(slot)
	if !serve {
		log.Info("getHeader: returning 204 — bid serving suppressed (plan or global disable)")
		h.recordBid(slot, fork.String(), "", nil, 0, bidStatusSuppressed, "")
		w.WriteHeader(http.StatusNoContent)

		return
	}

	h.headersRequested.Add(1)

	// Broadcast getHeader received event
	if h.events != nil {
		h.events.BroadcastBuilderAPIGetHeaderReceived(slotU64, parentHashStr, pubkeyStr)
	}

	parentHashBytes, err := hex.DecodeString(trimHex(parentHashStr))
	if err != nil || len(parentHashBytes) != 32 {
		log.WithError(err).Warn("getHeader: invalid parent_hash")
		writeError(w, http.StatusBadRequest, "invalid parent_hash: must be 32 bytes hex")
		return
	}
	var parentHash phase0.Hash32
	copy(parentHash[:], parentHashBytes)

	pubkeyBytes, err := hex.DecodeString(trimHex(pubkeyStr))
	if err != nil || len(pubkeyBytes) != 48 {
		log.WithError(err).Warn("getHeader: invalid pubkey")
		writeError(w, http.StatusBadRequest, "invalid pubkey: must be 48 bytes hex")
		return
	}
	var pubkey phase0.BLSPubKey
	copy(pubkey[:], pubkeyBytes)

	if _, registered := h.validatorsStore.Get(pubkey); !registered {
		log.WithField("pubkey_hex", "0x"+hex.EncodeToString(pubkey[:])).Info(
			"getHeader: returning 204 — proposer not in validator store (no registration for this pubkey)")
		h.recordBid(slot, fork.String(), "", nil, 0, bidStatusFailed,
			"proposer not registered (no validator registration for pubkey)")
		w.WriteHeader(http.StatusNoContent)

		return
	}

	event := h.payloadCache.Get(slot)
	if event == nil {
		log.WithField("slot", slotU64).Info(
			"getHeader: returning 204 — no cached payload for slot")
		h.recordBid(slot, fork.String(), "", nil, 0, bidStatusFailed, "no cached payload for slot")
		w.WriteHeader(http.StatusNoContent)

		return
	}
	if event.Attributes.ParentBlockHash != parentHash {
		log.WithFields(logrus.Fields{
			"slot":                slotU64,
			"request_parent_hash": "0x" + hex.EncodeToString(parentHash[:]),
			"cached_parent_hash":  "0x" + hex.EncodeToString(event.Attributes.ParentBlockHash[:]),
		}).Info("getHeader: returning 204 — cached payload parent hash does not match request")
		h.recordBid(slot, fork.String(), "", nil, 0, bidStatusFailed,
			"cached payload parent hash does not match request")
		w.WriteHeader(http.StatusNoContent)

		return
	}

	// Value resolution per the frozen settings: an absolute total value (when
	// set) replaces blockValue+subsidy entirely; otherwise the (possibly
	// per-slot overridden) subsidy is added to the block value.
	subsidyGwei := frozenSettings.SubsidyGwei
	totalValueGwei := frozenSettings.TotalValueGwei

	log.Info("Subsidy Gwei: " + fmt.Sprintf("%d", subsidyGwei))
	maxWithdrawalsPerPayload := uint64(0)
	if chainSpec := h.chainSvc.GetChainSpec(); chainSpec != nil {
		maxWithdrawalsPerPayload = chainSpec.MaxWithdrawalsPerPayload
	}
	signedBid, err := BuildSignedBuilderBid(event, fork, h.blsSigner.PublicKey(), h.blsSigner,
		subsidyGwei, totalValueGwei, h.chainSvc.GetGenesis().GenesisForkVersion, maxWithdrawalsPerPayload)
	if err != nil {
		log.WithError(err).Warn("getHeader: failed to build SignedBuilderBid")
		h.recordBid(slot, fork.String(), "", nil, 0, bidStatusFailed,
			"failed to build SignedBuilderBid: "+err.Error())
		writeError(w, http.StatusInternalServerError, "failed to build bid")

		return
	}

	// Record the delivered bid on the payload's activity log; the inclusion
	// tracker derives the won-block source (builder-api vs p2p) from it.
	bidValueGwei := new(big.Int).Div(signedBid.Message.Value.ToBig(), big.NewInt(1e9)).Uint64()
	event.AddBid(payload_builder.BidRecord{
		Transport: payload_builder.BidTransportBuilderAPI,
		Value:     phase0.Gwei(bidValueGwei),
		At:        time.Now(),
	})

	log.WithFields(logrus.Fields{
		"slot":        slotU64,
		"block_hash":  "0x" + hex.EncodeToString(event.BlockHash[:]),
		"parent_hash": "0x" + hex.EncodeToString(parentHash[:]),
		"value":       signedBid.Message.Value.String(),
		"gas_limit":   signedBid.Message.Header.GasLimit,
	}).Infof("getHeader: delivered header for slot %d", slotU64)

	blockHashHex := "0x" + hex.EncodeToString(event.BlockHash[:])

	// Broadcast getHeader delivered event
	if h.events != nil {
		h.events.BroadcastBuilderAPIGetHeaderDelivered(slotU64, blockHashHex, signedBid.Message.Value.String())
	}

	// Planned response delay: wait context-aware immediately before writing
	// the bid response; a proposer hangup during the wait cancels the serve.
	if frozenSettings.DelayMs > 0 {
		select {
		case <-time.After(time.Duration(frozenSettings.DelayMs) * time.Millisecond):
		case <-r.Context().Done():
			log.WithField("delay_ms", frozenSettings.DelayMs).Info(
				"getHeader: request cancelled during planned response delay")
			h.recordBid(slot, fork.String(), blockHashHex, signedBid, bidValueGwei,
				bidStatusCancelled, "request context cancelled during planned response delay")

			return
		}
	}

	w.Header().Set("Eth-Consensus-Version", fork.String())

	// Per builder-specs the response may be SSZ; the proposer opts in via
	// the Accept header.
	if preferSSZ(r.Header.Get("Accept")) {
		body, err := signedBid.MarshalSSZ()
		if err != nil {
			log.WithError(err).Warn("getHeader: failed to SSZ-encode SignedBuilderBid")
			h.recordBid(slot, fork.String(), blockHashHex, signedBid, bidValueGwei,
				bidStatusFailed, "failed to SSZ-encode SignedBuilderBid: "+err.Error())
			writeError(w, http.StatusInternalServerError, "failed to encode bid")

			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)

		if _, err := w.Write(body); err != nil {
			log.WithError(err).Warn("getHeader: failed to write SSZ response")
			h.recordBid(slot, fork.String(), blockHashHex, signedBid, bidValueGwei,
				bidStatusFailed, "failed to write SSZ response: "+err.Error())

			return
		}

		h.recordBid(slot, fork.String(), blockHashHex, signedBid, bidValueGwei, bidStatusServed, "")

		return
	}

	resp := GetHeaderResponse{
		Version: fork.String(),
		Data:    signedBid,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.WithError(err).Warn("getHeader: failed to write JSON response")
		h.recordBid(slot, fork.String(), blockHashHex, signedBid, bidValueGwei,
			bidStatusFailed, "failed to write JSON response: "+err.Error())

		return
	}

	h.recordBid(slot, fork.String(), blockHashHex, signedBid, bidValueGwei, bidStatusServed, "")
}
