package fulu

import (
	"fmt"

	eth2all "github.com/ethpandaops/go-eth2-client/spec/all"
	"github.com/ethpandaops/go-eth2-client/spec/deneb"
	"github.com/ethpandaops/go-eth2-client/spec/gloas"
)

// DenebPayload returns the Deneb execution payload view of the fork-agnostic
// beacon payload (used for Deneb/Electra/Fulu unblinding).
func DenebPayload(p *eth2all.ExecutionPayload) (*deneb.ExecutionPayload, error) {
	if p == nil {
		return nil, nil
	}

	view, err := p.ToView()
	if err != nil {
		return nil, err
	}

	dp, ok := view.(*deneb.ExecutionPayload)
	if !ok {
		return nil, fmt.Errorf("expected deneb execution payload, got %T", view)
	}

	return dp, nil
}

// GloasPayload returns the Gloas execution payload view of the fork-agnostic
// beacon payload (used for Gloas+ envelope reveals).
func GloasPayload(p *eth2all.ExecutionPayload) (*gloas.ExecutionPayload, error) {
	if p == nil {
		return nil, nil
	}

	view, err := p.ToView()
	if err != nil {
		return nil, err
	}

	gp, ok := view.(*gloas.ExecutionPayload)
	if !ok {
		return nil, fmt.Errorf("expected gloas execution payload, got %T", view)
	}

	return gp, nil
}
