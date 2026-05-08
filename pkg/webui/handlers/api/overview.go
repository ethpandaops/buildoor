package api

import (
	"net/http"

	"github.com/ethpandaops/buildoor/pkg/epbs"
	"github.com/ethpandaops/buildoor/version"
)

// OverviewELClient is the EL client identification surfaced by the overview endpoint.
type OverviewELClient struct {
	Code    string `json:"code,omitempty"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Commit  string `json:"commit,omitempty"`
}

// OverviewServices captures which optional services are available and toggled on.
type OverviewServices struct {
	EPBSAvailable         bool   `json:"epbs_available"`
	EPBSEnabled           bool   `json:"epbs_enabled"`
	EPBSRegistrationState string `json:"epbs_registration_state,omitempty"`
	BuilderAPIAvailable   bool   `json:"builder_api_available"`
	BuilderAPIEnabled     bool   `json:"builder_api_enabled"`
	LifecycleAvailable    bool   `json:"lifecycle_available"`
	LifecycleEnabled      bool   `json:"lifecycle_enabled"`
}

// OverviewBalances captures CL builder + wallet balance for display.
type OverviewBalances struct {
	CLBalanceGwei        uint64 `json:"cl_balance_gwei,omitempty"`
	PendingPaymentsGwei  uint64 `json:"pending_payments_gwei,omitempty"`
	EffectiveBalanceGwei uint64 `json:"effective_balance_gwei,omitempty"`
	WalletAddress        string `json:"wallet_address,omitempty"`
	WalletBalanceWei     string `json:"wallet_balance_wei,omitempty"`
}

// OverviewStats is a compact subset of stats useful for the overview view.
type OverviewStats struct {
	SlotsBuilt                     uint64 `json:"slots_built"`
	BlocksIncluded                 uint64 `json:"blocks_included"`
	BidsSubmitted                  uint64 `json:"bids_submitted"`
	BidsWon                        uint64 `json:"bids_won"`
	BuilderAPIHeadersRequested     uint64 `json:"builder_api_headers_requested"`
	BuilderAPIBlocksPublished      uint64 `json:"builder_api_blocks_published"`
	BuilderAPIRegisteredValidators int    `json:"builder_api_registered_validators"`
}

// OverviewResponse is the response payload of /api/buildoor/overview — a compact
// summary used by the multi-instance overview UI.
type OverviewResponse struct {
	Version       string            `json:"version"`
	Running       bool              `json:"running"`
	BuilderPubkey string            `json:"builder_pubkey,omitempty"`
	BuilderIndex  uint64            `json:"builder_index,omitempty"`
	IsRegistered  bool              `json:"is_registered"`
	CurrentSlot   uint64            `json:"current_slot"`
	ELClient      *OverviewELClient `json:"el_client,omitempty"`
	Services      OverviewServices  `json:"services"`
	Balances      OverviewBalances  `json:"balances"`
	Stats         OverviewStats     `json:"stats"`
}

// GetOverview godoc
// @Id getOverview
// @Summary Get a compact overview of this buildoor instance
// @Tags Buildoor
// @Description Returns a single-payload summary used by the multi-instance overview UI:
// @Description running state, builder pubkey, current slot, EL client info, available/enabled
// @Description services, balances, and recent build stats.
// @Produce json
// @Success 200 {object} OverviewResponse "Success"
// @Router /api/buildoor/overview [get]
func (h *APIHandler) GetOverview(w http.ResponseWriter, r *http.Request) {
	// Allow the overview UI (served from a separate origin) to read this endpoint.
	w.Header().Set("Access-Control-Allow-Origin", "*")

	resp := OverviewResponse{
		Version:     version.GetBuildVersion(),
		Running:     true,
		CurrentSlot: uint64(h.builderSvc.GetCurrentSlot()),
	}

	// EL client identification from the builder service cache.
	if v := h.builderSvc.GetELClientVersion(); v != nil {
		resp.ELClient = &OverviewELClient{
			Code:    v.Code,
			Name:    v.Name,
			Version: v.Version,
			Commit:  v.Commit,
		}
	}

	// Service availability + enabled flags mirror /api/services/toggle response.
	resp.Services = OverviewServices{
		EPBSAvailable:       h.epbsSvc != nil,
		EPBSEnabled:         h.epbsSvc != nil && h.epbsSvc.IsEnabled(),
		BuilderAPIAvailable: h.builderAPISvc != nil,
		BuilderAPIEnabled:   h.builderAPISvc != nil && h.builderAPISvc.IsEnabled(),
		LifecycleAvailable:  h.lifecycleMgr != nil,
		LifecycleEnabled:    h.lifecycleMgr != nil && h.lifecycleMgr.IsEnabled(),
	}

	// Builder identity, registration and balances from ePBS + chain services.
	if h.epbsSvc != nil {
		pubkey := h.epbsSvc.GetBuilderPubkey()
		resp.BuilderPubkey = pubkey.String()
		resp.BuilderIndex = h.epbsSvc.GetBuilderIndex()
		resp.IsRegistered = h.epbsSvc.IsRegistered()
		resp.Services.EPBSRegistrationState = epbs.RegistrationStateName(h.epbsSvc.GetRegistrationState())

		if tracker := h.epbsSvc.GetBidTracker(); tracker != nil {
			resp.Balances.PendingPaymentsGwei = tracker.GetTotalPendingPayments()
		}

		if h.chainSvc != nil {
			if info := h.chainSvc.GetBuilderByPubkey(pubkey); info != nil {
				resp.Balances.CLBalanceGwei = info.Balance
			}
		}
	}

	if resp.Balances.CLBalanceGwei > resp.Balances.PendingPaymentsGwei {
		resp.Balances.EffectiveBalanceGwei = resp.Balances.CLBalanceGwei - resp.Balances.PendingPaymentsGwei
	}

	// Wallet info via lifecycle manager (only available when --lifecycle prerequisites are met).
	if h.lifecycleMgr != nil {
		if wallet := h.lifecycleMgr.GetWallet(); wallet != nil {
			resp.Balances.WalletAddress = wallet.Address().Hex()

			if balance, err := wallet.GetBalance(r.Context()); err == nil && balance != nil {
				resp.Balances.WalletBalanceWei = balance.String()
			}
		}
	}

	// Build stats — snapshot of counters maintained by the builder + builder API services.
	stats := h.builderSvc.GetStats()
	resp.Stats = OverviewStats{
		SlotsBuilt:     stats.SlotsBuilt,
		BlocksIncluded: stats.BlocksIncluded,
		BidsSubmitted:  stats.BidsSubmitted,
		BidsWon:        stats.BidsWon,
	}

	if h.builderAPISvc != nil {
		apiStats := h.builderAPISvc.GetRequestStats()
		resp.Stats.BuilderAPIHeadersRequested = apiStats.HeadersRequested
		resp.Stats.BuilderAPIBlocksPublished = apiStats.BlocksPublished
		resp.Stats.BuilderAPIRegisteredValidators = apiStats.ValidatorCount
	}

	writeJSON(w, http.StatusOK, resp)
}
