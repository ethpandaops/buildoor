package api

import (
	"fmt"
	"time"

	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/validatorranges"
)

// EventTypeSlotOutlook announces what buildoor knows about the next slot
// before its build starts.
const EventTypeSlotOutlook EventType = "slot_outlook"

// SlotOutlookEvent describes the next slot: who proposes it, what that
// proposer told buildoor, and the action plan it will run under. Every field
// is public chain or plan data; explicit plan transactions are redacted.
type SlotOutlookEvent struct {
	Slot uint64 `json:"slot"`

	// ProposerIndex is unset when the epoch's duties are not cached yet.
	ProposerIndex  *uint64 `json:"proposer_index,omitempty"`
	ProposerClient string  `json:"proposer_client,omitempty"`

	// BuilderAPIRegistered reports whether the proposer registered with
	// the Builder API (unset when the Builder API does not run).
	BuilderAPIRegistered *bool  `json:"builder_api_registered,omitempty"`
	RegisteredGasLimit   uint64 `json:"registered_gas_limit,omitempty"`

	// ProposerPrefs reports whether gossiped Gloas proposer preferences
	// exist for the slot (unset before Gloas).
	ProposerPrefs     *bool  `json:"proposer_prefs,omitempty"`
	PrefsFeeRecipient string `json:"prefs_fee_recipient,omitempty"`
	PrefsGasLimit     uint64 `json:"prefs_gas_limit,omitempty"`

	// Plan is the slot's effective action plan (explicit or rule-derived);
	// unset when the slot runs on the global baseline.
	Plan   *action_plan.SlotPlan `json:"plan,omitempty"`
	Frozen bool                  `json:"frozen"`
}

// outlookSources are the read-only stores the slot outlook is built from.
// All of them are optional.
type outlookSources struct {
	settingsSvc    *config.Service
	validatorStore *memstore.Store[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration]
	propPrefSvc    *payload_bidder.ProposerPreferencesService
	valRanges      *validatorranges.Resolver
}

// AttachSources hands the manager the stores it reads for the slot outlook
// and the setting sources of the config event. Call before Start.
func (m *EventStreamManager) AttachSources(
	settingsSvc *config.Service,
	validatorStore *memstore.Store[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration],
	propPrefSvc *payload_bidder.ProposerPreferencesService,
	valRanges *validatorranges.Resolver,
) {
	m.sources = outlookSources{
		settingsSvc:    settingsSvc,
		validatorStore: validatorStore,
		propPrefSvc:    propPrefSvc,
		valRanges:      valRanges,
	}
}

// buildSlotOutlook assembles the outlook of a slot from the cached duties,
// the registration and preference stores and the action plan service.
func (m *EventStreamManager) buildSlotOutlook(slot phase0.Slot) *SlotOutlookEvent {
	out := &SlotOutlookEvent{Slot: uint64(slot)}

	if proposer, ok := m.lookupProposer(slot); ok {
		index := uint64(proposer)
		out.ProposerIndex = &index

		if m.sources.valRanges != nil {
			out.ProposerClient = m.sources.valRanges.GetClientName(proposer)
		}

		if m.sources.validatorStore != nil && m.builderAPISvc != nil {
			registered := false

			if pubkey := m.chainSvc.GetValidatorPubkeyByIndex(proposer); pubkey != nil {
				if reg, found := m.sources.validatorStore.Get(*pubkey); found && reg != nil && reg.Message != nil {
					registered = true
					out.RegisteredGasLimit = reg.Message.GasLimit
				}
			}

			out.BuilderAPIRegistered = &registered
		}
	}

	if m.sources.propPrefSvc != nil {
		pref, found := m.sources.propPrefSvc.GetStore().Get(slot)
		hasPrefs := found && pref != nil && pref.Message != nil
		out.ProposerPrefs = &hasPrefs

		if hasPrefs {
			out.PrefsFeeRecipient = fmt.Sprintf("0x%x", pref.Message.FeeRecipient[:])
			out.PrefsGasLimit = pref.Message.TargetGasLimit
		}
	}

	if m.planSvc != nil {
		out.Plan = m.planSvc.PlanForSlot(slot).Redacted()
		out.Frozen = m.planSvc.IsFrozen(slot)
	}

	return out
}

// lookupProposer resolves a slot's scheduled proposer from the cached epoch
// duties.
func (m *EventStreamManager) lookupProposer(slot phase0.Slot) (phase0.ValidatorIndex, bool) {
	if m.chainSvc == nil {
		return 0, false
	}

	spec := m.chainSvc.GetChainSpec()
	stats := m.chainSvc.GetEpochStats(m.chainSvc.GetEpochOfSlot(slot))

	if spec == nil || stats == nil || spec.SlotsPerEpoch == 0 {
		return 0, false
	}

	slotIndex := uint64(slot) % spec.SlotsPerEpoch
	if slotIndex >= uint64(len(stats.ProposerDuties)) {
		return 0, false
	}

	return stats.ProposerDuties[slotIndex], true
}

// broadcastSlotOutlook sends the outlook of the slot after current to every
// viewer. It runs at each slot start and after plan or rule changes (which
// can change the next slot's plan).
func (m *EventStreamManager) broadcastSlotOutlook(current phase0.Slot) {
	m.Broadcast(&StreamEvent{
		Type:      EventTypeSlotOutlook,
		Timestamp: time.Now().UnixMilli(),
		Data:      m.buildSlotOutlook(current + 1),
	})
}

// configEventData is the payload of the config event: the effective config
// plus the layer each non-default setting comes from.
func (m *EventStreamManager) configEventData() map[string]any {
	data := configToMap(m.builderSvc.GetConfig())
	if data != nil && m.sources.settingsSvc != nil {
		data["setting_sources"] = m.sources.settingsSvc.Sources()
	}

	return data
}
