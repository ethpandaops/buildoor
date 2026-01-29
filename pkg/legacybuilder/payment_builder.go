package legacybuilder

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/wallet"
)

// PaymentBuilder creates builder payment transactions.
type PaymentBuilder struct {
	wallet *wallet.Wallet
	log    logrus.FieldLogger
}

// NewPaymentBuilder creates a new payment builder.
func NewPaymentBuilder(w *wallet.Wallet, log logrus.FieldLogger) *PaymentBuilder {
	return &PaymentBuilder{
		wallet: w,
		log:    log.WithField("component", "payment-builder"),
	}
}

// CreatePaymentTx creates and signs a simple ETH transfer from the builder wallet
// to the proposer's fee recipient address.
func (b *PaymentBuilder) CreatePaymentTx(
	feeRecipient common.Address,
	amount *big.Int,
	nonce uint64,
	chainID *big.Int,
	gasLimit uint64,
	gasFeeCap *big.Int,
	gasTipCap *big.Int,
) (*types.Transaction, error) {
	if amount.Sign() <= 0 {
		return nil, fmt.Errorf("payment amount must be positive")
	}

	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		GasTipCap: gasTipCap,
		GasFeeCap: gasFeeCap,
		Gas:       gasLimit,
		To:        &feeRecipient,
		Value:     amount,
	})

	signedTx, err := b.wallet.SignTransaction(tx)
	if err != nil {
		return nil, fmt.Errorf("failed to sign payment tx: %w", err)
	}

	b.log.WithFields(logrus.Fields{
		"to":     feeRecipient.Hex(),
		"amount": amount.String(),
		"nonce":  nonce,
	}).Debug("Payment transaction created")

	return signedTx, nil
}
