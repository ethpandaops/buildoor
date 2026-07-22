package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/wallet"
)

// ErrDepositFeeTooHigh is returned when the builder deposit contract's current queue
// fee exceeds the operator's configured limit (DepositMaxFeeGwei). It is a signal to
// delay the deposit/top-up and retry later, not a hard failure.
var ErrDepositFeeTooHigh = errors.New("builder deposit queue fee exceeds configured limit")

// ErrContractNotActive is returned while the builder deposit contract still holds the
// pre-fork excess inhibitor (before GLOAS_FORK_EPOCH), so deposits can't be submitted yet.
var ErrContractNotActive = errors.New("builder deposit contract not active yet")

// ErrContractNotDeployed is returned when a builder system contract has no code at
// its expected address. Before the Amsterdam fork this is normal (the EL injects the
// predeploys at the fork); after the fork it means the network uses different
// addresses than this build expects.
var ErrContractNotDeployed = errors.New("builder system contract not deployed")

// isDepositDeferred reports whether err indicates a deposit/top-up that should be
// delayed and retried later (queue fee over the limit, or contract not yet active
// or deployed) rather than treated as a hard failure.
func isDepositDeferred(err error) bool {
	return errors.Is(err, ErrDepositFeeTooHigh) || errors.Is(err, ErrContractNotActive) ||
		errors.Is(err, ErrContractNotDeployed)
}

// depositGasLimit is the gas limit for builder deposit transactions.
const depositGasLimit = 1000000

// DepositService handles builder deposits and top-ups via the EIP-8282 builder
// deposit system contract.
type DepositService struct {
	cfg      *config.Config
	chainSvc chain.Service
	signer   *signer.BLSSigner
	wallet   *wallet.Wallet
	log      logrus.FieldLogger
}

// NewDepositService creates a new deposit service.
func NewDepositService(
	cfg *config.Config,
	chainSvc chain.Service,
	blsSigner *signer.BLSSigner,
	w *wallet.Wallet,
	log logrus.FieldLogger,
) (*DepositService, error) {
	depositLog := log.WithField("component", "deposit-service")

	// Sync wallet
	if err := w.Sync(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to sync wallet: %w", err)
	}

	depositLog.WithField("deposit_contract", BuilderDepositContractAddress.Hex()).
		Info("Using builder deposit contract")

	return &DepositService{
		cfg:      cfg,
		chainSvc: chainSvc,
		signer:   blsSigner,
		wallet:   w,
		log:      depositLog,
	}, nil
}

// IsBuilderRegistered checks if the builder is registered on the beacon chain.
func (s *DepositService) IsBuilderRegistered(_ context.Context) (bool, *BuilderState, error) {
	pubkey := s.signer.PublicKey()

	info := s.chainSvc.GetBuilderByPubkey(pubkey)
	if info == nil {
		return false, &BuilderState{
			Pubkey:       pubkey[:],
			IsRegistered: false,
		}, nil
	}

	return true, &BuilderState{
		Pubkey:            pubkey[:],
		Index:             info.Index,
		IsRegistered:      true,
		Balance:           info.Balance,
		DepositEpoch:      info.DepositEpoch,
		WithdrawableEpoch: info.WithdrawableEpoch,
	}, nil
}

// CreateDeposit creates and sends an EIP-8282 builder deposit transaction. It is
// also used for top-ups (which are simply additional deposits for the same pubkey).
//
// Before submitting it reads the contract's current per-request queue fee and, when
// DepositMaxFeeGwei is set, returns ErrDepositFeeTooHigh if the fee exceeds the limit
// so the caller can delay and retry. The transaction value is stake + queue fee.
func (s *DepositService) CreateDeposit(ctx context.Context, amountGwei uint64) error {
	s.log.WithField("amount_gwei", amountGwei).Info("Creating builder deposit")

	pubkey := s.signer.PublicKey()
	withdrawalCredentials := BuilderWithdrawalCredentials(s.wallet.Address())

	// Step 1: Compute the builder-deposit signing root (DOMAIN_BUILDER_DEPOSIT,
	// GENESIS_FORK_VERSION) and sign it as a proof-of-possession.
	signingRoot, err := signer.ComputeBuilderDepositSigningRoot(
		pubkey,
		withdrawalCredentials,
		amountGwei,
		s.chainSvc.GetGenesis().GenesisForkVersion,
	)
	if err != nil {
		return fmt.Errorf("failed to compute signing root: %w", err)
	}

	signature, err := s.signer.Sign(signingRoot[:])
	if err != nil {
		return fmt.Errorf("failed to sign deposit: %w", err)
	}

	// Step 2: Build the raw 184-byte request calldata.
	calldata, err := BuildBuilderDepositCalldata(pubkey[:], withdrawalCredentials[:], amountGwei, signature[:])
	if err != nil {
		return fmt.Errorf("failed to build deposit calldata: %w", err)
	}

	// Step 3: Resolve the queue fee and enforce the operator's fee limit.
	fee, err := s.resolveDepositFee(ctx)
	if err != nil {
		return err
	}

	// Step 4: msg.value = stake (wei) + queue fee (wei).
	value := new(big.Int).Add(GweiToWei(amountGwei), fee)

	s.log.WithFields(logrus.Fields{
		"pubkey":           fmt.Sprintf("0x%x", pubkey[:]),
		"withdrawal_creds": fmt.Sprintf("0x%x", withdrawalCredentials[:]),
		"amount_gwei":      amountGwei,
		"queue_fee_wei":    fee.String(),
		"value_wei":        value.String(),
	}).Info("Builder deposit prepared")

	return s.sendDepositTransaction(ctx, calldata, value)
}

// CreateTopup creates and sends a top-up transaction (an additional deposit).
func (s *DepositService) CreateTopup(ctx context.Context, amountGwei uint64) error {
	s.log.WithField("amount_gwei", amountGwei).Info("Creating builder top-up")

	return s.CreateDeposit(ctx, amountGwei)
}

// resolveDepositFee reads the builder deposit contract's current queue fee and
// enforces DepositMaxFeeGwei. It returns ErrContractNotActive before the fork and
// ErrDepositFeeTooHigh when the fee exceeds the configured limit.
func (s *DepositService) resolveDepositFee(ctx context.Context) (*big.Int, error) {
	fee, active, err := ReadQueueFee(ctx, s.wallet.GetRPCClient(), BuilderDepositContractAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to read deposit queue fee: %w", err)
	}

	if !active {
		return nil, ErrContractNotActive
	}

	if maxFeeGwei := s.cfg.DepositMaxFeeGwei; maxFeeGwei > 0 {
		maxFeeWei := GweiToWei(maxFeeGwei)
		if fee.Cmp(maxFeeWei) > 0 {
			s.log.WithFields(logrus.Fields{
				"queue_fee_wei": fee.String(),
				"max_fee_gwei":  maxFeeGwei,
			}).Info("Builder deposit queue fee exceeds limit, delaying")

			return nil, fmt.Errorf("%w: fee %s wei > limit %d gwei", ErrDepositFeeTooHigh, fee.String(), maxFeeGwei)
		}
	}

	return fee, nil
}

// sendDepositTransaction sends the deposit transaction to the builder deposit contract.
//
// SendAndConfirm sources a fresh nonce and resolves nonce conflicts/displacement, so
// several instances can share this funding key safely.
func (s *DepositService) sendDepositTransaction(ctx context.Context, calldata []byte, value *big.Int) error {
	receipt, err := s.wallet.SendAndConfirm(
		ctx,
		BuilderDepositContractAddress,
		value,
		calldata,
		depositGasLimit,
		5*time.Minute,
	)
	if err != nil {
		return fmt.Errorf("deposit transaction failed: %w", err)
	}

	s.log.WithFields(logrus.Fields{
		"tx_hash":      receipt.TxHash.Hex(),
		"block_number": receipt.BlockNumber.Uint64(),
	}).Info("Deposit transaction confirmed")

	return nil
}
