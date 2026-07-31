package builder_keys

import (
	"fmt"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
)

// NextDepositCandidate returns the lowest-index key eligible for a fresh
// deposit: never used, or used before and since gone from the builder registry.
// Picking the lowest index keeps the highest derivation index bounded as the
// operator ramps the target up and down over a devnet's lifetime.
//
// It returns nil when the derivation cap leaves no eligible key.
func (r *Registry) NextDepositCandidate() *Key {
	for keyIndex := uint64(0); keyIndex <= r.maxIndex(); keyIndex++ {
		key, err := r.derive(keyIndex)
		if err != nil {
			r.log.WithError(err).WithField("key_index", keyIndex).
				Error("Failed to derive builder key for deposit")

			return nil
		}

		if key.Status().Depositable() {
			return key
		}
	}

	return nil
}

// NextExitCandidate returns the highest-index active key that can be exited: it
// must have no pending payments, because the beacon chain silently ignores an
// exit request while a builder still owes one. Returns nil when no key qualifies.
func (r *Registry) NextExitCandidate() *Key {
	keys := r.Keys()

	for i := len(keys) - 1; i >= 0; i-- {
		state := keys[i].State()
		if state.Status == StatusActive && state.PendingPayments == 0 {
			return keys[i]
		}
	}

	return nil
}

// MarkDepositSubmitted records a confirmed deposit transaction for a key: it
// bumps the persisted use count (the key has consumed a deposit generation) and
// holds the key in the depositing state until the beacon state catches up.
func (r *Registry) MarkDepositSubmitted(keyIndex uint64) {
	key, err := r.Key(keyIndex)
	if err != nil {
		r.log.WithError(err).WithField("key_index", keyIndex).Error("Cannot record deposit for unknown builder key")

		return
	}

	now := time.Now()

	usage, ok := r.usage.Get(keyIndex)
	if !ok || usage == nil {
		usage = &Usage{
			KeyIndex:    keyIndex,
			Pubkey:      fmt.Sprintf("%#x", key.Pubkey()),
			FirstUsedAt: now.UnixMilli(),
		}
	} else {
		copied := *usage
		usage = &copied
	}

	usage.UseCount++
	usage.LastDepositAt = now.UnixMilli()

	r.usage.Put(keyIndex, usage)

	r.mu.Lock()

	if runtime, ok := r.runtimes[keyIndex]; ok {
		runtime.depositPendingUntil = now.Add(depositPendingTTL)
	}

	r.mu.Unlock()

	// The key belongs to the fleet from here on, wherever it sits relative to
	// the target — the discovery scan must keep visiting it.
	r.setTracked(keyIndex, true)

	r.log.WithFields(logrus.Fields{
		"key":       key.String(),
		"use_count": usage.UseCount,
	}).Info("Recorded builder key deposit")

	r.Refresh()
}

// MarkExitSubmitted records a submitted exit request for a key.
func (r *Registry) MarkExitSubmitted(keyIndex uint64) {
	usage, ok := r.usage.Get(keyIndex)
	if !ok || usage == nil {
		return
	}

	copied := *usage
	copied.LastExitAt = time.Now().UnixMilli()

	r.usage.Put(keyIndex, &copied)

	r.Refresh()
}

// MarkToppedUp records the epoch of a submitted top-up, arming the per-key
// cooldown that keeps a low balance from queueing duplicate deposits before the
// pending one lands.
func (r *Registry) MarkToppedUp(keyIndex uint64, epoch phase0.Epoch) {
	r.mu.Lock()

	if runtime, ok := r.runtimes[keyIndex]; ok {
		runtime.lastTopupEpoch = epoch
	}

	r.mu.Unlock()

	r.Refresh()
}
