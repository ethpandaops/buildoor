package p2p

import (
	"crypto/sha256"
	"encoding/binary"
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

// ApplyBPO XORs the fork digest with hash(bpoEpoch || maxBlobsPerBlock)[:4].
// This matches Prysm's BPO (Blob Parameters Only) fork digest modification for Fulu+ forks.
func ApplyBPO(digest [4]byte, bpoEpoch uint64, maxBlobsPerBlock uint64) [4]byte {
	var hb [16]byte
	binary.LittleEndian.PutUint64(hb[0:8], bpoEpoch)
	binary.LittleEndian.PutUint64(hb[8:16], maxBlobsPerBlock)

	bpoHash := sha256.Sum256(hb[:])

	return [4]byte{
		digest[0] ^ bpoHash[0],
		digest[1] ^ bpoHash[1],
		digest[2] ^ bpoHash[2],
		digest[3] ^ bpoHash[3],
	}
}

// BuildTopicName constructs a full gossip topic string in the format:
// /eth2/{fork-digest-hex}/{message-name}/ssz_snappy
func BuildTopicName(forkDigest [4]byte, messageName string) string {
	return fmt.Sprintf("/eth2/%x/%s/ssz_snappy", forkDigest, messageName)
}
