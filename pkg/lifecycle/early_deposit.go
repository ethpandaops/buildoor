package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/wallet"
)

// ErrNoDepositContract is returned when the beacon spec does not advertise a regular
// validator deposit contract address, so early onboarding cannot submit a deposit.
var ErrNoDepositContract = errors.New("no validator deposit contract address in spec")

// depositContractABI is the minimal ABI of the regular validator deposit contract's
// deposit() function (selector 0x22895118). Early onboarding goes through this
// contract rather than the EIP-8282 builder deposit predeploy.
const depositContractABI = `[{"name":"deposit","type":"function","stateMutability":"payable","inputs":[` +
	`{"name":"pubkey","type":"bytes"},` +
	`{"name":"withdrawal_credentials","type":"bytes"},` +
	`{"name":"signature","type":"bytes"},` +
	`{"name":"deposit_data_root","type":"bytes32"}]}]`

// EarlyDepositService submits a pre-Gloas builder onboarding deposit via the regular
// validator deposit contract. Unlike the post-fork builder deposit (EIP-8282 predeploy,
// 0x00 withdrawal prefix, DOMAIN_BUILDER_DEPOSIT), an early deposit uses 0xB0 withdrawal
// credentials and is signed with the validator deposit domain — i.e. it is an ordinary
// validator deposit that sits in the beacon state's pending_deposits queue and is
// converted into a builder at the Gloas fork boundary.
type EarlyDepositService struct {
	cfg        *config.Config
	chainSvc   chain.Service
	signer     *signer.BLSSigner
	wallet     *wallet.Wallet
	depositABI abi.ABI
	log        logrus.FieldLogger
}

// NewEarlyDepositService creates a new early deposit service.
func NewEarlyDepositService(
	cfg *config.Config,
	chainSvc chain.Service,
	blsSigner *signer.BLSSigner,
	w *wallet.Wallet,
	log logrus.FieldLogger,
) (*EarlyDepositService, error) {
	depositABI, err := abi.JSON(strings.NewReader(depositContractABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse deposit contract ABI: %w", err)
	}

	return &EarlyDepositService{
		cfg:        cfg,
		chainSvc:   chainSvc,
		signer:     blsSigner,
		wallet:     w,
		depositABI: depositABI,
		log:        log.WithField("component", "early-deposit-service"),
	}, nil
}

// HasPendingDeposit reports whether this builder's pubkey is already present in the
// beacon state's pending_deposits queue. It is used after a restart to avoid submitting
// a duplicate early deposit while a prior one is still waiting in the queue.
func (s *EarlyDepositService) HasPendingDeposit() bool {
	stats := s.chainSvc.GetCurrentEpochStats()
	if stats == nil {
		return false
	}

	pubkey := s.signer.PublicKey()
	for i := range stats.PendingDeposits {
		if stats.PendingDeposits[i].Pubkey == pubkey {
			return true
		}
	}

	return false
}

// CreateEarlyDeposit builds, signs and sends a validator deposit for this builder via
// the regular deposit contract. The deposit uses 0xB0 (BUILDER_WITHDRAWAL_PREFIX) withdrawal
// credentials pointing at the funding wallet and is signed with the validator deposit
// domain over GENESIS_FORK_VERSION.
func (s *EarlyDepositService) CreateEarlyDeposit(ctx context.Context, amountGwei uint64) error {
	depositContract := s.chainSvc.GetChainSpec().DepositContractAddress
	if depositContract == nil {
		return ErrNoDepositContract
	}

	pubkey := s.signer.PublicKey()
	withdrawalCredentials := ValidatorWithdrawalCredentials(s.wallet.Address())
	genesisForkVersion := s.chainSvc.GetGenesis().GenesisForkVersion

	// Sign the deposit message with the validator deposit domain (DOMAIN_DEPOSIT).
	signingRoot, err := signer.ComputeDepositSigningRoot(pubkey, withdrawalCredentials, amountGwei, genesisForkVersion)
	if err != nil {
		return fmt.Errorf("failed to compute deposit signing root: %w", err)
	}

	signature, err := s.signer.Sign(signingRoot[:])
	if err != nil {
		return fmt.Errorf("failed to sign early deposit: %w", err)
	}

	depositDataRoot, err := signer.ComputeDepositDataRoot(pubkey, withdrawalCredentials, amountGwei, signature)
	if err != nil {
		return fmt.Errorf("failed to compute deposit data root: %w", err)
	}

	calldata, err := s.depositABI.Pack(
		"deposit",
		pubkey[:],
		withdrawalCredentials[:],
		signature[:],
		[32]byte(depositDataRoot),
	)
	if err != nil {
		return fmt.Errorf("failed to encode deposit calldata: %w", err)
	}

	value := GweiToWei(amountGwei)

	s.log.WithFields(logrus.Fields{
		"pubkey":           fmt.Sprintf("0x%x", pubkey[:]),
		"withdrawal_creds": fmt.Sprintf("0x%x", withdrawalCredentials[:]),
		"deposit_contract": depositContract.Hex(),
		"amount_gwei":      amountGwei,
		"value_wei":        value.String(),
	}).Info("Early builder deposit prepared (regular deposit contract)")

	receipt, err := s.wallet.SendAndConfirm(ctx, *depositContract, value, calldata, depositGasLimit, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("early deposit transaction failed: %w", err)
	}

	s.log.WithFields(logrus.Fields{
		"tx_hash":      receipt.TxHash.Hex(),
		"block_number": receipt.BlockNumber.Uint64(),
	}).Info("Early deposit transaction confirmed")

	return nil
}
