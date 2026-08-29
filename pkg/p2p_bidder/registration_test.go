package p2p_bidder

import (
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/builder_keys"
	"github.com/ethpandaops/buildoor/pkg/config"
)

// A fleet ramping toward its target deposits continuously, and the scheduler
// gates bidding on the reported registration state. Letting one key's in-flight
// deposit pull that state back to pending suppresses bidding from every
// already-active key for the whole ramp.
func TestSetRegistrationPendingKeepsFleetBidding(t *testing.T) {
	newService := func(registry *builder_keys.Registry, state int32) *Service {
		log := logrus.New()
		log.SetLevel(logrus.PanicLevel)

		svc := &Service{registry: registry, log: log}
		svc.registrationState.Store(state)

		return svc
	}

	t.Run("active fleet keeps bidding while another key deposits", func(t *testing.T) {
		registry := newTestKeyRegistry(t, 7)
		require.True(t, registry.AnyActive())

		svc := newService(registry, RegistrationStateRegistered)
		svc.SetRegistrationPending()

		require.Equal(t, RegistrationStateRegistered, svc.GetRegistrationState())
		require.True(t, svc.IsRegistered())
	})

	t.Run("no active key still reports pending", func(t *testing.T) {
		log := logrus.New()
		log.SetLevel(logrus.PanicLevel)

		registry, err := builder_keys.NewRegistry(
			config.NewStaticService(&config.Config{BuilderKeys: config.BuilderKeysConfig{
				TargetCount: 1, DiscoveryGap: 1, MaxIndex: 32,
			}}), testBuilderPrivkey, log)
		require.NoError(t, err)
		require.False(t, registry.AnyActive())

		svc := newService(registry, RegistrationStateUnregistered)
		svc.SetRegistrationPending()

		require.Equal(t, RegistrationStatePending, svc.GetRegistrationState())
	})
}
