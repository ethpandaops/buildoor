package p2p

import (
	"context"
	"fmt"

	"github.com/attestantio/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// BeaconStatusProvider implements StatusProvider by querying the beacon REST API.
// It computes the fork digest with the full cumulative BPO XOR chain, matching
// Prysm's params.ForkDigest(currentEpoch) behaviour.
type BeaconStatusProvider struct {
	client    *beacon.Client
	chainSpec *beacon.ChainSpec
	genesis   *beacon.Genesis
}

// NewBeaconStatusProvider creates a StatusProvider backed by the given beacon client.
// chainSpec is used to apply the BPO XOR chain when computing the fork digest.
// genesis provides the genesis validators root required for fork digest computation.
func NewBeaconStatusProvider(client *beacon.Client, chainSpec *beacon.ChainSpec, genesis *beacon.Genesis) *BeaconStatusProvider {
	return &BeaconStatusProvider{
		client:    client,
		chainSpec: chainSpec,
		genesis:   genesis,
	}
}

// GetChainStatus fetches the current chain status and builds a StatusV2 message.
// The fork digest is computed as:
//
//	base = compute_fork_data_root(current_fork_version, genesis_validators_root)[:4]
//	for each BlobSchedule entry with entry.Epoch <= current_epoch:
//	    base = base XOR sha256(entry.Epoch_le64 || entry.MaxBlobsPerBlock_le64)[:4]
//
// This matches Prysm's params.ForkDigest(currentEpoch) computation.
func (p *BeaconStatusProvider) GetChainStatus(ctx context.Context) (*StatusMessage, error) {
	result, err := p.client.GetChainStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("beacon GetChainStatus: %w", err)
	}

	currentForkVersion, err := p.client.GetCurrentForkVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("get current fork version: %w", err)
	}

	forkDigest, err := ComputeForkDigestWithBPO(
		currentForkVersion,
		p.genesis.GenesisValidatorsRoot,
		p.chainSpec,
	)
	if err != nil {
		return nil, fmt.Errorf("compute fork digest: %w", err)
	}

	return &StatusMessage{
		ForkDigest:            forkDigest,
		FinalizedRoot:         [32]byte(result.FinalizedRoot),
		FinalizedEpoch:        result.FinalizedEpoch,
		HeadRoot:              [32]byte(result.HeadRoot),
		HeadSlot:              result.HeadSlot,
		EarliestAvailableSlot: 0, // builder doesn't serve historical data
	}, nil
}

// ComputeForkDigestWithBPO computes the fork digest, applying all BPO (Blob Parameters
// Only) XOR modifications from the blob schedule unconditionally.
// Prysm applies every entry in the BLOB_SCHEDULE to the fork digest regardless of
// whether the entry's activation epoch has been reached, so we do the same here.
// This must be used for BOTH Status RPC messages and gossip topic names.
func ComputeForkDigestWithBPO(
	forkVersion phase0.Version,
	genesisValidatorsRoot phase0.Root,
	chainSpec *beacon.ChainSpec,
) ([4]byte, error) {
	forkData := &phase0.ForkData{
		CurrentVersion:        forkVersion,
		GenesisValidatorsRoot: genesisValidatorsRoot,
	}

	forkDataRoot, err := forkData.HashTreeRoot()
	if err != nil {
		return [4]byte{}, fmt.Errorf("hash tree root: %w", err)
	}

	var digest [4]byte
	copy(digest[:], forkDataRoot[:4])

	if chainSpec == nil || len(chainSpec.BlobSchedule) == 0 {
		return digest, nil
	}

	// Apply BPO XOR for every blob schedule entry unconditionally.
	// Prysm includes the full BLOB_SCHEDULE in the fork digest regardless of activation epoch.
	for _, bpo := range chainSpec.BlobSchedule {
		digest = ApplyBPO(digest, bpo.Epoch, bpo.MaxBlobsPerBlock)
	}

	return digest, nil
}
