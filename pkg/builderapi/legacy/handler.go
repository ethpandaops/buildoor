// Package legacy implements the pre-Gloas Builder API dialect (Electra/Fulu):
// validator registration, getHeader bid delivery, and blinded block submission
// on top of the shared payload cache.
package legacy

import (
	"context"
	"sync/atomic"

	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	apiv1fulu "github.com/ethpandaops/go-eth2-client/api/v1/fulu"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

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

// BlockPublisher submits unblinded block contents to the beacon node
// (implemented by *beacon.Client.SubmitFuluBlock).
type BlockPublisher interface {
	SubmitFuluBlock(ctx context.Context, contents *apiv1fulu.SignedBlockContents) error
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

	publisher BlockPublisher   // optional; set via SetBlockPublisher
	events    EventBroadcaster // optional; set via SetEventBroadcaster (nil-checked)

	enabled          atomic.Bool
	headersRequested atomic.Uint64
	blocksPublished  atomic.Uint64
}

// NewHandler creates a new pre-Gloas Builder API dialect handler. cfg is the
// shared mutable config pointer; values are read live, never copied out.
func NewHandler(cfg *config.BuilderAPIConfig, log logrus.FieldLogger, chainSvc chain.Service,
	payloadCache *payload_builder.PayloadCache,
	validatorsStore *memstore.Store[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration],
	blsSigner *signer.BLSSigner) *Handler {
	return &Handler{
		cfg:             cfg,
		log:             log.WithField("component", "builderapi-legacy"),
		chainSvc:        chainSvc,
		payloadCache:    payloadCache,
		validatorsStore: validatorsStore,
		blsSigner:       blsSigner,
	}
}

// SetBlockPublisher sets the publisher for unblinded blocks (e.g. beacon node client).
func (h *Handler) SetBlockPublisher(p BlockPublisher) {
	h.publisher = p
}

// SetEventBroadcaster sets the optional event broadcaster for WebUI events.
func (h *Handler) SetEventBroadcaster(b EventBroadcaster) {
	h.events = b
}

// SetEnabled sets the enabled state of the legacy Builder API dialect.
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
