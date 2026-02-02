package chain

import (
	"context"
	"sync"
	"time"

	"github.com/attestantio/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
)

// ValidatorIndexCache caches validator index→pubkey and refreshes every epoch from beacon state.
// Used by payload building to resolve proposer index to pubkey without querying beacon state on every build.
type ValidatorIndexCache struct {
	clClient      *beacon.Client
	chainSvc      Service
	log           logrus.FieldLogger
	mu            sync.RWMutex
	indexToPubkey map[phase0.ValidatorIndex]phase0.BLSPubKey
	stopCh        chan struct{}
	wg            sync.WaitGroup
}

// NewValidatorIndexCache creates a cache that will be refreshed every epoch.
func NewValidatorIndexCache(clClient *beacon.Client, chainSvc Service, log logrus.FieldLogger) *ValidatorIndexCache {
	return &ValidatorIndexCache{
		clClient:      clClient,
		chainSvc:      chainSvc,
		log:           log.WithField("component", "validator-index-cache"),
		indexToPubkey: make(map[phase0.ValidatorIndex]phase0.BLSPubKey),
		stopCh:        make(chan struct{}),
	}
}

// Start runs an immediate refresh and then refreshes every epoch.
func (c *ValidatorIndexCache) Start(ctx context.Context) error {
	c.refresh(ctx)
	c.wg.Add(1)
	go c.run(ctx)
	c.log.Info("Validator index cache started")
	return nil
}

// Stop stops the refresh loop.
func (c *ValidatorIndexCache) Stop() {
	close(c.stopCh)
	c.wg.Wait()
	c.log.Info("Validator index cache stopped")
}

// Get returns the pubkey for the given validator index, or false if not in cache.
func (c *ValidatorIndexCache) Get(index phase0.ValidatorIndex) (phase0.BLSPubKey, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	pk, ok := c.indexToPubkey[index]
	return pk, ok
}

func (c *ValidatorIndexCache) refresh(ctx context.Context) {
	m, err := c.clClient.GetValidatorIndexToPubkeyMap(ctx, "head")
	if err != nil {
		c.log.WithError(err).Warn("Failed to refresh validator index cache")
		return
	}
	c.mu.Lock()
	c.indexToPubkey = m
	c.mu.Unlock()
	c.log.WithField("validators", len(m)).Debug("Refreshed validator index cache")
}

func (c *ValidatorIndexCache) run(ctx context.Context) {
	defer c.wg.Done()
	spec := c.chainSvc.GetChainSpec()
	epochDuration := time.Duration(spec.SlotsPerEpoch) * spec.SecondsPerSlot

	for {
		epoch, err := c.chainSvc.GetCurrentEpoch(ctx)
		if err != nil {
			c.log.WithError(err).Debug("Failed to get current epoch for cache refresh")
			select {
			case <-ctx.Done():
				return
			case <-c.stopCh:
				return
			case <-time.After(10 * time.Second):
				continue
			}
		}

		nextEpochSlot := phase0.Slot(uint64(epoch+1) * spec.SlotsPerEpoch)
		nextEpochTime := c.chainSvc.SlotToTime(nextEpochSlot)
		delay := time.Until(nextEpochTime)
		if delay < 0 {
			delay = 0
		}
		if delay > epochDuration {
			delay = epochDuration
		}

		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-time.After(delay):
			c.refresh(ctx)
		}
	}
}
