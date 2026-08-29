package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/builder_keys"
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

// ErrUnexpectedFeeResponse is returned when a builder system contract's queue fee
// getter answers with something other than a single 32-byte word.
var ErrUnexpectedFeeResponse = errors.New("unexpected queue fee response")

// ErrBuilderExited is returned when a deposit/top-up targets a builder whose exit has
// been initiated. Exited builders can never be reactivated: the deposit would only top
// up the exited registry entry and be withdrawn back to the wallet by the sweep. The
// pubkey becomes depositable again only once it leaves the builder registry (its index
// is reused by a different builder's deposit).
var ErrBuilderExited = errors.New("builder has exited and cannot be reactivated; deposits are disabled until the pubkey leaves the builder registry")

// isDepositDeferred reports whether err indicates a deposit/top-up that should be
// delayed and retried later (queue fee over the limit, or contract not yet active
// or deployed) rather than treated as a hard failure.
func isDepositDeferred(err error) bool {
	return errors.Is(err, ErrDepositFeeTooHigh) || errors.Is(err, ErrContractNotActive) ||
		errors.Is(err, ErrContractNotDeployed)
}

// depositGasLimit is the gas limit for builder deposit transactions.
const depositGasLimit = 1000000

// depositConfirmTimeout bounds how long a deposit transaction may take to confirm.
const depositConfirmTimeout = 5 * time.Minute

// DepositService handles builder deposits and top-ups via the EIP-8282 builder
// deposit system contract. It is key-agnostic: every operation names the builder
// key it acts on, so one service serves the whole managed key set.
type DepositService struct {
	cfgSvc   *config.Service // settings source; one snapshot per read
	chainSvc chain.Service
	wallet   *wallet.Wallet
	log      logrus.FieldLogger
}

// NewDepositService creates a new deposit service.
func NewDepositService(
	cfgSvc *config.Service,
	chainSvc chain.Service,
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
		cfgSvc:   cfgSvc,
		chainSvc: chainSvc,
		wallet:   w,
		log:      depositLog,
	}, nil
}

// IsBuilderRegistered checks if the given builder key is registered on the
// beacon chain.
func (s *DepositService) IsBuilderRegistered(key *builder_keys.Key) (bool, *BuilderState, error) {
	pubkey := key.Pubkey()

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

// CreateDeposit creates and sends an EIP-8282 builder deposit transaction for the
// given key. It is also used for top-ups (which are simply additional deposits
// for the same pubkey).
//
// Before submitting it reads the contract's current per-request queue fee and, when
// DepositMaxFeeGwei is set, returns ErrDepositFeeTooHigh if the fee exceeds the limit
// so the caller can delay and retry. The transaction value is stake + queue fee.
func (s *DepositService) CreateDeposit(
	ctx context.Context, key *builder_keys.Key, amountGwei uint64,
) error {
	fee, err := s.resolveDepositFee(ctx)
	if err != nil {
		return err
	}

	request, err := s.depositRequest(key, amountGwei, fee)
	if err != nil {
		return err
	}

	return s.sendDepositTransaction(ctx, request)
}

// CreateDeposits submits deposits for several keys as one batch and returns the
// per-key errors (nil for the keys that landed). Batching matters because every
// deposit is serialized on the same funding key: bringing a fleet up one
// confirmed transaction at a time costs a block per key.
//
// The queue fee is read once for the whole batch — it is a property of the
// contract, not of a key — so a fee over the operator's limit defers the whole
// batch with a single error rather than per key.
func (s *DepositService) CreateDeposits(
	ctx context.Context, keys []*builder_keys.Key, amountGwei uint64,
) ([]error, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	fee, err := s.resolveDepositFee(ctx)
	if err != nil {
		return nil, err
	}

	errs := make([]error, len(keys))
	requests := make([]wallet.TxRequest, 0, len(keys))
	// requestKeys maps each built request back to its key, since keys whose
	// request failed to build are not submitted.
	requestKeys := make([]int, 0, len(keys))

	for i, key := range keys {
		request, err := s.depositRequest(key, amountGwei, fee)
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
			errs[keyIndex] = fmt.Errorf("deposit transaction failed: %w", result.Err)
			continue
		}

		s.log.WithFields(logrus.Fields{
			"key":          keys[keyIndex].String(),
			"tx_hash":      result.Receipt.TxHash.Hex(),
			"block_number": result.Receipt.BlockNumber.Uint64(),
		}).Info("Deposit transaction confirmed")
	}

	return errs, nil
}

// depositRequest builds the signed deposit calldata and transaction parameters
// for one key. It performs no I/O beyond the chain-spec reads, so a batch can
// build every request before sending any of them.
func (s *DepositService) depositRequest(
	key *builder_keys.Key, amountGwei uint64, fee *big.Int,
) (wallet.TxRequest, error) {
	pubkey := key.Pubkey()

	// Refuse deposits for an exited builder entry: they cannot reactivate it and
	// are withdrawn back to the wallet, minus gas and the queue fee. A fresh
	// registration (pubkey absent from the registry) passes this check.
	if chain.HasBuilderExited(s.chainSvc.GetBuilderByPubkey(pubkey)) {
		return wallet.TxRequest{}, ErrBuilderExited
	}

	withdrawalCredentials := BuilderWithdrawalCredentials(s.wallet.Address())

	// Compute the builder-deposit signing root (DOMAIN_BUILDER_DEPOSIT,
	// GENESIS_FORK_VERSION) and sign it as a proof-of-possession.
	signingRoot, err := signer.ComputeBuilderDepositSigningRoot(
		pubkey,
		withdrawalCredentials,
		amountGwei,
		s.chainSvc.GetGenesis().GenesisForkVersion,
	)
	if err != nil {
		return wallet.TxRequest{}, fmt.Errorf("failed to compute signing root: %w", err)
	}

	signature, err := key.BLSSigner().Sign(signingRoot[:])
	if err != nil {
		return wallet.TxRequest{}, fmt.Errorf("failed to sign deposit: %w", err)
	}

	calldata, err := BuildBuilderDepositCalldata(pubkey[:], withdrawalCredentials[:], amountGwei, signature[:])
	if err != nil {
		return wallet.TxRequest{}, fmt.Errorf("failed to build deposit calldata: %w", err)
	}

	// msg.value = stake (wei) + queue fee (wei).
	value := new(big.Int).Add(GweiToWei(amountGwei), fee)

	s.log.WithFields(logrus.Fields{
		"key":              key.String(),
		"pubkey":           fmt.Sprintf("0x%x", pubkey[:]),
		"withdrawal_creds": fmt.Sprintf("0x%x", withdrawalCredentials[:]),
		"amount_gwei":      amountGwei,
		"queue_fee_wei":    fee.String(),
		"value_wei":        value.String(),
	}).Info("Builder deposit prepared")

	return wallet.TxRequest{
		To:       BuilderDepositContractAddress,
		Value:    value,
		Data:     calldata,
		GasLimit: depositGasLimit,
	}, nil
}

// CreateTopup creates and sends a top-up transaction (an additional deposit).
func (s *DepositService) CreateTopup(
	ctx context.Context, key *builder_keys.Key, amountGwei uint64,
) error {
	s.log.WithFields(logrus.Fields{
		"key":         key.String(),
		"amount_gwei": amountGwei,
	}).Info("Creating builder top-up")

	return s.CreateDeposit(ctx, key, amountGwei)
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

	if maxFeeGwei := s.cfgSvc.Current().DepositMaxFeeGwei; maxFeeGwei > 0 {
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

// sendDepositTransaction sends one deposit transaction to the builder deposit
// contract.
//
// SendAndConfirm sources a fresh nonce and resolves nonce conflicts/displacement, so
// several instances can share this funding key safely.
func (s *DepositService) sendDepositTransaction(ctx context.Context, request wallet.TxRequest) error {
	receipt, err := s.wallet.SendAndConfirm(
		ctx,
		request.To,
		request.Value,
		request.Data,
		request.GasLimit,
		depositConfirmTimeout,
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
