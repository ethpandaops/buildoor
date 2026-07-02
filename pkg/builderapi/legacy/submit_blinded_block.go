package legacy

import (
	"encoding/hex"
	"encoding/json"
	"net/http"

	apiv1electra "github.com/ethpandaops/go-eth2-client/api/v1/electra"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
)

// HandleSubmitBlindedBlock handles POST /eth/v2/builder/blinded_blocks
// (Electra-shaped SignedBlindedBeaconBlock, serving Electra and Fulu).
// Returns 202 Accepted on success, 400 on validation/match failure, 415 on wrong
// Content-Type, and 503 when the legacy Builder API dialect is disabled.
func (h *Handler) HandleSubmitBlindedBlock(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithField("path", "/eth/v2/builder/blinded_blocks")

	if !h.enabled.Load() {
		log.Warn("submitBlindedBlock: 503 — builder API disabled")
		writeError(w, http.StatusServiceUnavailable, "builder not ready")
		return
	}

	// The legacy dialect ends at Gloas: post-Gloas blocks carry the payload bid
	// inside the beacon block and are submitted via the beacon_block endpoint.
	fork := h.chainSvc.GetCurrentFork()
	if fork >= version.DataVersionGloas {
		log.WithField("fork", fork.String()).Warn(
			"submitBlindedBlock: 400 — legacy Builder API dialect not served post-Gloas")
		writeError(w, http.StatusBadRequest, "legacy builder API dialect not available post-Gloas")
		return
	}

	if r.Header.Get("Content-Type") != "application/json" {
		log.Warn("submitBlindedBlock: Content-Type must be application/json")
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}

	if h.payloadCache == nil {
		log.Warn("submitBlindedBlock: builder service not available")
		writeError(w, http.StatusBadRequest, "builder not available")
		return
	}

	var blinded apiv1electra.SignedBlindedBeaconBlock
	if err := json.NewDecoder(r.Body).Decode(&blinded); err != nil {
		log.WithError(err).Warn("submitBlindedBlock: invalid JSON body")
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if blinded.Message == nil || blinded.Message.Body == nil ||
		blinded.Message.Body.ExecutionPayloadHeader == nil {
		log.Warn("submitBlindedBlock: blinded block missing message or execution_payload_header")
		writeError(w, http.StatusBadRequest, "invalid blinded block: missing message or execution_payload_header")
		return
	}

	blockHash := blinded.Message.Body.ExecutionPayloadHeader.BlockHash
	slot := blinded.Message.Slot
	blockHashHex := "0x" + hex.EncodeToString(blockHash[:])
	log = log.WithFields(logrus.Fields{
		"slot":       slot,
		"block_hash": blockHashHex,
	})
	log.Debug("submitBlindedBlock request received")

	// Broadcast submitBlindedBlock received event
	if h.events != nil {
		h.events.BroadcastBuilderAPISubmitBlindedReceived(uint64(slot), blockHashHex)
	}

	event := h.payloadCache.GetByBlockHash(blockHash)
	if event == nil {
		log.Info("submitBlindedBlock: no cached payload for block hash (payload may not have been built or already evicted)")
		writeError(w, http.StatusBadRequest, "no matching payload for block hash")
		return
	}

	contents, err := UnblindSignedBlindedBeaconBlock(&blinded, event, fork)
	if err != nil {
		log.WithError(err).Warn("submitBlindedBlock: unblind failed")
		writeError(w, http.StatusBadRequest, "unblind failed: "+err.Error())
		return
	}
	if contents == nil {
		log.Warn("submitBlindedBlock: unblind produced no contents")
		writeError(w, http.StatusBadRequest, "unblind produced no contents")
		return
	}

	log.Infof("submitBlindedBlock: unblinded block for slot %d", slot)

	if h.publisher != nil {
		if err := h.publisher.SubmitLegacyBlock(r.Context(), contents); err != nil {
			log.WithError(err).Error("submitBlindedBlock: failed to publish unblinded block")
			writeError(w, http.StatusInternalServerError, "failed to publish block: "+err.Error())
			return
		}

		log.Info("SubmitBlindedBlock: Successfully published block!")
	} else {
		log.Warn("submitBlindedBlock: no publisher available")
		writeError(w, http.StatusBadRequest, "no publisher available")
		return
	}

	h.blocksPublished.Add(1)

	log.Infof("submitBlindedBlock: submitted unblinded block for slot %d, block hash %s", slot, blockHashHex)

	// Won-block tracking is NOT done here: the shared
	// payload_bidder.InclusionTracker records the win when the block is
	// actually seen at the head.
	if h.events != nil {
		h.events.BroadcastBuilderAPISubmitBlindedDelivered(uint64(slot), blockHashHex)
	}

	w.WriteHeader(http.StatusAccepted)
}
