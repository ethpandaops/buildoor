package beacon

import (
	"context"
	"fmt"

	eth2client "github.com/ethpandaops/go-eth2-client"
	"github.com/ethpandaops/go-eth2-client/api"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

// ChainStatusResult holds the head and finality data needed to populate a libp2p StatusV2 message.
// Fork digest computation is intentionally excluded here — callers that have access to the chain
// spec (BPO schedule) are responsible for computing it and applying all cumulative BPO XORs.
type ChainStatusResult struct {
	FinalizedRoot  phase0.Root
	FinalizedEpoch uint64
	HeadRoot       phase0.Root
	HeadSlot       uint64
}

// GetChainStatus fetches the current chain head and finality data from the beacon node.
// It does NOT compute the fork digest — use GetCurrentForkVersion alongside the chain spec's
// BlobSchedule to compute the correct BPO-modified fork digest for StatusV2 messages.
func (c *Client) GetChainStatus(ctx context.Context) (*ChainStatusResult, error) {
	head, err := c.getHeadBlockHeader(ctx)
	if err != nil {
		return nil, fmt.Errorf("get head block header: %w", err)
	}

	c.log.WithField("head_slot", head.Slot).WithField("head_root", fmt.Sprintf("%x", head.Root[:4])).Debug("Fetched head block header for chain status")

	finality, err := c.getFinalityCheckpoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("get finality checkpoints: %w", err)
	}

	c.log.WithField("finalized_epoch", finality.FinalizedEpoch).WithField("finalized_root", fmt.Sprintf("%x", finality.FinalizedRoot[:4])).Debug("Fetched finality checkpoints for chain status")

	return &ChainStatusResult{
		FinalizedRoot:  finality.FinalizedRoot,
		FinalizedEpoch: finality.FinalizedEpoch,
		HeadRoot:       head.Root,
		HeadSlot:       head.Slot,
	}, nil
}

// GetCurrentForkVersion fetches the current fork version from the beacon node's head state.
// This reflects the currently active fork (e.g. Fulu or Gloas), not a future scheduled fork.
func (c *Client) GetCurrentForkVersion(ctx context.Context) (phase0.Version, error) {
	return c.GetForkVersion(ctx)
}

type headBlockHeader struct {
	Root phase0.Root
	Slot uint64
}

func (c *Client) getHeadBlockHeader(ctx context.Context) (*headBlockHeader, error) {
	provider, ok := c.client.(eth2client.BeaconBlockHeadersProvider)
	if !ok {
		return nil, fmt.Errorf("client does not support beacon block headers provider")
	}

	resp, err := provider.BeaconBlockHeader(ctx, &api.BeaconBlockHeaderOpts{
		Block: "head",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get head block header: %w", err)
	}

	if resp.Data == nil || resp.Data.Header == nil || resp.Data.Header.Message == nil {
		return nil, fmt.Errorf("head block header response is nil")
	}

	return &headBlockHeader{
		Root: resp.Data.Root,
		Slot: uint64(resp.Data.Header.Message.Slot),
	}, nil
}

type finalityCheckpoints struct {
	FinalizedRoot  phase0.Root
	FinalizedEpoch uint64
}

func (c *Client) getFinalityCheckpoints(ctx context.Context) (*finalityCheckpoints, error) {
	provider, ok := c.client.(eth2client.FinalityProvider)
	if !ok {
		return nil, fmt.Errorf("client does not support finality provider")
	}

	resp, err := provider.Finality(ctx, &api.FinalityOpts{
		State: "head",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get finality checkpoints: %w", err)
	}

	if resp.Data == nil || resp.Data.Finalized == nil {
		return nil, fmt.Errorf("finality checkpoints response is nil")
	}

	return &finalityCheckpoints{
		FinalizedRoot:  resp.Data.Finalized.Root,
		FinalizedEpoch: uint64(resp.Data.Finalized.Epoch),
	}, nil
}
