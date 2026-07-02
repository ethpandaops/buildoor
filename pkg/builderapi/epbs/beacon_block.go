package epbs

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"github.com/ethpandaops/go-eth2-client/api"
	apiv1all "github.com/ethpandaops/go-eth2-client/api/v1/all"
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	dynssz "github.com/pk910/dynamic-ssz"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// HandleSubmitBeaconBlock handles POST /eth/v1/builder/beacon_block.
//
// The proposer submits a full post-Gloas SignedBeaconBlock that binds them to
// the builder's bid. The block is decoded fork-agnostically; the wire version
// is taken from the Eth-Consensus-Version request header, falling back to the
// chain's current fork when the header is absent. If the builder still holds
// the payload referenced by the bid's block_hash, it broadcasts the beacon
// block immediately (block publication is time-critical) and schedules the
// execution payload envelope reveal with the shared RevealService, which
// publishes it at the configured reveal time (deduped per slot with the p2p
// flow).
//
// Returns 202 on success, 400 on a malformed block or missing payload,
// 415 on wrong Content-Type, 500 on broadcast/internal errors, and 503 if the
// dialect is disabled, not fully configured, or the chain has not activated
// Gloas yet.
func (h *Handler) HandleSubmitBeaconBlock(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithField("path", "/eth/v1/builder/beacon_block")

	if !h.enabled.Load() || h.payloadCache == nil {
		log.Warn("submitBeaconBlock: 503 — builder not fully configured (disabled or payload cache missing)")
		writeError(w, http.StatusServiceUnavailable, "builder not ready")
		return
	}

	if h.broadcaster == nil || h.revealSvc == nil {
		log.Warn("submitBeaconBlock: 503 — block broadcaster or reveal service not configured")
		writeError(w, http.StatusServiceUnavailable, "builder not ready")
		return
	}

	if r.Header.Get("Content-Type") != "application/json" {
		log.WithField("content_type", r.Header.Get("Content-Type")).Warn("submitBeaconBlock: rejected — Content-Type must be application/json")
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}

	fork := h.chainSvc.GetCurrentFork()
	if fork < version.DataVersionGloas {
		log.WithField("fork", fork.String()).Warn(
			"submitBeaconBlock: 503 — post-Gloas Builder API dialect not available pre-Gloas")
		writeError(w, http.StatusServiceUnavailable, "post-Gloas builder API dialect not available pre-Gloas")
		return
	}

	// The proposer declares the block's wire version in the request header;
	// without it, assume the chain's current fork.
	if hdr := r.Header.Get("Eth-Consensus-Version"); hdr != "" {
		parsed, err := parseConsensusVersion(hdr)
		if err != nil {
			log.WithError(err).Warn("submitBeaconBlock: invalid Eth-Consensus-Version header")
			writeError(w, http.StatusBadRequest, "invalid Eth-Consensus-Version header: "+err.Error())
			return
		}

		fork = parsed
	}

	if fork < version.DataVersionGloas {
		log.WithField("fork", fork.String()).Warn(
			"submitBeaconBlock: 400 — beacon_block submissions require Gloas or later")
		writeError(w, http.StatusBadRequest, "beacon_block submissions require Gloas or later")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.WithError(err).Warn("submitBeaconBlock: failed to read body")
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	block := eth2all.SignedBeaconBlock{Version: fork}
	if err := json.Unmarshal(body, &block); err != nil {
		log.WithError(err).Warn("submitBeaconBlock: invalid JSON body")
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if block.Message == nil || block.Message.Body == nil ||
		block.Message.Body.SignedExecutionPayloadBid == nil ||
		block.Message.Body.SignedExecutionPayloadBid.Message == nil {
		log.Warn("submitBeaconBlock: missing signed_execution_payload_bid in block body")
		writeError(w, http.StatusBadRequest, "missing signed_execution_payload_bid in block body")
		return
	}

	bid := block.Message.Body.SignedExecutionPayloadBid.Message
	blockHashHex := "0x" + hex.EncodeToString(bid.BlockHash[:])
	log = log.WithFields(logrus.Fields{
		"slot":       bid.Slot,
		"block_hash": blockHashHex,
	})
	log.Debug("submitBeaconBlock request received")

	if h.events != nil {
		h.events.BroadcastBuilderAPISubmitBlockReceived(uint64(bid.Slot), blockHashHex)
	}

	event := h.payloadCache.GetByBlockHash(bid.BlockHash)
	if event == nil {
		log.Info("submitBeaconBlock: 400 — no cached payload for bid block hash")
		writeError(w, http.StatusBadRequest, "no cached payload for bid block hash")
		return
	}

	beaconBlockRoot, err := dynssz.GetGlobalDynSsz().HashTreeRoot(block.Message)
	if err != nil {
		log.WithError(err).Warn("submitBeaconBlock: failed to compute beacon block hash tree root")
		writeError(w, http.StatusInternalServerError, "failed to compute beacon block root")
		return
	}
	var blockRoot phase0.Root
	copy(blockRoot[:], beaconBlockRoot[:])

	// Post-Gloas blocks are submitted bare (no block-contents wrapper — blobs
	// travel with the envelope reveal later).
	proposal, err := apiv1all.ProposalFromSignedBlock(&block)
	if err != nil {
		log.WithError(err).Warn("submitBeaconBlock: failed to build proposal from beacon block")
		writeError(w, http.StatusInternalServerError, "failed to build proposal: "+err.Error())
		return
	}

	// Broadcast the proposer's block immediately — the proposer delegated
	// time-critical block publication to us.
	if err := h.broadcaster.SubmitProposal(r.Context(), &api.SubmitProposalOpts{
		Proposal: proposal,
	}); err != nil {
		log.WithError(err).Error("submitBeaconBlock: failed to broadcast beacon block")
		writeError(w, http.StatusInternalServerError, "failed to broadcast beacon block: "+err.Error())
		return
	}
	log.Info("submitBeaconBlock: broadcasted beacon block")

	// Schedule the envelope reveal at the configured reveal time. The shared
	// RevealService owns envelope building, publishing, MarkRevealed bookkeeping,
	// and per-slot dedup with the p2p flow — nothing is published here.
	h.revealSvc.RequestReveal(&payload_bidder.RevealRequest{
		Payload: event,
		BlockInfo: &beacon.BlockInfo{
			Slot:               bid.Slot,
			Root:               blockRoot,
			ParentRoot:         block.Message.ParentRoot,
			ExecutionBlockHash: bid.BlockHash,
		},
		Transport: payload_builder.BidTransportBuilderAPI,
	})

	if h.events != nil {
		h.events.BroadcastBuilderAPISubmitBlockDelivered(uint64(bid.Slot), blockHashHex)
	}

	h.blocksAccepted.Add(1)

	log.WithField("beacon_block_root", "0x"+hex.EncodeToString(blockRoot[:])).Info(
		"submitBeaconBlock: accepted beacon block, reveal scheduled")

	w.WriteHeader(http.StatusAccepted)
}
