// Package epbs implements the post-Gloas (Gloas/Heze+) Builder API dialect:
// execution payload bid delivery, signed beacon block acceptance (with the
// payload reveal delegated to the shared RevealService), and builder
// preferences, on top of the shared payload cache.
// See https://github.com/ethereum/builder-specs/blob/epbs-spec-updates/specs/gloas/builder.md
package epbs

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethpandaops/go-eth2-client/api"
	gloasspec "github.com/ethpandaops/go-eth2-client/spec/gloas"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/signer"
)

// EventBroadcaster is the narrow WebUI event surface the post-Gloas dialect needs
// (satisfied structurally by the webui EventStreamManager).
type EventBroadcaster interface {
	BroadcastBuilderAPIGetBidReceived(slot uint64, parentHash, pubkey string)
	BroadcastBuilderAPIGetBidDelivered(slot uint64, blockHash, blockValue string)
	BroadcastBuilderAPISubmitBlockReceived(slot uint64, blockHash string)
	BroadcastBuilderAPISubmitBlockDelivered(slot uint64, blockHash string)
}

// SlotResultRecorder is the narrow per-slot result recording surface the
// post-Gloas dialect needs (satisfied structurally by the slot-results
// tracker, via the parent builderapi.SlotResultRecorder).
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

	submissionDialect = "epbs"
)

// maxRecordedBidSlots bounds the per-handler bid-record dedupe map.
const maxRecordedBidSlots = 16

// recordedBid is the dedupe fingerprint of the last recorded bid per slot;
// getExecutionPayloadBid may be polled and repeated identical outcomes must
// be recorded (and captured as an artifact) only once.
type recordedBid struct {
	status    string
	blockHash string
	value     uint64
	errMsg    string
}

// BlockBroadcaster publishes the proposer's signed beacon block
// (implemented by *beacon.Client.SubmitProposal).
type BlockBroadcaster interface {
	SubmitProposal(ctx context.Context, opts *api.SubmitProposalOpts) error
}

// Handler serves the post-Gloas Builder API dialect endpoints
// (getExecutionPayloadBid, submitBeaconBlock, submitBuilderPreferences). It is
// constructed and mounted by the parent builderapi.Server. Payload reveals are
// never published from the HTTP handlers: accepted beacon blocks hand the
// reveal to the shared payload_bidder.RevealService, which publishes the
// envelope at the configured reveal time.
type Handler struct {
	cfg          *config.BuilderAPIConfig // shared pointer, read live
	log          logrus.FieldLogger
	chainSvc     chain.Service
	payloadCache *payload_builder.PayloadCache
	bidderSigner *payload_bidder.Signer // shared Gloas bid signer (wraps blsSigner)
	blsSigner    *signer.BLSSigner      // IsBuilderActive pubkey check

	// planSvc is the mandatory per-slot scheduling/settings authority: bid
	// serving is decided exclusively by the slot's frozen plan.
	planSvc *action_plan.PlanService

	revealSvc      *payload_bidder.RevealService                                      // SetRevealService — the ONLY reveal path
	propPrefsStore *memstore.Store[phase0.Slot, *gloasspec.SignedProposerPreferences] // SetProposerPreferencesStore
	prefsStore     *BuilderPreferencesStore                                           // created in NewHandler
	broadcaster    BlockBroadcaster                                                   // SetBlockBroadcaster
	events         EventBroadcaster                                                   // SetEventBroadcaster (nil-checked)
	recorder       SlotResultRecorder                                                 // SetResultRecorder (nil-checked)

	lastBidMu sync.Mutex
	lastBids  map[phase0.Slot]recordedBid // dedupe of repeated identical bid records

	builderIndex   atomic.Uint64 // builder index used in Gloas bids; set after lifecycle registration
	enabled        atomic.Bool
	bidsRequested  atomic.Uint64 // count of getExecutionPayloadBid requests received
	blocksAccepted atomic.Uint64 // count of accepted signed beacon blocks
}

// NewHandler creates a new post-Gloas Builder API dialect handler. cfg is the
// shared mutable config pointer; values are read live, never copied out.
// planSvc is the mandatory per-slot scheduling/settings authority consulted
// (via Freeze) on every getExecutionPayloadBid request.
func NewHandler(cfg *config.BuilderAPIConfig, log logrus.FieldLogger, chainSvc chain.Service,
	planSvc *action_plan.PlanService, payloadCache *payload_builder.PayloadCache,
	blsSigner *signer.BLSSigner) *Handler {
	return &Handler{
		cfg:          cfg,
		log:          log.WithField("component", "builderapi-epbs"),
		chainSvc:     chainSvc,
		planSvc:      planSvc,
		payloadCache: payloadCache,
		bidderSigner: payload_bidder.NewSigner(blsSigner),
		blsSigner:    blsSigner,
		prefsStore:   NewBuilderPreferencesStore(),
		lastBids:     make(map[phase0.Slot]recordedBid, maxRecordedBidSlots),
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
// transformThreshold bounds how long an operator jq transform may run before
// it is cancelled, so a pathological expression cannot hang a request.
const transformTimeout = 2 * time.Second

func (h *Handler) frozenBuilderAPISettings(slot phase0.Slot) (*action_plan.ResolvedBuilderAPISettings, bool) {
	frozen := h.planSvc.Freeze(slot)
	if frozen.BuilderAPI == nil {
		return nil, false
	}

	return frozen.BuilderAPI, true
}

// recordBid forwards a bid outcome to the result recorder, deduping repeated
// identical outcomes per slot: a record is skipped when the previous record
// for the slot carried the same status, block hash, total value, and error.
// blockHash is only the dedupe fingerprint; the recorder derives everything
// else from signedBid.
func (h *Handler) recordBid(slot phase0.Slot, forkName, blockHash string, signedBid any,
	totalValueGwei, executionPaymentGwei uint64, status, errMsg string) {
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

	h.recorder.RecordBuilderAPIBid(slot, forkName, signedBid, totalValueGwei, executionPaymentGwei, status, errMsg)
}

// recordSubmission forwards a beacon-block submission outcome to the result
// recorder (nil-guarded, never deduped).
func (h *Handler) recordSubmission(slot phase0.Slot, status, errMsg string) {
	if h.recorder == nil {
		return
	}

	h.recorder.RecordBlockSubmission(slot, submissionDialect, status, errMsg)
}

// GetBuilderPreferencesStore returns the store of latest per-validator builder
// preferences submitted via the submitBuilderPreferences API.
func (h *Handler) GetBuilderPreferencesStore() *BuilderPreferencesStore {
	return h.prefsStore
}

// SetRevealService wires the shared reveal service that publishes execution
// payload envelopes at the configured reveal time.
func (h *Handler) SetRevealService(rs *payload_bidder.RevealService) {
	h.revealSvc = rs
}

// SetProposerPreferencesStore wires the per-slot proposer preferences store
// (owned by payload_bidder.ProposerPreferencesService) used to resolve the fee
// recipient when building Gloas execution payload bids.
func (h *Handler) SetProposerPreferencesStore(
	store *memstore.Store[phase0.Slot, *gloasspec.SignedProposerPreferences]) {
	h.propPrefsStore = store
}

// SetBlockBroadcaster wires the broadcaster used to publish the proposer's
// signed beacon block (e.g. the beacon node client).
func (h *Handler) SetBlockBroadcaster(b BlockBroadcaster) {
	h.broadcaster = b
}

// SetEventBroadcaster sets the optional event broadcaster for WebUI events.
func (h *Handler) SetEventBroadcaster(b EventBroadcaster) {
	h.events = b
}

// SetBuilderIndex sets the on-chain builder index inserted into Gloas bids.
// Called from the lifecycle manager once registration is observed.
func (h *Handler) SetBuilderIndex(index uint64) {
	h.builderIndex.Store(index)
}

// SetEnabled sets the enabled state of the post-Gloas Builder API dialect.
// Only the non-slot-scoped endpoints (beacon block submission, builder
// preferences) follow this flag; getExecutionPayloadBid bid serving is
// decided exclusively by the slot's frozen action plan.
func (h *Handler) SetEnabled(enabled bool) {
	h.enabled.Store(enabled)
}

// BidsRequested returns the count of getExecutionPayloadBid requests received.
func (h *Handler) BidsRequested() uint64 {
	return h.bidsRequested.Load()
}

// BlocksAccepted returns the count of accepted signed beacon blocks.
func (h *Handler) BlocksAccepted() uint64 {
	return h.blocksAccepted.Load()
}
