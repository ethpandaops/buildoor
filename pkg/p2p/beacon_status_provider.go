package p2p

import (
	"context"
	"fmt"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// BeaconStatusProvider implements StatusProvider by querying the beacon REST API.
type BeaconStatusProvider struct {
	client *beacon.Client
}

// NewBeaconStatusProvider creates a StatusProvider backed by the given beacon client.
func NewBeaconStatusProvider(client *beacon.Client) *BeaconStatusProvider {
	return &BeaconStatusProvider{client: client}
}

// GetChainStatus fetches the current chain status from the beacon node and converts
// it into a StatusMessage for the StatusV2 RPC.
func (p *BeaconStatusProvider) GetChainStatus(ctx context.Context) (*StatusMessage, error) {
	result, err := p.client.GetChainStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("beacon GetChainStatus: %w", err)
	}

	return &StatusMessage{
		ForkDigest:            result.ForkDigest,
		FinalizedRoot:         [32]byte(result.FinalizedRoot),
		FinalizedEpoch:        result.FinalizedEpoch,
		HeadRoot:              [32]byte(result.HeadRoot),
		HeadSlot:              result.HeadSlot,
		EarliestAvailableSlot: result.EarliestAvailableSlot,
	}, nil
}
