package epbs

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"time"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// GetExecutionPayloadBidResponse is the JSON envelope returned by
// POST /eth/v1/builder/execution_payload_bid/{slot}/{parent_hash}/{parent_root}/{proposer_pubkey}.
type GetExecutionPayloadBidResponse struct {
	Version string                             `json:"version"`
	Data    *eth2all.SignedExecutionPayloadBid `json:"data"`
}

// HandleGetExecutionPayloadBid handles
// POST /eth/v1/builder/execution_payload_bid/{slot}/{parent_hash}/{parent_root}/{proposer_pubkey}.
//
// Looks up the cached payload for the requested slot, validates the supplied
// parent_hash and parent_root against it, then constructs and signs a Gloas
// SignedExecutionPayloadBid using the proposer's fee recipient from the
// ProposerPreferences cache. Returns 204 if no payload is cached, 400 if the
// inputs do not match the cached payload or proposer preferences are missing,
// and 503 while the chain has not activated Gloas yet.
//
// If the request body contains a SignedRequestAuthV1, it is validated:
//   - auth.message.slot must match the requested slot
//   - auth.message.builder_url must match cfg.BuilderURL (if configured)
//   - BLS signature must verify against the proposer_pubkey path parameter
func (h *Handler) HandleGetExecutionPayloadBid(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithField("path", "/eth/v1/builder/execution_payload_bid/...")

	if !h.enabled.Load() || h.payloadCache == nil || h.blsSigner == nil {
		log.Warn("getExecutionPayloadBid: returning 204 — builder service disabled or signer/payload cache unavailable")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if !chain.IsBuilderActive(h.chainSvc.GetBuilderByPubkey(h.blsSigner.PublicKey()), uint64(h.chainSvc.GetFinalizedEpoch())) {
		log.Warn("getExecutionPayloadBid: returning 204 — builder not active on chain")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if h.propPrefsStore == nil {
		log.Warn("getExecutionPayloadBid: proposer preferences store not configured")
		writeError(w, http.StatusInternalServerError, "proposer preferences store not configured")
		return
	}

	vars := mux.Vars(r)
	slotStr := vars["slot"]
	parentHashStr := vars["parent_hash"]
	parentRootStr := vars["parent_root"]
	proposerPubkeyStr := vars["proposer_pubkey"]

	log = log.WithFields(logrus.Fields{
		"slot":            slotStr,
		"parent_hash":     parentHashStr,
		"parent_root":     parentRootStr,
		"proposer_pubkey": proposerPubkeyStr,
	})
	log.Debug("getExecutionPayloadBid request received")

	pubkeyBytes, hexErr := hex.DecodeString(trimHex(proposerPubkeyStr))
	if hexErr != nil || len(pubkeyBytes) != 48 {
		log.WithError(hexErr).Warn("getExecutionPayloadBid: invalid proposer_pubkey for auth verification")
		writeError(w, http.StatusBadRequest, "invalid proposer_pubkey: must be 48 bytes hex")
		return
	}
	var proposerPubkey phase0.BLSPubKey
	copy(proposerPubkey[:], pubkeyBytes)

	slotU64, err := strconv.ParseUint(slotStr, 10, 64)
	if err != nil {
		log.WithError(err).Warn("getExecutionPayloadBid: invalid slot")
		writeError(w, http.StatusBadRequest, "invalid slot: must be a number")
		return
	}
	slot := phase0.Slot(slotU64)

	h.bidsRequested.Add(1)

	if h.events != nil {
		h.events.BroadcastBuilderAPIGetBidReceived(slotU64, parentHashStr, proposerPubkeyStr)
	}

	// The post-Gloas dialect only exists once the chain has activated Gloas;
	// before that, reject cleanly instead of leaking an internal error from the
	// fork-version lookup below.
	if fork := h.chainSvc.GetCurrentFork(); fork < version.DataVersionGloas {
		log.WithField("fork", fork.String()).Warn(
			"getExecutionPayloadBid: 503 — post-Gloas Builder API dialect not available pre-Gloas")
		writeError(w, http.StatusServiceUnavailable, "post-Gloas builder API dialect not available pre-Gloas")
		return
	}

	// Resolve the Gloas fork version once for bid signing (DomainBeaconBuilder is
	// chain-fork bound). Request auth is NOT signed with this: per the Gloas
	// builder-specs, RequestAuth is signed with compute_domain(DOMAIN_REQUEST_AUTH)
	// using the genesis fork version and a zero genesis_validators_root — an
	// application-space domain that mirrors DomainApplicationBuilder. So auth is
	// verified below with the genesis fork version, not gloasForkVersion.

	gloasForkVersion, err := h.chainSvc.GetChainSpec().GetForkVersion(version.DataVersionGloas)
	if err != nil {
		log.WithError(err).Warn("getExecutionPayloadBid: failed to get Gloas fork version")
		writeError(w, http.StatusInternalServerError, "failed to get Gloas fork version")
		return
	}

	// Parse and validate SignedRequestAuth from the request body.
	// Auth is always verified when present; h.cfg.RequireRequestAuth controls whether
	// absence is an error.
	var authBody []byte
	if r.ContentLength > 0 {
		var readErr error
		authBody, readErr = io.ReadAll(r.Body)
		if readErr != nil {
			log.WithError(readErr).Warn("getExecutionPayloadBid: failed to read request body")
			writeError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
	}
	if len(authBody) > 0 {
		signedAuth, parseErr := parseSignedRequestAuth(authBody, r.Header.Get("Content-Type"))
		if parseErr != nil {
			code := http.StatusBadRequest
			if errors.Is(parseErr, errUnsupportedContentType) {
				code = http.StatusUnsupportedMediaType
			}
			log.WithError(parseErr).Warn("getExecutionPayloadBid: invalid SignedRequestAuth body")
			writeError(w, code, "invalid SignedRequestAuthV1: "+parseErr.Error())
			return
		}
		if signedAuth.Message == nil {
			log.Warn("getExecutionPayloadBid: SignedRequestAuth missing message")
			writeError(w, http.StatusBadRequest, "invalid SignedRequestAuthV1: message is null")
			return
		}
		if phase0.Slot(signedAuth.Message.Slot) != slot {
			log.WithFields(logrus.Fields{
				"auth_slot":    signedAuth.Message.Slot,
				"request_slot": slot,
			}).Warn("getExecutionPayloadBid: SignedRequestAuth slot mismatch")
			writeError(w, http.StatusBadRequest, "invalid SignedRequestAuthV1: auth.message.slot does not match the requested slot")
			return
		}
		if h.cfg.BuilderURL != "" && string(signedAuth.Message.Data) != h.cfg.BuilderURL {
			log.WithFields(logrus.Fields{
				"auth_url":    string(signedAuth.Message.Data),
				"builder_url": h.cfg.BuilderURL,
			}).Warn("getExecutionPayloadBid: SignedRequestAuth data (builder_url) mismatch")
			writeError(w, http.StatusBadRequest, "invalid SignedRequestAuthV1: auth.message.data does not match this builder's URL")
			return
		}

		if authErr := VerifyRequestAuth(signedAuth, proposerPubkey, h.chainSvc.GetGenesis().GenesisForkVersion); authErr != nil {
			log.WithError(authErr).Warn("getExecutionPayloadBid: SignedRequestAuth signature verification failed")
			writeError(w, http.StatusUnauthorized, "invalid SignedRequestAuthV1: signature verification failed")
			return
		}
		log.Info("getExecutionPayloadBid: SignedRequestAuth verified")
	} else if h.cfg.RequireRequestAuth {
		log.Warn("getExecutionPayloadBid: missing required SignedRequestAuth")
		writeError(w, http.StatusUnauthorized, "missing SignedRequestAuthV1: this builder requires authenticated requests")
		return
	}

	parentHashBytes, err := hex.DecodeString(trimHex(parentHashStr))
	if err != nil || len(parentHashBytes) != 32 {
		log.WithError(err).Warn("getExecutionPayloadBid: invalid parent_hash")
		writeError(w, http.StatusBadRequest, "invalid parent_hash: must be 32 bytes hex")
		return
	}
	var parentHash phase0.Hash32
	copy(parentHash[:], parentHashBytes)

	parentRootBytes, err := hex.DecodeString(trimHex(parentRootStr))
	if err != nil || len(parentRootBytes) != 32 {
		log.WithError(err).Warn("getExecutionPayloadBid: invalid parent_root")
		writeError(w, http.StatusBadRequest, "invalid parent_root: must be 32 bytes hex")
		return
	}
	var parentRoot phase0.Root
	copy(parentRoot[:], parentRootBytes)

	event := h.payloadCache.Get(slot)
	if event == nil {
		log.Info("getExecutionPayloadBid: returning 204 — no cached payload for slot")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if event.Attributes.ParentBlockHash != parentHash {
		log.WithFields(logrus.Fields{
			"request_parent_hash": "0x" + hex.EncodeToString(parentHash[:]),
			"cached_parent_hash":  "0x" + hex.EncodeToString(event.Attributes.ParentBlockHash[:]),
		}).Info("getExecutionPayloadBid: 400 — parent_hash does not match cached payload")
		writeError(w, http.StatusBadRequest, "parent_hash does not match cached payload")
		return
	}

	if event.Attributes.ParentBlockRoot != parentRoot {
		log.WithFields(logrus.Fields{
			"request_parent_root": "0x" + hex.EncodeToString(parentRoot[:]),
			"cached_parent_root":  "0x" + hex.EncodeToString(event.Attributes.ParentBlockRoot[:]),
		}).Info("getExecutionPayloadBid: 400 — parent_root does not match cached payload")
		writeError(w, http.StatusBadRequest, "parent_root does not match cached payload")
		return
	}

	signedPrefs, ok := h.propPrefsStore.Get(slot)
	if !ok || signedPrefs == nil || signedPrefs.Message == nil {
		log.Info("getExecutionPayloadBid: 400 — no proposer preferences cached for slot")
		writeError(w, http.StatusBadRequest, "no proposer preferences cached for slot")
		return
	}
	prefs := signedPrefs.Message

	// Split the post-subsidy block value between the execution-layer payment and the
	// trustless on-chain payment (Value). max_execution_payment caps how much the
	// proposer accepts directly from the builder as an execution payment; it defaults
	// to 0 when the proposer never submitted preferences, per the Gloas spec (no
	// execution payment allowed in that case). Anything above the cap is paid
	// trustlessly on-chain via Value.
	blockValueGwei := new(big.Int).Div(event.BlockValue, big.NewInt(1e9)).Uint64()
	valueAfterSubsidy := phase0.Gwei(blockValueGwei + h.cfg.BlockValueSubsidyGwei)
	maxExecutionPayment := h.prefsStore.GetOrDefault(proposerPubkey)
	executionPayment := min(valueAfterSubsidy, maxExecutionPayment)
	value := valueAfterSubsidy - executionPayment

	signedBid, err := payload_bidder.BuildSignedBid(event, payload_bidder.BidParams{
		BuilderIndex:     h.builderIndex.Load(),
		FeeRecipient:     prefs.FeeRecipient,
		Value:            value,
		ExecutionPayment: executionPayment,
	}, h.bidderSigner, gloasForkVersion, h.chainSvc.GetGenesis().GenesisValidatorsRoot)
	if err != nil {
		log.WithError(err).Warn("getExecutionPayloadBid: failed to build signed bid")
		writeError(w, http.StatusInternalServerError, "failed to build signed bid")
		return
	}

	event.AddBid(payload_builder.BidRecord{
		Transport:        payload_builder.BidTransportBuilderAPI,
		Value:            value,
		ExecutionPayment: executionPayment,
		At:               time.Now(),
	})

	log.WithFields(logrus.Fields{
		"block_hash":    "0x" + hex.EncodeToString(event.BlockHash[:]),
		"builder_index": signedBid.Message.BuilderIndex,
		"fee_recipient": prefs.FeeRecipient.String(),
		"gas_limit":     signedBid.Message.GasLimit,
	}).Info("getExecutionPayloadBid: delivered Gloas SignedExecutionPayloadBid")

	if h.events != nil {
		h.events.BroadcastBuilderAPIGetBidDelivered(
			uint64(slot),
			"0x"+hex.EncodeToString(event.BlockHash[:]),
			fmt.Sprintf("%d", uint64(signedBid.Message.Value)),
		)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Eth-Consensus-Version", "gloas")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(GetExecutionPayloadBidResponse{
		Version: "gloas",
		Data:    signedBid,
	})
}
