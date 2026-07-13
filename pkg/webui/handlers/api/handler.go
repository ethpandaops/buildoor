package api

import (
	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/builderapi"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/lifecycle"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/p2p_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/validatorranges"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers/auth"
)

// APIHandler handles API requests for the buildoor web UI.
type APIHandler struct {
	authHandler    *auth.AuthHandler
	settingsSvc    *config.Service // central settings authority (single writer)
	stateDB        *db.Database    // optional state-db (may be disabled)
	builderSvc     *payload_builder.Service
	epbsSvc        *p2p_bidder.Service                                                   // May be nil
	lifecycleMgr   *lifecycle.Manager                                                    // May be nil
	chainSvc       chain.Service                                                         // May be nil
	validatorStore *memstore.Store[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration] // May be nil (only set when Builder API enabled)
	builderAPISvc  *builderapi.Server                                                    // May be nil (only set when Builder API enabled)
	propPrefSvc    *payload_bidder.ProposerPreferencesService                            // May be nil (Gloas not scheduled)
	valRanges      *validatorranges.Resolver                                             // May be nil
	eventStreamMgr *EventStreamManager                                                   // May be nil

	revealSvc        *payload_bidder.RevealService    // May be nil (Gloas not scheduled)
	inclusionTracker *payload_bidder.InclusionTracker // May be nil
	payments         *payload_bidder.PaymentTracker   // May be nil (Gloas not scheduled)
	slotActions      *payload_bidder.SlotActionsStore // May be nil (Gloas not scheduled)
}

// NewAPIHandler creates a new API handler.
func NewAPIHandler(
	authHandler *auth.AuthHandler,
	settingsSvc *config.Service,
	stateDB *db.Database,
	builderSvc *payload_builder.Service,
	epbsSvc *p2p_bidder.Service,
	lifecycleMgr *lifecycle.Manager,
	chainSvc chain.Service,
	validatorStore *memstore.Store[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration],
	builderAPISvc *builderapi.Server,
	propPrefSvc *payload_bidder.ProposerPreferencesService,
	valRanges *validatorranges.Resolver,
	revealSvc *payload_bidder.RevealService,
	inclusionTracker *payload_bidder.InclusionTracker,
	payments *payload_bidder.PaymentTracker,
	slotActions *payload_bidder.SlotActionsStore,
) *APIHandler {
	h := &APIHandler{
		authHandler:    authHandler,
		settingsSvc:    settingsSvc,
		stateDB:        stateDB,
		builderSvc:     builderSvc,
		epbsSvc:        epbsSvc,
		lifecycleMgr:   lifecycleMgr,
		chainSvc:       chainSvc,
		validatorStore: validatorStore,
		builderAPISvc:  builderAPISvc,
		propPrefSvc:    propPrefSvc,
		valRanges:      valRanges,

		revealSvc:        revealSvc,
		inclusionTracker: inclusionTracker,
		payments:         payments,
		slotActions:      slotActions,
	}

	// Create and start event stream manager
	if builderSvc != nil {
		h.eventStreamMgr = NewEventStreamManager(
			builderSvc, epbsSvc, lifecycleMgr, chainSvc,
			builderAPISvc, revealSvc, inclusionTracker, payments, slotActions,
		)
		if slotActions != nil {
			slotActions.SetChangeCallback(h.eventStreamMgr.BroadcastConfigUpdate)
		}

		h.eventStreamMgr.Start()
	}

	return h
}

// GetEventStreamManager returns the event stream manager for external use.
func (h *APIHandler) GetEventStreamManager() *EventStreamManager {
	return h.eventStreamMgr
}
