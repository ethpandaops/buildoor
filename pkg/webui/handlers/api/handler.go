package api

import (
	"github.com/ethpandaops/buildoor/pkg/builderapi"
	"github.com/ethpandaops/buildoor/pkg/builderapi/validators"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/epbs"
	"github.com/ethpandaops/buildoor/pkg/lifecycle"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/proposerpreferences"
	"github.com/ethpandaops/buildoor/pkg/validatorranges"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers/auth"
)

// APIHandler handles API requests for the buildoor web UI.
type APIHandler struct {
	authHandler    *auth.AuthHandler
	settingsSvc    *config.Service // central settings authority (single writer)
	stateDB        *db.Database    // optional state-db (may be disabled)
	builderSvc     *payload_builder.Service
	epbsSvc        *epbs.Service                // May be nil
	lifecycleMgr   *lifecycle.Manager           // May be nil
	chainSvc       chain.Service                // May be nil
	validatorStore *validators.Store            // May be nil (only set when Builder API enabled)
	builderAPISvc  *builderapi.Server           // May be nil (only set when Builder API enabled)
	propPrefSvc    *proposerpreferences.Service // May be nil (only set when P2P peer addrs configured)
	valRanges      *validatorranges.Resolver    // May be nil
	eventStreamMgr *EventStreamManager          // May be nil
}

// NewAPIHandler creates a new API handler.
func NewAPIHandler(
	authHandler *auth.AuthHandler,
	settingsSvc *config.Service,
	stateDB *db.Database,
	builderSvc *payload_builder.Service,
	epbsSvc *epbs.Service,
	lifecycleMgr *lifecycle.Manager,
	chainSvc chain.Service,
	validatorStore *validators.Store,
	builderAPISvc *builderapi.Server,
	propPrefSvc *proposerpreferences.Service,
	valRanges *validatorranges.Resolver,
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
	}

	// Create and start event stream manager
	if builderSvc != nil {
		h.eventStreamMgr = NewEventStreamManager(
			builderSvc, epbsSvc, lifecycleMgr, chainSvc,
			builderAPISvc,
		)
		h.eventStreamMgr.Start()
	}

	return h
}

// GetEventStreamManager returns the event stream manager for external use.
func (h *APIHandler) GetEventStreamManager() *EventStreamManager {
	return h.eventStreamMgr
}
