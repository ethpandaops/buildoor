package api

import (
	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/epbs"
	"github.com/ethpandaops/buildoor/pkg/lifecycle"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers/auth"
)

// APIHandler handles API requests for the buildoor web UI.
type APIHandler struct {
	authHandler    *auth.AuthHandler
	builderSvc     *builder.Service
	epbsSvc        *epbs.Service       // May be nil
	lifecycleMgr   *lifecycle.Manager  // May be nil
	eventStreamMgr *EventStreamManager // May be nil
}

// NewAPIHandler creates a new API handler.
func NewAPIHandler(
	authHandler *auth.AuthHandler,
	builderSvc *builder.Service,
	epbsSvc *epbs.Service,
	lifecycleMgr *lifecycle.Manager,
) *APIHandler {
	h := &APIHandler{
		authHandler:  authHandler,
		builderSvc:   builderSvc,
		epbsSvc:      epbsSvc,
		lifecycleMgr: lifecycleMgr,
	}

	// Create and start event stream manager
	if builderSvc != nil {
		h.eventStreamMgr = NewEventStreamManager(builderSvc, epbsSvc, lifecycleMgr)
		h.eventStreamMgr.Start()
	}

	return h
}

// GetEventStreamManager returns the event stream manager for external use.
func (h *APIHandler) GetEventStreamManager() *EventStreamManager {
	return h.eventStreamMgr
}

// Stop stops the API handler and its components.
func (h *APIHandler) Stop() {
	if h.eventStreamMgr != nil {
		h.eventStreamMgr.Stop()
	}
}
