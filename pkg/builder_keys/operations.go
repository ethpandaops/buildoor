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

// NextExitCandidate returns the highest-index key that can be exited: active on
// chain and free of pending payments, since the beacon chain silently ignores an
// exit request while a builder still owes one.
//
// It returns nil while a higher-index key is still on its way to active. Those
// in-flight keys are the actual surplus — exiting a lower, usable key in their
// place would burn a key we just paid for and leave the fleet no smaller once
// they register.
func (r *Registry) NextExitCandidate() *Key {
	keys := r.Keys()

	for i := len(keys) - 1; i >= 0; i-- {
		state := keys[i].State()

		switch state.Status {
		case StatusDepositing, StatusPending:
			return nil
		case StatusActive:
			if state.PendingPayments == 0 {
				return keys[i]
			}

			// The payment settles within a couple of epochs; waiting keeps the
			// exit order intact instead of skipping down to a lower key.
			return nil
		case StatusUnused, StatusExiting, StatusExited, StatusWithdrawn:
			// Never exitable; keep looking further down.
		}
	}

	return nil
}

// MarkDepositPending claims a key for a deposit that is being submitted, so a
// batch picking several candidates in a row does not pick the same key twice and
// a concurrent pass does not deposit for it again. Release it with
// ReleaseDepositPending when the submission never reached the chain, or confirm
// it with MarkDepositSubmitted.
func (r *Registry) MarkDepositPending(keyIndex uint64) {
	r.mu.Lock()

	if runtime, ok := r.runtimes[keyIndex]; ok {
		runtime.depositPendingUntil = time.Now().Add(depositPendingTTL)
	}

	r.mu.Unlock()

	r.setTracked(keyIndex, true)
	r.Refresh()
}

// ReleaseDepositPending clears the in-flight marker of a key whose deposit never
// reached the chain, so it becomes a candidate again immediately instead of
// waiting out the TTL.
func (r *Registry) ReleaseDepositPending(keyIndex uint64) {
	r.mu.Lock()

	if runtime, ok := r.runtimes[keyIndex]; ok {
		runtime.depositPendingUntil = time.Time{}
	}

	r.mu.Unlock()

	r.Refresh()
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

// PrimeKeyState derives the key at the given index, applies mutate to a copy of
// its state snapshot and publishes the result. It bypasses the chain refresh, so
// it is only for tests and for priming a key before any beacon state is
// available; the next Refresh overwrites whatever it set.
func (r *Registry) PrimeKeyState(keyIndex uint64, mutate func(*State)) (*Key, error) {
	key, err := r.Key(keyIndex)
	if err != nil {
		return nil, err
	}

	state := *key.State()
	mutate(&state)
	key.state.Store(&state)

	r.setTracked(keyIndex, true)
	r.rebuildBuilderIndex()

	return key, nil
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
