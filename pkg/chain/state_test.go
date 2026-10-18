package chain

import (
	"sync"
	"testing"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/stretchr/testify/require"
)

func TestProposerPubkeysServeTheLookahead(t *testing.T) {
	s := &service{}

	s.publishProposerPubkeys(map[phase0.ValidatorIndex]phase0.BLSPubKey{7: {0x07}})

	pubkey := s.GetValidatorPubkeyByIndex(7)
	require.NotNil(t, pubkey)
	require.Equal(t, phase0.BLSPubKey{0x07}, *pubkey)

	require.Nil(t, s.GetValidatorPubkeyByIndex(8), "no beacon node to ask for other indices")
}

func TestProposerPubkeysRefreshDoesNotRaceReaders(t *testing.T) {
	s := &service{}
	s.publishProposerPubkeys(map[phase0.ValidatorIndex]phase0.BLSPubKey{1: {1}})

	var wg sync.WaitGroup

	stop := make(chan struct{})

	wg.Add(1)

	go func() {
		defer wg.Done()

		for b := byte(0); ; b++ {
			select {
			case <-stop:
				return
			default:
			}

			s.publishProposerPubkeys(map[phase0.ValidatorIndex]phase0.BLSPubKey{1: {b}})
		}
	}()

	for range 5000 {
		require.NotNil(t, s.GetValidatorPubkeyByIndex(1))
	}

	close(stop)
	wg.Wait()
}
