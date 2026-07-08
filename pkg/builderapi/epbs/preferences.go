package epbs

import (
	"encoding/hex"
	"errors"
	"io"
	"net/http"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
)

// HandleSubmitBuilderPreferences handles POST /eth/v1/builder/builder_preferences/{validator_pubkey}.
//
// It records the validator's latest max_execution_payment after authenticating
// the request via the embedded SignedRequestAuthV1. Per the Gloas builder-specs,
// the builder MUST verify the auth signature against the validator_pubkey path
// param (401 on failure) and MUST check that auth.message.builder_url matches its
// own URL (400 on failure). The preference is stored only after both checks pass.
// On success it returns 202.
func (h *Handler) HandleSubmitBuilderPreferences(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithField("path", "/eth/v1/builder/builder_preferences")

	if !h.enabled.Load() {
		log.Warn("submitBuilderPreferences: 503 — builder API disabled")
		writeError(w, http.StatusServiceUnavailable, "builder not ready")
		return
	}

	// The builder MUST check auth.message.builder_url against its own URL. Without a
	// configured URL it cannot perform that mandatory check, so treat it as a server
	// misconfiguration (500) rather than a client error.
	if h.cfg.BuilderURL == "" {
		log.Error("submitBuilderPreferences: 500 — builder URL not configured; cannot verify auth.message.builder_url")
		writeError(w, http.StatusInternalServerError, "builder URL not configured")
		return
	}

	pubkeyBytes, hexErr := hex.DecodeString(trimHex(mux.Vars(r)["validator_pubkey"]))
	if hexErr != nil || len(pubkeyBytes) != 48 {
		log.WithError(hexErr).Warn("submitBuilderPreferences: invalid validator_pubkey")
		writeError(w, http.StatusBadRequest, "invalid validator_pubkey: must be 48 bytes hex")
		return
	}
	var validatorPubkey phase0.BLSPubKey
	copy(validatorPubkey[:], pubkeyBytes)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.WithError(err).Warn("submitBuilderPreferences: failed to read body")
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	req, parseErr := parseBuilderPreferencesRequest(body, r.Header.Get("Content-Type"))
	if parseErr != nil {
		code := http.StatusBadRequest
		if errors.Is(parseErr, errUnsupportedContentType) {
			code = http.StatusUnsupportedMediaType
		}
		log.WithError(parseErr).Warn("submitBuilderPreferences: invalid request body")
		writeError(w, code, "invalid BuilderPreferencesRequestV1: "+parseErr.Error())
		return
	}
	if req.Preferences == nil {
		log.Warn("submitBuilderPreferences: missing preferences")
		writeError(w, http.StatusBadRequest, "invalid BuilderPreferencesRequestV1: preferences is null")
		return
	}
	if req.Auth == nil || req.Auth.Message == nil {
		log.Warn("submitBuilderPreferences: missing auth")
		writeError(w, http.StatusBadRequest, "invalid BuilderPreferencesRequestV1: auth is null")
		return
	}

	// Check auth.message.data (the builder URL) matches this builder's URL (400 on mismatch).
	if string(req.Auth.Message.Data) != h.cfg.BuilderURL {
		log.WithFields(logrus.Fields{
			"auth_url":    string(req.Auth.Message.Data),
			"builder_url": h.cfg.BuilderURL,
		}).Warn("submitBuilderPreferences: builder_url mismatch")
		writeError(w, http.StatusBadRequest, "auth.message.data does not match this builder's URL")
		return
	}

	// Verify the BLS signature against the validator_pubkey path param (401 on failure).
	// RequestAuth is signed with DOMAIN_REQUEST_AUTH at the genesis fork version — an
	// application-space domain, not chain-fork bound.
	if authErr := VerifyRequestAuth(req.Auth, validatorPubkey, h.chainSvc.GetGenesis().GenesisForkVersion); authErr != nil {
		log.WithError(authErr).Warn("submitBuilderPreferences: signature verification failed")
		writeError(w, http.StatusUnauthorized, "invalid SignedRequestAuthV1: signature verification failed")
		return
	}

	// Auth validated — record the latest preference (overwrites any previous value).
	h.prefsStore.Set(validatorPubkey, phase0.Gwei(req.Preferences.MaxExecutionPayment))
	log.WithFields(logrus.Fields{
		"validator_pubkey":      "0x" + hex.EncodeToString(validatorPubkey[:]),
		"max_execution_payment": uint64(req.Preferences.MaxExecutionPayment),
	}).Info("submitBuilderPreferences: stored builder preference")

	w.WriteHeader(http.StatusAccepted)
}
