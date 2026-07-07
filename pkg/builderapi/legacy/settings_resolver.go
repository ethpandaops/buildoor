package legacy

import (
	"github.com/ethereum/go-ethereum/common"
	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// RegistrationSettingsResolver resolves pre-Gloas proposer settings from the
// validator registration store. It implements
// payload_builder.ProposerSettingsResolver and self-scopes: post-Gloas the
// gossip proposer-preferences resolver applies instead, so it returns false.
type RegistrationSettingsResolver struct {
	store    *memstore.Store[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration]
	chainSvc chain.Service
}

var _ payload_builder.ProposerSettingsResolver = (*RegistrationSettingsResolver)(nil)

// NewRegistrationSettingsResolver creates a resolver over the shared validator
// registration store.
func NewRegistrationSettingsResolver(
	store *memstore.Store[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration],
	chainSvc chain.Service,
) *RegistrationSettingsResolver {
	return &RegistrationSettingsResolver{
		store:    store,
		chainSvc: chainSvc,
	}
}

// ResolveProposerSettings looks up the proposer's validator registration and
// returns its fee recipient. TargetGasLimit is deliberately left 0 (not
// announced): the registration gas limit was never used for pre-Gloas builds
// and this preserves that behavior.
func (r *RegistrationSettingsResolver) ResolveProposerSettings(_ phase0.Slot,
	proposerIndex phase0.ValidatorIndex) (payload_builder.ProposerSettings, bool) {
	if r.chainSvc.GetCurrentFork() >= version.DataVersionGloas {
		return payload_builder.ProposerSettings{}, false
	}

	pubkey := r.chainSvc.GetValidatorPubkeyByIndex(proposerIndex)
	if pubkey == nil {
		return payload_builder.ProposerSettings{}, false
	}

	reg, ok := r.store.Get(*pubkey)
	if !ok || reg == nil || reg.Message == nil {
		return payload_builder.ProposerSettings{}, false
	}

	return payload_builder.ProposerSettings{
		FeeRecipient:   common.Address(reg.Message.FeeRecipient),
		TargetGasLimit: 0,
	}, true
}
