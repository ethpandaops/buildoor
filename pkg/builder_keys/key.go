// Package builder_keys owns buildoor's managed set of builder BLS keys: the
// internal derivation from the operator's entry key, each key's on-chain state
// (registration, balance, pending payments), the persisted usage history that
// makes withdrawn keys reusable, and the selection of a ready key per bid.
//
// Two index spaces meet here and must never be conflated:
//
//   - the KEY INDEX is our internal derivation index. It is stable forever;
//     index 0 is the operator's entry key, so a single-key deployment keeps its
//     identity when the fleet grows.
//   - the BUILDER INDEX is the beacon registry index assigned at deposit time.
//     It only exists once a key is registered and is reused by other builders
//     after an exit.
package builder_keys

import (
	"fmt"
	"sync/atomic"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/signer"
)

// Key is one derived builder key: a stable identity plus the latest snapshot of
// its on-chain state. Instances are created once by the Registry and are safe
// for concurrent use; the state snapshot is swapped atomically on every refresh,
// so readers on the bid/reveal hot paths never block or observe a torn value.
type Key struct {
	keyIndex uint64
	pubkey   phase0.BLSPubKey
	bls      *signer.BLSSigner

	state atomic.Pointer[State]
}

// newKey derives the key at the given internal index from the entry private key.
func newKey(entryPrivkeyHex string, keyIndex uint64) (*Key, error) {
	privkeyHex, err := signer.DeriveInternalKey(entryPrivkeyHex, keyIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to derive builder key %d: %w", keyIndex, err)
	}

	blsSigner, err := signer.NewBLSSigner(privkeyHex)
	if err != nil {
		return nil, fmt.Errorf("failed to create signer for builder key %d: %w", keyIndex, err)
	}

	k := &Key{
		keyIndex: keyIndex,
		pubkey:   blsSigner.PublicKey(),
		bls:      blsSigner,
	}

	k.state.Store(&State{
		KeyIndex: keyIndex,
		Pubkey:   k.pubkey,
		Status:   StatusUnused,
	})

	return k, nil
}

// KeyIndex returns the internal derivation index (0 = the entry key).
func (k *Key) KeyIndex() uint64 { return k.keyIndex }

// Pubkey returns the key's BLS public key.
func (k *Key) Pubkey() phase0.BLSPubKey { return k.pubkey }

// BLSSigner returns the underlying signer for bid, envelope and deposit signing.
func (k *Key) BLSSigner() *signer.BLSSigner { return k.bls }

// State returns the latest state snapshot. Never nil.
func (k *Key) State() *State { return k.state.Load() }

// BuilderIndex returns the on-chain builder index and whether the key is
// registered at all. Index 0 is a valid builder index, so the boolean — not a
// zero check — decides whether the value may be used.
func (k *Key) BuilderIndex() (uint64, bool) {
	state := k.state.Load()

	return state.BuilderIndex, state.HasBuilderIndex
}

// Status returns the key's current lifecycle status.
func (k *Key) Status() Status { return k.state.Load().Status }

// String renders the key for logs: internal index plus a pubkey prefix.
func (k *Key) String() string {
	return fmt.Sprintf("#%d/%x", k.keyIndex, k.pubkey[:4])
}
