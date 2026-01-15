package lifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/contracts"
	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/wallet"
)

// DepositContractAddress is the default builder deposit contract address.
// This should be fetched from chain spec in production.
var DepositContractAddress = common.HexToAddress("0x00000000219ab540356cbb839cbe05303d7705fa")

// DepositService handles builder deposits and top-ups.
type DepositService struct {
	cfg       *builder.Config
	clClient  *beacon.Client
	signer    *signer.BLSSigner
	wallet    *wallet.Wallet
	contract  *contracts.BuilderDepositContract
	chainSpec *beacon.ChainSpec
	genesis   *beacon.Genesis
	log       logrus.FieldLogger
}

// NewDepositService creates a new deposit service.
func NewDepositService(
	cfg *builder.Config,
	clClient *beacon.Client,
	blsSigner *signer.BLSSigner,
	w *wallet.Wallet,
	log logrus.FieldLogger,
) (*DepositService, error) {
	depositLog := log.WithField("component", "deposit-service")

	// Sync wallet
	if err := w.Sync(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to sync wallet: %w", err)
	}

	// Get chain spec
	chainSpec, err := clClient.GetChainSpec(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get chain spec: %w", err)
	}

	genesis, err := clClient.GetGenesis(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get genesis: %w", err)
	}

	// Initialize deposit contract
	// TODO: Get actual deposit contract address from chain spec
	contract, err := contracts.NewBuilderDepositContract(DepositContractAddress, w.GetRPCClient())
	if err != nil {
		return nil, fmt.Errorf("failed to initialize deposit contract: %w", err)
	}

	return &DepositService{
		cfg:       cfg,
		clClient:  clClient,
		signer:    blsSigner,
		wallet:    w,
		contract:  contract,
		chainSpec: chainSpec,
		genesis:   genesis,
		log:       depositLog,
	}, nil
}

// IsBuilderRegistered checks if the builder is registered on the beacon chain.
func (s *DepositService) IsBuilderRegistered(ctx context.Context) (bool, *builder.BuilderState, error) {
	pubkey := s.signer.PublicKey()

	// Ensure builders are loaded (idempotent - safe to call multiple times)
	if _, err := s.clClient.LoadBuilders(ctx); err != nil {
		return false, nil, fmt.Errorf("failed to load builders: %w", err)
	}

	info, err := s.clClient.GetBuilderByPubkey(ctx, pubkey)
	if err != nil {
		return false, nil, fmt.Errorf("failed to get builder info: %w", err)
	}

	if info == nil {
		return false, &builder.BuilderState{
			Pubkey:       pubkey[:],
			IsRegistered: false,
		}, nil
	}

	return true, &builder.BuilderState{
		Pubkey:            pubkey[:],
		Index:             info.Index,
		IsRegistered:      true,
		Balance:           info.Balance,
		DepositEpoch:      info.DepositEpoch,
		WithdrawableEpoch: info.WithdrawableEpoch,
	}, nil
}

// CreateDeposit creates and sends a builder deposit transaction.
func (s *DepositService) CreateDeposit(ctx context.Context, amountGwei uint64) error {
	s.log.WithField("amount_gwei", amountGwei).Info("Creating builder deposit")

	pubkey := s.signer.PublicKey()
	withdrawalCredentials := contracts.BuildWithdrawalCredentials(s.wallet.Address())

	// Step 1: Compute signing root for DepositMessage (pubkey, wc, amount)
	// Use GENESIS_FORK_VERSION from genesis data (required by spec)
	signingRoot, err := signer.ComputeDepositSigningRoot(
		pubkey,
		withdrawalCredentials,
		amountGwei,
		s.genesis.GenesisForkVersion,
	)
	if err != nil {
		return fmt.Errorf("failed to compute signing root: %w", err)
	}

	// Step 2: Sign the deposit message
	signature, err := s.signer.Sign(signingRoot[:])
	if err != nil {
		return fmt.Errorf("failed to sign deposit: %w", err)
	}

	// Step 3: Compute deposit data root (includes signature)
	depositDataRoot, err := signer.ComputeDepositDataRoot(
		pubkey,
		withdrawalCredentials,
		amountGwei,
		signature,
	)
	if err != nil {
		return fmt.Errorf("failed to compute deposit data root: %w", err)
	}

	s.log.WithFields(logrus.Fields{
		"pubkey":               fmt.Sprintf("0x%x", pubkey[:]),
		"withdrawal_creds":     fmt.Sprintf("0x%x", withdrawalCredentials[:]),
		"amount_gwei":          amountGwei,
		"genesis_fork_version": fmt.Sprintf("0x%x", s.genesis.GenesisForkVersion[:]),
		"signing_root":         fmt.Sprintf("0x%x", signingRoot[:]),
		"signature":            fmt.Sprintf("0x%x", signature[:]),
		"deposit_data_root":    fmt.Sprintf("0x%x", depositDataRoot[:]),
	}).Info("Deposit data prepared")

	// Step 4: Send transaction
	return s.sendDepositTransaction(ctx, pubkey[:], withdrawalCredentials, signature[:], depositDataRoot, amountGwei)
}

// CreateTopup creates and sends a top-up transaction.
func (s *DepositService) CreateTopup(ctx context.Context, amountGwei uint64) error {
	s.log.WithField("amount_gwei", amountGwei).Info("Creating builder top-up")

	// Top-up uses the same deposit function
	return s.CreateDeposit(ctx, amountGwei)
}

// sendDepositTransaction sends the deposit transaction to the deposit contract.
func (s *DepositService) sendDepositTransaction(
	ctx context.Context,
	pubkey []byte,
	withdrawalCredentials [32]byte,
	signature []byte,
	depositDataRoot [32]byte,
	amountGwei uint64,
) error {
	// Build transaction data
	txData, err := s.contract.Deposit(
		pubkey,
		withdrawalCredentials[:],
		signature,
		depositDataRoot,
		contracts.GweiToWei(amountGwei),
	)
	if err != nil {
		return fmt.Errorf("failed to build deposit tx data: %w", err)
	}

	// Send transaction
	tx, err := s.wallet.BuildAndSend(
		ctx,
		s.contract.Address(),
		contracts.GweiToWei(amountGwei),
		txData,
		400000, // Gas limit
	)
	if err != nil {
		return fmt.Errorf("failed to send deposit transaction: %w", err)
	}

	s.log.WithField("tx_hash", tx.Hash().Hex()).Info("Deposit transaction sent")

	// Wait for confirmation
	receipt, err := s.wallet.Await(ctx, tx.Hash(), 5*time.Minute)
	if err != nil {
		return fmt.Errorf("deposit transaction failed: %w", err)
	}

	s.log.WithFields(logrus.Fields{
		"tx_hash":      tx.Hash().Hex(),
		"block_number": receipt.BlockNumber.Uint64(),
	}).Info("Deposit transaction confirmed")

	return nil
}
