package lifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/wallet"
)

// exitGasLimit is the gas limit for builder exit transactions.
const exitGasLimit = 500000

// ExitService handles builder exits via the EIP-8282 builder exit system contract.
//
// Unlike a validator voluntary exit (a BLS-signed beacon message), a builder exit is
// an execution-layer transaction to the builder exit predeploy carrying only the
// builder pubkey as calldata. The source is the transaction sender (msg.sender), which
// must match the builder's registered execution address — i.e. the funding wallet that
// supplied the withdrawal credentials at deposit time. A per-request queue fee is paid
// as msg.value.
type ExitService struct {
	chainSvc chain.Service
	signer   *signer.BLSSigner
	wallet   *wallet.Wallet
	log      logrus.FieldLogger
}

// NewExitService creates a new exit service.
func NewExitService(
	chainSvc chain.Service,
	blsSigner *signer.BLSSigner,
	w *wallet.Wallet,
	log logrus.FieldLogger,
) *ExitService {
	return &ExitService{
		chainSvc: chainSvc,
		signer:   blsSigner,
		wallet:   w,
		log:      log.WithField("component", "exit-service"),
	}
}

// CreateExit submits a builder exit request transaction for this builder.
//
// Exits always proceed regardless of the configured deposit fee limit (unlike
// deposits/top-ups, which are delayed when the fee is too high), so the operator can
// always withdraw. The queue fee is read from the contract and paid as msg.value.
func (s *ExitService) CreateExit(ctx context.Context) error {
	pubkey := s.signer.PublicKey()

	s.log.WithField("pubkey", fmt.Sprintf("0x%x", pubkey[:])).Info("Creating builder exit")

	calldata, err := BuildBuilderExitCalldata(pubkey[:])
	if err != nil {
		return fmt.Errorf("failed to build exit calldata: %w", err)
	}

	fee, active, err := ReadQueueFee(ctx, s.wallet.GetRPCClient(), BuilderExitContractAddress)
	if err != nil {
		return fmt.Errorf("failed to read exit queue fee: %w", err)
	}

	if !active {
		return ErrContractNotActive
	}

	s.log.WithField("queue_fee_wei", fee.String()).Info("Builder exit prepared")

	receipt, err := s.wallet.SendAndConfirm(
		ctx,
		BuilderExitContractAddress,
		fee,
		calldata,
		exitGasLimit,
		5*time.Minute,
	)
	if err != nil {
		return fmt.Errorf("exit transaction failed: %w", err)
	}

	s.log.WithFields(logrus.Fields{
		"tx_hash":      receipt.TxHash.Hex(),
		"block_number": receipt.BlockNumber.Uint64(),
	}).Info("Exit transaction confirmed")

	return nil
}
