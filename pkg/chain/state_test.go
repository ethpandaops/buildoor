package chain

// Tests for NM-08: on the cap-sufficient path, fetchEpochStats used to
// reuse the published validatorIndexCache's backing array in place and
// write new pubkeys into it with no lock held, while
// GetValidatorPubkeyByIndex read the same array under RLock only -- a
// concurrent writer that never locks the per-element writes could produce a
// torn 48-byte BLS pubkey read. The fix (now factored into
// publishValidatorIndexCache, called from fetchEpochStats) always allocates
// a fresh backing array before publishing it under cacheMu, so a reader
// holding a reference to the old array is guaranteed it is never written to
// again. These tests call that real method directly -- fetchEpochStats
// itself needs a live beacon state provider and isn't unit-testable in
// isolation.

import (
	"sync"
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testValidators(pubkeys ...phase0.BLSPubKey) []*phase0.Validator {
	out := make([]*phase0.Validator, len(pubkeys))
	for i, pk := range pubkeys {
		out[i] = &phase0.Validator{PublicKey: pk}
	}

	return out
}

func TestPublishValidatorIndexCache_NeverAliasesThePreviousBackingArray(t *testing.T) {
	s := &service{}

	s.publishValidatorIndexCache(testValidators(make([]phase0.BLSPubKey, 4)...))

	s.cacheMu.RLock()
	firstCache := s.validatorIndexCache
	s.cacheMu.RUnlock()

	// Same length as before: the old reuse-in-place branch would have kept
	// this exact backing array (cap sufficient) rather than allocating a new
	// one.
	s.publishValidatorIndexCache(testValidators(
		phase0.BLSPubKey{0: 1}, phase0.BLSPubKey{0: 2}, phase0.BLSPubKey{0: 3}, phase0.BLSPubKey{0: 4},
	))

	s.cacheMu.RLock()
	secondCache := s.validatorIndexCache
	s.cacheMu.RUnlock()

	require.Len(t, firstCache, 4)
	require.Len(t, secondCache, 4)
	assert.NotSame(t, &firstCache[0], &secondCache[0],
		"NM-08: a refresh must never write into the previously-published backing array")

	// The old array is provably untouched by the second publish (still all
	// zero), confirming a reader that captured a reference before the
	// refresh would never observe a write.
	assert.Equal(t, phase0.BLSPubKey{}, firstCache[0])
}

func TestPublishValidatorIndexCache_ConcurrentRefreshDoesNotRaceGetValidatorPubkeyByIndex(t *testing.T) {
	s := &service{}
	s.publishValidatorIndexCache(testValidators(make([]phase0.BLSPubKey, 4)...))

	var wg sync.WaitGroup

	stop := make(chan struct{})

	wg.Add(1)

	go func() {
		defer wg.Done()

		var b byte

		for {
			select {
			case <-stop:
				return
			default:
			}

			b++

			s.publishValidatorIndexCache(testValidators(
				phase0.BLSPubKey{0: b}, phase0.BLSPubKey{0: b}, phase0.BLSPubKey{0: b}, phase0.BLSPubKey{0: b},
			))
		}
	}()

	// Reader: the real production method.
	for range 5000 {
		_ = s.GetValidatorPubkeyByIndex(1)
	}

	close(stop)
	wg.Wait()
}
