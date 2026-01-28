package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/lifecycle"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/wallet"
)

var depositCmd = &cobra.Command{
	Use:   "deposit",
	Short: "Register builder by creating deposit",
	Long:  `Creates a builder deposit transaction to register this builder on the beacon chain.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		// Validate required config
		if cfg.BuilderPrivkey == "" {
			return fmt.Errorf("--builder-privkey is required")
		}

		if cfg.CLClient == "" {
			return fmt.Errorf("--cl-client is required")
		}

		if cfg.ELRPC == "" {
			return fmt.Errorf("--el-rpc is required for deposit")
		}

		if cfg.WalletPrivkey == "" {
			return fmt.Errorf("--wallet-privkey is required for deposit")
		}

		// Initialize CL client
		clClient, err := beacon.NewClient(ctx, cfg.CLClient, logger)
		if err != nil {
			return fmt.Errorf("failed to connect to CL: %w", err)
		}
		defer clClient.Close()

		// Initialize RPC client
		rpcClient, err := execution.NewClient(ctx, cfg.ELRPC, logger)
		if err != nil {
			return fmt.Errorf("failed to connect to EL RPC: %w", err)
		}
		defer rpcClient.Close()

		// Initialize BLS signer
		blsSigner, err := signer.NewBLSSigner(cfg.BuilderPrivkey)
		if err != nil {
			return fmt.Errorf("invalid builder key: %w", err)
		}

		// Initialize wallet
		w, err := wallet.NewWallet(cfg.WalletPrivkey, rpcClient, logger)
		if err != nil {
			return fmt.Errorf("invalid wallet key: %w", err)
		}

		// Get chain spec and genesis
		chainSpec, err := clClient.GetChainSpec(ctx)
		if err != nil {
			return fmt.Errorf("failed to get chain spec: %w", err)
		}

		genesis, err := clClient.GetGenesis(ctx)
		if err != nil {
			return fmt.Errorf("failed to get genesis: %w", err)
		}

		// Initialize chain service
		chainSvc := chain.NewService(clClient, chainSpec, genesis, logger)
		if err := chainSvc.Start(ctx); err != nil {
			return fmt.Errorf("failed to start chain service: %w", err)
		}
		defer chainSvc.Stop() //nolint:errcheck // cleanup

		// Check if builder already registered
		pubkey := blsSigner.PublicKey()

		builderInfo := chainSvc.GetBuilderByPubkey(pubkey)
		if builderInfo != nil {
			logger.WithField("builder_index", builderInfo.Index).Info("Builder already registered")
			return nil
		}

		// Get deposit amount
		amount, _ := cmd.Flags().GetUint64("amount")
		if amount == 0 {
			amount = cfg.DepositAmount
		}

		waitForInclusion, _ := cmd.Flags().GetBool("wait")
		timeout, _ := cmd.Flags().GetDuration("timeout")

		// Initialize lifecycle manager
		lifecycleMgr, err := lifecycle.NewManager(cfg, clClient, chainSvc, blsSigner, w, logger)
		if err != nil {
			return fmt.Errorf("failed to initialize lifecycle manager: %w", err)
		}

		logger.WithFields(map[string]any{
			"pubkey":      fmt.Sprintf("%x", pubkey[:8]),
			"amount_gwei": amount,
		}).Info("Creating builder deposit")

		// Ensure builder is registered
		if err := lifecycleMgr.EnsureBuilderRegistered(ctx); err != nil {
			return fmt.Errorf("failed to ensure builder registered: %w", err)
		}

		if waitForInclusion {
			logger.Info("Waiting for registration...")

			if err := lifecycleMgr.WaitForRegistration(ctx, timeout); err != nil {
				return fmt.Errorf("registration wait failed: %w", err)
			}

			state := lifecycleMgr.GetBuilderState()
			logger.WithFields(map[string]any{
				"builder_index": state.Index,
				"balance":       state.Balance,
			}).Info("Builder registered successfully")
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(depositCmd)

	depositCmd.Flags().Uint64("amount", 10000000000, "Deposit amount in Gwei")
	depositCmd.Flags().Bool("wait", true, "Wait for deposit to be included")
	depositCmd.Flags().Duration("timeout", 5*time.Minute, "Timeout for waiting")
}
