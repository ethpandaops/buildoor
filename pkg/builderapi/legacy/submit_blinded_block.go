package legacy

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/ethpandaops/go-eth2-client/api"
	apiv1all "github.com/ethpandaops/go-eth2-client/api/v1/all"
	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// SubmitBlindedBlockV1Response is the JSON envelope returned by
// POST /eth/v1/builder/blinded_blocks: { "version": "<fork>", "data": ... }
// where data is the bare ExecutionPayload pre-Deneb and an
// ExecutionPayloadAndBlobsBundle from Deneb onwards.
type SubmitBlindedBlockV1Response struct {
	Version string `json:"version"`
	Data    any    `json:"data"`
}

// executionPayloadAndBlobsBundle is the Deneb+ v1 unblind response payload:
// the full execution payload plus the blobs bundle the proposer needs to
// publish the block itself.
type executionPayloadAndBlobsBundle struct {
	ExecutionPayload *eth2all.ExecutionPayload    `json:"execution_payload"`
	BlobsBundle      *payload_builder.BlobsBundle `json:"blobs_bundle"`
}

// HandleSubmitBlindedBlockV1 handles POST /eth/v1/builder/blinded_blocks
// (Bellatrix onwards). Unlike v2, the v1 flow returns the unblinded execution
// payload (plus blobs bundle from Deneb) in the response body so the proposer
// can publish the block itself; the block is additionally published by the
// builder, mirroring mev-boost-relay behavior.
func (h *Handler) HandleSubmitBlindedBlockV1(w http.ResponseWriter, r *http.Request) {
	h.handleSubmitBlindedBlock(w, r, 1)
}

// HandleSubmitBlindedBlock handles POST /eth/v2/builder/blinded_blocks.
// The v2 flow publishes the unblinded block and returns 202 Accepted with no
// body (the payload and blobs reach the network via the builder's publish).
func (h *Handler) HandleSubmitBlindedBlock(w http.ResponseWriter, r *http.Request) {
	h.handleSubmitBlindedBlock(w, r, 2)
}

// handleSubmitBlindedBlock implements both blinded-block submit endpoint
// versions. The blinded block is decoded fork-agnostically from either JSON or
// SSZ (application/octet-stream) per builder-specs; the wire version is taken
// from the Eth-Consensus-Version request header, falling back to the chain's
// current fork when the header is absent. After a successful unblind+publish,
// apiVersion selects the response shape: v1 returns 200 with the unblinded
// payload (and blobs bundle from Deneb), v2 returns 202 with no body.
// Returns 400 on validation/match failure, 415 on wrong Content-Type, and 503
// when the legacy Builder API dialect is disabled.
func (h *Handler) handleSubmitBlindedBlock(w http.ResponseWriter, r *http.Request, apiVersion int) {
	log := h.log.WithField("path", r.URL.Path)

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

	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (contentType != "application/json" && contentType != "application/octet-stream") {
		log.Warn("submitBlindedBlock: Content-Type must be application/json or application/octet-stream")
		writeError(w, http.StatusUnsupportedMediaType,
			"Content-Type must be application/json or application/octet-stream")
		return
	}

	if h.payloadCache == nil {
		log.Warn("submitBlindedBlock: builder service not available")
		writeError(w, http.StatusBadRequest, "builder not available")
		return
	}

	// The proposer declares the block's wire version in the request header;
	// without it, assume the chain's current fork.
	if hdr := r.Header.Get("Eth-Consensus-Version"); hdr != "" {
		parsed, err := parseConsensusVersion(hdr)
		if err != nil {
			log.WithError(err).Warn("submitBlindedBlock: invalid Eth-Consensus-Version header")
			writeError(w, http.StatusBadRequest, "invalid Eth-Consensus-Version header: "+err.Error())
			return
		}

		fork = parsed
	}

	blinded := apiv1all.SignedBlindedBeaconBlock{Version: fork}

	if contentType == "application/octet-stream" {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			log.WithError(err).Warn("submitBlindedBlock: failed to read SSZ body")
			writeError(w, http.StatusBadRequest, "failed to read body: "+err.Error())
			return
		}

		if err := blinded.UnmarshalSSZ(body); err != nil {
			log.WithError(err).Warn("submitBlindedBlock: invalid SSZ body")
			writeError(w, http.StatusBadRequest, "invalid SSZ: "+err.Error())
			return
		}
	} else if err := json.NewDecoder(r.Body).Decode(&blinded); err != nil {
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

	contents, err := UnblindSignedBlindedBeaconBlock(&blinded, event)
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

	if h.clClient == nil {
		log.Warn("submitBlindedBlock: no CL client available")
		writeError(w, http.StatusBadRequest, "no CL client available")
		return
	}

	// Deneb onwards the proposal wraps the block with its blobs
	// (SignedBlockContents); bellatrix/capella proposals are the bare signed
	// block.
	var proposal *api.VersionedSignedProposal

	if blinded.Version >= version.DataVersionDeneb {
		proposal, err = contents.ToVersioned()
	} else {
		proposal, err = apiv1all.ProposalFromSignedBlock(contents.SignedBlock)
	}

	if err != nil {
		log.WithError(err).Error("submitBlindedBlock: failed to convert unblinded contents to proposal")
		writeError(w, http.StatusInternalServerError, "failed to convert block contents: "+err.Error())
		return
	}

	if err := h.clClient.SubmitProposal(r.Context(), &api.SubmitProposalOpts{Proposal: proposal}); err != nil {
		log.WithError(err).Error("submitBlindedBlock: failed to publish unblinded block")
		writeError(w, http.StatusInternalServerError, "failed to publish block: "+err.Error())
		return
	}

	log.Info("SubmitBlindedBlock: Successfully published block!")

	h.blocksPublished.Add(1)

	log.Infof("submitBlindedBlock: submitted unblinded block for slot %d, block hash %s", slot, blockHashHex)

	// Won-block tracking is NOT done here: the shared
	// payload_bidder.InclusionTracker records the win when the block is
	// actually seen at the head.
	if h.events != nil {
		h.events.BroadcastBuilderAPISubmitBlindedDelivered(uint64(slot), blockHashHex)
	}

	if apiVersion == 1 {
		h.writeUnblindedPayloadResponse(w, log, blinded.Version, event)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// writeUnblindedPayloadResponse writes the v1 submitBlindedBlock 200 response:
// the bare execution payload pre-Deneb, or the payload plus blobs bundle from
// Deneb onwards (an empty bundle when the block carries no blobs).
func (h *Handler) writeUnblindedPayloadResponse(
	w http.ResponseWriter,
	log logrus.FieldLogger,
	fork version.DataVersion,
	event *payload_builder.Payload,
) {
	var data any = event.ExecutionPayload

	if fork >= version.DataVersionDeneb {
		bundle := event.BlobsBundle
		if bundle == nil {
			bundle = &payload_builder.BlobsBundle{
				Commitments: []deneb.KZGCommitment{},
				Proofs:      []deneb.KZGProof{},
				Blobs:       []deneb.Blob{},
			}
		}

		data = &executionPayloadAndBlobsBundle{
			ExecutionPayload: event.ExecutionPayload,
			BlobsBundle:      bundle,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Eth-Consensus-Version", fork.String())
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(SubmitBlindedBlockV1Response{
		Version: fork.String(),
		Data:    data,
	}); err != nil {
		log.WithError(err).Warn("submitBlindedBlock: failed to encode v1 unblind response")
	}
}
