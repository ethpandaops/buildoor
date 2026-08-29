package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder_keys"
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
// validator deposit contract. Both paths use 0xB0 withdrawal credentials, but unlike
// the post-fork builder deposit (EIP-8282 predeploy, DOMAIN_BUILDER_DEPOSIT), an early
// deposit is signed with the validator deposit domain — i.e. it is an ordinary
// validator deposit that sits in the beacon state's pending_deposits queue and is
// converted into a builder at the Gloas fork boundary.
type EarlyDepositService struct {
	cfgSvc     *config.Service // settings source; one snapshot per read
	chainSvc   chain.Service
	wallet     *wallet.Wallet
	depositABI abi.ABI
	log        logrus.FieldLogger
}

// NewEarlyDepositService creates a new early deposit service.
func NewEarlyDepositService(
	cfgSvc *config.Service,
	chainSvc chain.Service,
	w *wallet.Wallet,
	log logrus.FieldLogger,
) (*EarlyDepositService, error) {
	depositABI, err := abi.JSON(strings.NewReader(depositContractABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse deposit contract ABI: %w", err)
	}

	return &EarlyDepositService{
		cfgSvc:     cfgSvc,
		chainSvc:   chainSvc,
		wallet:     w,
		depositABI: depositABI,
		log:        log.WithField("component", "early-deposit-service"),
	}, nil
}

// HasPendingDeposit reports whether the key's pubkey is already present in the
// beacon state's pending_deposits queue. It is used after a restart to avoid submitting
// a duplicate early deposit while a prior one is still waiting in the queue.
func (s *EarlyDepositService) HasPendingDeposit(key *builder_keys.Key) bool {
	stats := s.chainSvc.GetCurrentEpochStats()
	if stats == nil {
		return false
	}

	pubkey := key.Pubkey()
	for i := range stats.PendingDeposits {
		if stats.PendingDeposits[i].Pubkey == pubkey {
			return true
		}
	}

	return false
}

// CreateEarlyDeposits builds, signs and sends validator deposits for the given keys
// as one batch, returning the per-key errors (nil for the keys that landed).
//
// The deposits use 0xB0 (BUILDER_WITHDRAWAL_PREFIX) withdrawal credentials pointing at
// the funding wallet and are signed with the validator deposit domain over
// GENESIS_FORK_VERSION. They do not race each other: they sit in the pending-deposit
// queue together and the Gloas transition converts them all.
func (s *EarlyDepositService) CreateEarlyDeposits(
	ctx context.Context, keys []*builder_keys.Key, amountGwei uint64,
) ([]error, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	depositContract := s.chainSvc.GetChainSpec().DepositContractAddress
	if depositContract == nil {
		return nil, ErrNoDepositContract
	}

	errs := make([]error, len(keys))
	requests := make([]wallet.TxRequest, 0, len(keys))
	// requestKeys maps each built request back to its key, since keys whose
	// request failed to build are not submitted.
	requestKeys := make([]int, 0, len(keys))

	for i, key := range keys {
		request, err := s.earlyDepositRequest(key, amountGwei, *depositContract)
		if err != nil {
			errs[i] = err
			continue
		}

		requests = append(requests, request)
		requestKeys = append(requestKeys, i)
	}

	for _, result := range s.wallet.SendBatchAndConfirm(ctx, requests, depositConfirmTimeout) {
		keyIndex := requestKeys[result.Index]

		if result.Err != nil {
			errs[keyIndex] = fmt.Errorf("early deposit transaction failed: %w", result.Err)
			continue
		}

		s.log.WithFields(logrus.Fields{
			"key":          keys[keyIndex].String(),
			"tx_hash":      result.Receipt.TxHash.Hex(),
			"block_number": result.Receipt.BlockNumber.Uint64(),
		}).Info("Early deposit transaction confirmed")
	}

	return errs, nil
}

// earlyDepositRequest builds the signed deposit calldata and transaction
// parameters for one key. It performs no I/O, so a batch can build every request
// before sending any of them.
func (s *EarlyDepositService) earlyDepositRequest(
	key *builder_keys.Key, amountGwei uint64, depositContract common.Address,
) (wallet.TxRequest, error) {
	pubkey := key.Pubkey()
	withdrawalCredentials := ValidatorWithdrawalCredentials(s.wallet.Address())
	genesisForkVersion := s.chainSvc.GetGenesis().GenesisForkVersion

	// Sign the deposit message with the validator deposit domain (DOMAIN_DEPOSIT).
	signingRoot, err := signer.ComputeDepositSigningRoot(pubkey, withdrawalCredentials, amountGwei, genesisForkVersion)
	if err != nil {
		return wallet.TxRequest{}, fmt.Errorf("failed to compute deposit signing root: %w", err)
	}

	signature, err := key.BLSSigner().Sign(signingRoot[:])
	if err != nil {
		return wallet.TxRequest{}, fmt.Errorf("failed to sign early deposit: %w", err)
	}

	depositDataRoot, err := signer.ComputeDepositDataRoot(pubkey, withdrawalCredentials, amountGwei, signature)
	if err != nil {
		return wallet.TxRequest{}, fmt.Errorf("failed to compute deposit data root: %w", err)
	}

	calldata, err := s.depositABI.Pack(
		"deposit",
		pubkey[:],
		withdrawalCredentials[:],
		signature[:],
		[32]byte(depositDataRoot),
	)
	if err != nil {
		return wallet.TxRequest{}, fmt.Errorf("failed to encode deposit calldata: %w", err)
	}

	value := GweiToWei(amountGwei)

	s.log.WithFields(logrus.Fields{
		"key":              key.String(),
		"pubkey":           fmt.Sprintf("0x%x", pubkey[:]),
		"withdrawal_creds": fmt.Sprintf("0x%x", withdrawalCredentials[:]),
		"deposit_contract": depositContract.Hex(),
		"amount_gwei":      amountGwei,
		"value_wei":        value.String(),
	}).Info("Early builder deposit prepared (regular deposit contract)")

	return wallet.TxRequest{
		To:       depositContract,
		Value:    value,
		Data:     calldata,
		GasLimit: depositGasLimit,
	}, nil
}
