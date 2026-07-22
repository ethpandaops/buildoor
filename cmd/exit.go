package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/lifecycle"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/wallet"
)

var exitCmd = &cobra.Command{
	Use:   "exit",
	Short: "Exit builder from the network",
	Long:  `Submits a builder exit request to the EIP-8282 builder exit system contract to remove this builder from the builder set.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		// Validate required config
		if cfg.BuilderPrivkey == "" && cfg.BuilderMnemonic == "" {
			return fmt.Errorf("--builder-privkey or --builder-mnemonic is required")
		}

		if cfg.CLClient == "" {
			return fmt.Errorf("--cl-client is required")
		}

		if cfg.ELRPC == "" {
			return fmt.Errorf("--el-rpc is required for builder exit")
		}

		if cfg.WalletPrivkey == "" {
			return fmt.Errorf("--wallet-privkey is required for builder exit")
		}

		// Initialize CL client
		clClient, err := beacon.NewClient(ctx, cfg.CLClient, logger)
		if err != nil {
			return fmt.Errorf("failed to connect to CL: %w", err)
		}
		defer clClient.Close()

		// Initialize EL RPC client
		rpcClient, err := execution.NewClient(ctx, cfg.ELRPC, logger)
		if err != nil {
			return fmt.Errorf("failed to connect to EL RPC: %w", err)
		}
		defer rpcClient.Close()

		// Initialize BLS signer (raw hex key or mnemonic-derived)
		blsSigner, err := signer.NewBuilderSigner(cfg.BuilderPrivkey, cfg.BuilderMnemonic, cfg.BuilderKeyIndex)
		if err != nil {
			return fmt.Errorf("invalid builder key: %w", err)
		}

		pubkey := blsSigner.PublicKey()

		// Initialize wallet (its address is the exit source; must match the builder's
		// registered execution address)
		w, err := wallet.NewWallet(cfg.WalletPrivkey, rpcClient, logger)
		if err != nil {
			return fmt.Errorf("invalid wallet key: %w", err)
		}

		// Get chain spec and genesis
		specData, rawData, err := clClient.GetRawSpecData(ctx)
		if err != nil {
			return fmt.Errorf("failed to get chain spec: %w", err)
		}

		chainSpec, err := chain.ParseChainSpec(specData, rawData)
		if err != nil {
			return fmt.Errorf("failed to parse chain spec: %w", err)
		}

		genesis, err := clClient.GetGenesis(ctx)
		if err != nil {
			return fmt.Errorf("failed to get genesis: %w", err)
		}

		// Initialize chain service
		chainSvc := chain.NewService(cfg, clClient, chainSpec, genesis, logger)
		if err := chainSvc.Start(ctx); err != nil {
			return fmt.Errorf("failed to start chain service: %w", err)
		}
		defer chainSvc.Stop() //nolint:errcheck // cleanup

		// Confirm the builder is registered before exiting
		builderInfo := chainSvc.GetBuilderByPubkey(pubkey)
		if builderInfo == nil {
			return fmt.Errorf("builder not registered")
		}

		logger.WithFields(map[string]any{
			"builder_index": builderInfo.Index,
			"pubkey":        fmt.Sprintf("%x", pubkey[:8]),
		}).Info("Submitting builder exit")

		exitSvc := lifecycle.NewExitService(chainSvc, blsSigner, w, logger)
		if err := exitSvc.CreateExit(ctx); err != nil {
			return fmt.Errorf("failed to submit builder exit: %w", err)
		}

		logger.WithField("builder_index", builderInfo.Index).Info("Builder exit submitted")

		return nil
	},
}

func init() {
	rootCmd.AddCommand(exitCmd)
}
