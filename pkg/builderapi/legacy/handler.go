// Package legacy implements the pre-Gloas Builder API dialect (Electra/Fulu):
// validator registration, getHeader bid delivery, and blinded block submission
// on top of the shared payload cache.
package legacy

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/ethpandaops/go-eth2-client/api"
	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// EventBroadcaster is the narrow WebUI event surface the legacy dialect needs
// (satisfied structurally by the webui EventStreamManager).
type EventBroadcaster interface {
	BroadcastBuilderAPIGetHeaderReceived(slot uint64, parentHash, pubkey string)
	BroadcastBuilderAPIGetHeaderDelivered(slot uint64, blockHash, blockValue string)
	BroadcastBuilderAPISubmitBlindedReceived(slot uint64, blockHash string)
	BroadcastBuilderAPISubmitBlindedDelivered(slot uint64, blockHash string)
}

// SlotResultRecorder is the narrow per-slot result recording surface the
// legacy dialect needs (satisfied structurally by the slot-results tracker,
// via the parent builderapi.SlotResultRecorder).
type SlotResultRecorder interface {
	RecordBuilderAPIBid(slot phase0.Slot, forkName string, signedBid any,
		totalValueGwei, executionPaymentGwei uint64, status string, errMsg string)
	RecordBlockSubmission(slot phase0.Slot, dialect string, status string, errMsg string)
}

// Recorder status / dialect values (the wire enums of the result tracker).
const (
	bidStatusServed     = "served"
	bidStatusSuppressed = "suppressed"
	bidStatusFailed     = "failed"
	bidStatusCancelled  = "cancelled"

	submissionStatusReceived = "received"
	submissionStatusAccepted = "accepted"
	submissionStatusFailed   = "failed"

	submissionDialect = "legacy"
)

// maxRecordedBidSlots bounds the per-handler bid-record dedupe map.
const maxRecordedBidSlots = 16

// recordedBid is the dedupe fingerprint of the last recorded bid per slot;
// getHeader polling repeats the identical outcome many times per slot and
// must be recorded (and captured as an artifact) only once.
type recordedBid struct {
	status    string
	blockHash string
	value     uint64
	errMsg    string
}

// ProposalSubmitter submits a full signed proposal to the beacon node
// (implemented by *beacon.Client).
type ProposalSubmitter interface {
	SubmitProposal(ctx context.Context, opts *api.SubmitProposalOpts) error
}

// Handler serves the pre-Gloas Builder API dialect endpoints
// (registerValidators, getHeader, submitBlindedBlock). It is constructed and
// mounted by the parent builderapi.Server.
type Handler struct {
	cfg             *config.BuilderAPIConfig // shared pointer, read live
	log             logrus.FieldLogger
	chainSvc        chain.Service
	payloadCache    *payload_builder.PayloadCache
	validatorsStore *memstore.Store[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration]
	blsSigner       *signer.BLSSigner

	// planSvc is the mandatory per-slot scheduling/settings authority: bid
	// serving is decided exclusively by the slot's frozen plan.
	planSvc *action_plan.PlanService

	clClient ProposalSubmitter  // optional; set via SetCLClient (nil-checked)
	events   EventBroadcaster   // optional; set via SetEventBroadcaster (nil-checked)
	recorder SlotResultRecorder // optional; set via SetResultRecorder (nil-checked)

	lastBidMu sync.Mutex
	lastBids  map[phase0.Slot]recordedBid // dedupe of repeated identical bid records

	enabled          atomic.Bool
	headersRequested atomic.Uint64
	blocksPublished  atomic.Uint64
}

// NewHandler creates a new pre-Gloas Builder API dialect handler. cfg is the
// shared mutable config pointer; values are read live, never copied out.
// planSvc is the mandatory per-slot scheduling/settings authority consulted
// (via Freeze) on every getHeader request.
func NewHandler(cfg *config.BuilderAPIConfig, log logrus.FieldLogger, chainSvc chain.Service,
	planSvc *action_plan.PlanService, payloadCache *payload_builder.PayloadCache,
	validatorsStore *memstore.Store[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration],
	blsSigner *signer.BLSSigner) *Handler {
	return &Handler{
		cfg:             cfg,
		log:             log.WithField("component", "builderapi-legacy"),
		chainSvc:        chainSvc,
		planSvc:         planSvc,
		payloadCache:    payloadCache,
		validatorsStore: validatorsStore,
		blsSigner:       blsSigner,
		lastBids:        make(map[phase0.Slot]recordedBid, maxRecordedBidSlots),
	}
}

// SetResultRecorder wires the optional per-slot result recorder.
func (h *Handler) SetResultRecorder(rec SlotResultRecorder) {
	h.recorder = rec
}

// frozenBuilderAPISettings resolves whether a bid may be served for the slot
// and with which effective settings. The frozen plan is the single authority:
// it can activate a globally disabled dialect and suppress an enabled one
// (frozen.BuilderAPI == nil → suppressed).
func (h *Handler) frozenBuilderAPISettings(slot phase0.Slot) (*action_plan.ResolvedBuilderAPISettings, bool) {
	frozen := h.planSvc.Freeze(slot)
	if frozen.BuilderAPI == nil {
		return nil, false
	}

	return frozen.BuilderAPI, true
}

// recordBid forwards a bid outcome to the result recorder, deduping repeated
// identical outcomes per slot (getHeader is polled): a record is skipped when
// the previous record for the slot carried the same status, block hash, total
// value, and error. blockHash is only the dedupe fingerprint; the recorder
// derives everything else from signedBid.
func (h *Handler) recordBid(slot phase0.Slot, forkName, blockHash string, signedBid any,
	totalValueGwei uint64, status, errMsg string) {
	if h.recorder == nil {
		return
	}

	entry := recordedBid{status: status, blockHash: blockHash, value: totalValueGwei, errMsg: errMsg}

	h.lastBidMu.Lock()
	if h.lastBids[slot] == entry {
		h.lastBidMu.Unlock()
		return
	}

	h.lastBids[slot] = entry

	// Bound the dedupe map: drop the smallest slots beyond the cap.
	for len(h.lastBids) > maxRecordedBidSlots {
		smallest := slot
		for s := range h.lastBids {
			if s < smallest {
				smallest = s
			}
		}

		delete(h.lastBids, smallest)
	}
	h.lastBidMu.Unlock()

	h.recorder.RecordBuilderAPIBid(slot, forkName, signedBid, totalValueGwei, 0, status, errMsg)
}

// recordSubmission forwards a blinded-block submission outcome to the result
// recorder (nil-guarded, never deduped).
func (h *Handler) recordSubmission(slot phase0.Slot, status, errMsg string) {
	if h.recorder == nil {
		return
	}

	h.recorder.RecordBlockSubmission(slot, submissionDialect, status, errMsg)
}

// SetCLClient sets the beacon node client used to publish unblinded blocks.
func (h *Handler) SetCLClient(c ProposalSubmitter) {
	h.clClient = c
}

// SetEventBroadcaster sets the optional event broadcaster for WebUI events.
func (h *Handler) SetEventBroadcaster(b EventBroadcaster) {
	h.events = b
}

// SetEnabled sets the enabled state of the legacy Builder API dialect. Only
// the non-slot-scoped endpoints (blinded block submission) follow this flag;
// getHeader bid serving is decided exclusively by the slot's frozen action
// plan.
func (h *Handler) SetEnabled(enabled bool) {
	h.enabled.Store(enabled)
}

// HeadersRequested returns the count of getHeader requests received.
func (h *Handler) HeadersRequested() uint64 {
	return h.headersRequested.Load()
}

// BlocksPublished returns the count of successfully published blocks.
func (h *Handler) BlocksPublished() uint64 {
	return h.blocksPublished.Load()
}
