package p2p

import (
	"fmt"

	"github.com/attestantio/go-eth2-client/spec/phase0"
)

// ComputeForkDigest computes the 4-byte fork digest from a fork version and genesis validators root.
// Per spec: ForkDigest = compute_fork_data_root(current_version, genesis_validators_root)[:4]
func ComputeForkDigest(forkVersion phase0.Version, genesisValidatorsRoot phase0.Root) ([4]byte, error) {
	forkData := &phase0.ForkData{
		CurrentVersion:        forkVersion,
		GenesisValidatorsRoot: genesisValidatorsRoot,
	}

	root, err := forkData.HashTreeRoot()
	if err != nil {
		return [4]byte{}, fmt.Errorf("failed to compute fork data root: %w", err)
	}

	var digest [4]byte
	copy(digest[:], root[:4])

	return digest, nil
}

// BuildTopicName constructs a full gossip topic string in the format:
// /eth2/{fork-digest-hex}/{message-name}/ssz_snappy
func BuildTopicName(forkDigest [4]byte, messageName string) string {
	return fmt.Sprintf("/eth2/%x/%s/ssz_snappy", forkDigest, messageName)
}
