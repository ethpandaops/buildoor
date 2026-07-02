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

	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// HandleGetHeader handles GET /eth/v1/builder/header/{slot}/{parent_hash}/{pubkey}.
// Returns 200 with Fulu SignedBuilderBid, or 204 if no bid, or 400 on invalid params / unregistered proposer.
func (h *Handler) HandleGetHeader(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithField("path", "/eth/v1/builder/header/...")

	if h.payloadCache == nil || h.blsSigner == nil || !h.enabled.Load() {
		log.Warn("getHeader: returning 204 — payload cache or BLS signer not available or service disabled")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// The legacy dialect ends at Gloas: post-Gloas proposers must use the
	// execution_payload_bid flow instead.
	if fork := h.chainSvc.GetCurrentFork(); fork >= version.DataVersionGloas {
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
		w.WriteHeader(http.StatusNoContent)
		return
	}

	event := h.payloadCache.Get(phase0.Slot(slotU64))
	if event == nil {
		log.WithField("slot", slotU64).Info(
			"getHeader: returning 204 — no cached payload for slot")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if event.Attributes.ParentBlockHash != parentHash {
		log.WithFields(logrus.Fields{
			"slot":                slotU64,
			"request_parent_hash": "0x" + hex.EncodeToString(parentHash[:]),
			"cached_parent_hash":  "0x" + hex.EncodeToString(event.Attributes.ParentBlockHash[:]),
		}).Info("getHeader: returning 204 — cached payload parent hash does not match request")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	subsidyGwei := uint64(0)
	if h.cfg != nil {
		subsidyGwei = h.cfg.BlockValueSubsidyGwei
	}
	log.Info("Subsidy Gwei: " + fmt.Sprintf("%d", subsidyGwei))
	maxWithdrawalsPerPayload := uint64(0)
	if chainSpec := h.chainSvc.GetChainSpec(); chainSpec != nil {
		maxWithdrawalsPerPayload = chainSpec.MaxWithdrawalsPerPayload
	}
	signedBid, err := BuildSignedBuilderBid(event, h.blsSigner.PublicKey(), h.blsSigner, subsidyGwei, h.chainSvc.GetGenesis().GenesisForkVersion, maxWithdrawalsPerPayload)
	if err != nil {
		log.WithError(err).Warn("getHeader: failed to build SignedBuilderBid")
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

	resp := GetHeaderResponse{
		Version: "fulu",
		Data:    signedBid,
	}

	log.WithFields(logrus.Fields{
		"slot":        slotU64,
		"block_hash":  "0x" + hex.EncodeToString(event.BlockHash[:]),
		"parent_hash": "0x" + hex.EncodeToString(parentHash[:]),
		"value":       signedBid.Message.Value.String(),
		"gas_limit":   signedBid.Message.Header.GasLimit,
	}).Infof("getHeader: delivered header for slot %d", slotU64)

	// Broadcast getHeader delivered event
	if h.events != nil {
		blockHashHex := "0x" + hex.EncodeToString(event.BlockHash[:])
		h.events.BroadcastBuilderAPIGetHeaderDelivered(slotU64, blockHashHex, signedBid.Message.Value.String())
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Eth-Consensus-Version", "fulu")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
