package payload_builder

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

// ProposerSettings are the proposer-announced build parameters for a slot.
type ProposerSettings struct {
	FeeRecipient   common.Address
	TargetGasLimit uint64 // 0 = not announced
}

// ProposerSettingsResolver resolves the proposer's announced settings for a
// build. Implementations self-scope: they return false when they don't apply
// to the slot's fork or hold no data (gossip preferences post-Gloas in
// payload_bidder; validator registrations pre-Gloas in builderapi/legacy).
type ProposerSettingsResolver interface {
	ResolveProposerSettings(slot phase0.Slot, proposerIndex phase0.ValidatorIndex) (ProposerSettings, bool)
}

// AddProposerSettingsResolver appends a resolver; the builder asks each
// registered resolver in order and uses the first match. Register before
// Start().
func (s *Service) AddProposerSettingsResolver(r ProposerSettingsResolver) {
	s.settingsResolvers = append(s.settingsResolvers, r)
}
