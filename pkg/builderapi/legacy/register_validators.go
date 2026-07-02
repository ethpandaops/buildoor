package legacy

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	"github.com/sirupsen/logrus"
)

// HandleRegisterValidators handles POST /eth/v1/builder/validators.
// Accepts a JSON array of SignedValidatorRegistration, verifies each signature,
// and stores valid registrations. Returns 200 on success, 400 on validation failure.
func (h *Handler) HandleRegisterValidators(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithField("path", "/eth/v1/builder/validators")
	defer func() {
		if err := recover(); err != nil {
			log.WithField("panic", err).Error("Panic in validator registration handler")
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("internal error: %v", err))
		}
	}()

	log.WithFields(logrus.Fields{
		"method":         r.Method,
		"content_type":   r.Header.Get("Content-Type"),
		"content_length": r.Header.Get("Content-Length"),
	}).Debug("Validator registration request received")

	if r.Header.Get("Content-Type") != "application/json" {
		log.WithField("content_type", r.Header.Get("Content-Type")).Warn("Rejected: Content-Type must be application/json")
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.WithError(err).Warn("Rejected: failed to read body")
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	var regs []*apiv1.SignedValidatorRegistration
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&regs); err != nil {
		log.WithError(err).WithField("request_body_json", string(body)).Warn("Rejected: invalid JSON body")
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	log.WithField("count", len(regs)).Debug("Decoded validator registrations")

	forkVersion, err := h.chainSvc.GetForkVersion()
	if err != nil {
		log.WithError(err).Warn("Rejected: failed to get fork version")
		writeError(w, http.StatusInternalServerError, "failed to get fork version")
		return
	}

	for i, reg := range regs {
		if reg == nil || reg.Message == nil {
			log.WithFields(logrus.Fields{"index": i, "total": len(regs)}).Warn("Rejected: registration message missing")
			writeError(w, http.StatusBadRequest, "registration message missing")
			return
		}
		pubkeyHex := hex.EncodeToString(reg.Message.Pubkey[:])
		if !VerifyRegistrationWithDomain(reg, h.chainSvc.GetGenesis().GenesisForkVersion, forkVersion, h.chainSvc.GetGenesis().GenesisValidatorsRoot) {
			// Log first failing registration as JSON for debugging (copy and share).
			rejJSON, _ := json.Marshal(reg)
			log.WithFields(logrus.Fields{
				"index":             i,
				"total":             len(regs),
				"pubkey":            pubkeyHex,
				"rejected_reg_json": string(rejJSON),
			}).Warn("Rejected: invalid signature for validator")
			writeError(w, http.StatusBadRequest, "invalid signature for validator "+pubkeyHex)
			return
		}
		h.validatorsStore.Put(reg.Message.Pubkey, reg)
		log.WithFields(logrus.Fields{"index": i, "pubkey": pubkeyHex}).Debug("Stored validator registration")
	}

	log.WithField("stored_count", len(regs)).Info("Validator registrations accepted")
	w.WriteHeader(http.StatusOK)
}
