package legacybuilder

import (
	"context"
	"fmt"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/wallet"
)

// NonceManager tracks the confirmed on-chain nonce for builder payment transactions.
// Payment txs are included via BuilderTxs (engine API), so they only land on-chain
// if our block is proposed. We always use the confirmed nonce rather than
// speculatively incrementing.
type NonceManager struct {
	wallet    *wallet.Wallet
	baseNonce uint64
	mu        sync.Mutex
	log       logrus.FieldLogger
}

// NewNonceManager creates a new nonce manager.
func NewNonceManager(w *wallet.Wallet, log logrus.FieldLogger) *NonceManager {
	return &NonceManager{
		wallet: w,
		log:    log.WithField("component", "nonce-manager"),
	}
}

// Sync fetches the confirmed nonce from the chain.
func (m *NonceManager) Sync(ctx context.Context) error {
	nonce, err := m.wallet.GetNonce(ctx)
	if err != nil {
		return fmt.Errorf("failed to get nonce: %w", err)
	}

	m.mu.Lock()
	m.baseNonce = nonce
	m.mu.Unlock()

	m.log.WithField("nonce", nonce).Debug("Nonce synced from chain")

	return nil
}

// GetBaseNonce returns the confirmed on-chain nonce.
// Call Sync before this to ensure the nonce is up to date.
func (m *NonceManager) GetBaseNonce() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.baseNonce
}
