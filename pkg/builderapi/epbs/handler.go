// Package epbs implements the post-Gloas (Gloas/Heze+) Builder API dialect:
// execution payload bid delivery, signed beacon block acceptance (with the
// payload reveal delegated to the shared RevealService), and builder
// preferences, on top of the shared payload cache.
// See https://github.com/ethereum/builder-specs/blob/epbs-spec-updates/specs/gloas/builder.md
package epbs

import (
	"context"
	"sync/atomic"

	"github.com/ethpandaops/go-eth2-client/api"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/proposerpreferences"
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

	revealSvc      *payload_bidder.RevealService // SetRevealService — the ONLY reveal path
	propPrefsCache *proposerpreferences.Cache    // SetProposerPreferencesCache
	prefsStore     *BuilderPreferencesStore      // created in NewHandler
	broadcaster    BlockBroadcaster              // SetBlockBroadcaster
	events         EventBroadcaster              // SetEventBroadcaster (nil-checked)

	builderIndex   atomic.Uint64 // builder index used in Gloas bids; set after lifecycle registration
	enabled        atomic.Bool
	bidsRequested  atomic.Uint64 // count of getExecutionPayloadBid requests received
	blocksAccepted atomic.Uint64 // count of accepted signed beacon blocks
}

// NewHandler creates a new post-Gloas Builder API dialect handler. cfg is the
// shared mutable config pointer; values are read live, never copied out.
func NewHandler(cfg *config.BuilderAPIConfig, log logrus.FieldLogger, chainSvc chain.Service,
	payloadCache *payload_builder.PayloadCache, blsSigner *signer.BLSSigner) *Handler {
	return &Handler{
		cfg:          cfg,
		log:          log.WithField("component", "builderapi-epbs"),
		chainSvc:     chainSvc,
		payloadCache: payloadCache,
		bidderSigner: payload_bidder.NewSigner(blsSigner),
		blsSigner:    blsSigner,
		prefsStore:   NewBuilderPreferencesStore(),
	}
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

// SetProposerPreferencesCache wires the proposer preferences cache used to
// resolve the fee recipient when building Gloas execution payload bids.
func (h *Handler) SetProposerPreferencesCache(cache *proposerpreferences.Cache) {
	h.propPrefsCache = cache
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
