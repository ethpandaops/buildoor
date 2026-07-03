package legacy

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"

	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/signer"
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

	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
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

// VerifyRegistration verifies the BLS signature of a validator registration
// using DOMAIN_APPLICATION_BUILDER with zero parameters (for tests).
// For chain-specific verification (e.g. mev-boost registrations), use VerifyRegistrationWithDomain.
func VerifyRegistration(reg *apiv1.SignedValidatorRegistration) bool {
	var zero phase0.Version
	var zeroRoot phase0.Root
	return VerifyRegistrationWithDomain(reg, zero, zero, zeroRoot)
}

// VerifyRegistrationWithDomain verifies the BLS signature of a validator registration
// using DOMAIN_APPLICATION_BUILDER. Tries (0,0), (genesisForkVersion, 0) from the beacon
// (mev-boost-relay style), then (forkVersion, genesisValidatorsRoot). Genesis fork version
// is taken from the beacon so local devnets with their own fork versions work.
func VerifyRegistrationWithDomain(reg *apiv1.SignedValidatorRegistration, genesisForkVersion, forkVersion phase0.Version, genesisValidatorsRoot phase0.Root) bool {
	if reg == nil || reg.Message == nil {
		return false
	}

	messageRoot, err := reg.Message.HashTreeRoot()
	if err != nil {
		return false
	}

	var root phase0.Root
	copy(root[:], messageRoot[:])

	var zeroVersion phase0.Version
	var zeroRoot phase0.Root

	// 1) (0, 0) — some clients use this for DOMAIN_APPLICATION_BUILDER.
	domainZero := signer.ComputeDomain(signer.DomainApplicationBuilder, zeroVersion, zeroRoot)
	signingRootZero := signer.ComputeSigningRoot(root, domainZero)
	if signer.VerifyBLSSignature(reg.Message.Pubkey, signingRootZero[:], reg.Signature) {
		return true
	}

	// 2) (genesisForkVersion, 0) — mev-boost-relay style; genesis fork from beacon (devnet-friendly).
	if genesisForkVersion != zeroVersion {
		domainRelay := signer.ComputeDomain(signer.DomainApplicationBuilder, genesisForkVersion, zeroRoot)
		signingRootRelay := signer.ComputeSigningRoot(root, domainRelay)
		if signer.VerifyBLSSignature(reg.Message.Pubkey, signingRootRelay[:], reg.Signature) {
			return true
		}
	}

	// 3) (forkVersion, genesisValidatorsRoot) — chain-specific domain.
	if forkVersion != zeroVersion || genesisValidatorsRoot != zeroRoot {
		domainChain := signer.ComputeDomain(signer.DomainApplicationBuilder, forkVersion, genesisValidatorsRoot)
		signingRootChain := signer.ComputeSigningRoot(root, domainChain)
		if signer.VerifyBLSSignature(reg.Message.Pubkey, signingRootChain[:], reg.Signature) {
			return true
		}
	}

	return false
}
