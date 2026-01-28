package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ethereum/go-ethereum/common"
	"github.com/spf13/cobra"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/epbs"
	"github.com/ethpandaops/buildoor/pkg/lifecycle"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/wallet"
	"github.com/ethpandaops/buildoor/pkg/webui"
	"github.com/ethpandaops/buildoor/pkg/webui/types"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Start the builder",
	Long: `Starts the builder service, connecting to beacon and execution nodes,
and begins building blocks according to configuration.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Validate required config
		if cfg.BuilderPrivkey == "" {
			return fmt.Errorf("--builder-privkey is required")
		}

		if cfg.CLClient == "" {
			return fmt.Errorf("--cl-client is required")
		}

		if cfg.ELEngineAPI == "" {
			return fmt.Errorf("--el-engine-api is required")
		}

		if cfg.ELJWTSecret == "" {
			return fmt.Errorf("--el-jwt-secret is required")
		}

		// 1. Initialize CL client
		logger.Info("Connecting to consensus layer...")

		clClient, err := beacon.NewClient(ctx, cfg.CLClient, logger)
		if err != nil {
			return fmt.Errorf("failed to connect to CL: %w", err)
		}
		defer clClient.Close()

		// 2. Initialize Engine API client (always required for payload building)
		logger.Info("Connecting to execution layer engine API...")

		engineClient, err := engine.NewClient(ctx, cfg.ELEngineAPI, cfg.ELJWTSecret, logger)
		if err != nil {
			return fmt.Errorf("failed to connect to EL engine API: %w", err)
		}
		defer engineClient.Close()

		// 3. Initialize BLS signer
		blsSigner, err := signer.NewBLSSigner(cfg.BuilderPrivkey)
		if err != nil {
			return fmt.Errorf("invalid builder key: %w", err)
		}

		pubkey := blsSigner.PublicKey()
		logger.WithField("pubkey", fmt.Sprintf("%x", pubkey[:8])).Info("Builder key loaded")

		// 4. Initialize RPC client and wallet (if lifecycle enabled)
		var rpcClient *execution.Client

		var w *wallet.Wallet

		if cfg.LifecycleEnabled {
			if cfg.ELRPC == "" {
				return fmt.Errorf("--el-rpc is required when lifecycle is enabled")
			}

			if cfg.WalletPrivkey == "" {
				return fmt.Errorf("--wallet-privkey is required when lifecycle is enabled")
			}

			logger.Info("Connecting to EL RPC for lifecycle management...")

			rpcClient, err = execution.NewClient(ctx, cfg.ELRPC, logger)
			if err != nil {
				return fmt.Errorf("failed to connect to EL RPC: %w", err)
			}
			defer rpcClient.Close()

			w, err = wallet.NewWallet(cfg.WalletPrivkey, rpcClient, logger)
			if err != nil {
				return fmt.Errorf("invalid wallet key: %w", err)
			}

			logger.WithField("wallet", w.Address().Hex()).Info("Wallet loaded")
		}

		// 5. Initialize chain service (epoch-level state management)
		logger.Info("Initializing chain service...")

		chainSpec, err := clClient.GetChainSpec(ctx)
		if err != nil {
			return fmt.Errorf("failed to get chain spec: %w", err)
		}

		genesis, err := clClient.GetGenesis(ctx)
		if err != nil {
			return fmt.Errorf("failed to get genesis: %w", err)
		}

		chainSvc := chain.NewService(clClient, chainSpec, genesis, logger)
		if err := chainSvc.Start(ctx); err != nil {
			return fmt.Errorf("failed to start chain service: %w", err)
		}
		defer chainSvc.Stop() //nolint:errcheck // cleanup

		// 6. Initialize lifecycle manager (if enabled)
		var lifecycleMgr *lifecycle.Manager

		if cfg.LifecycleEnabled {
			lifecycleMgr, err = lifecycle.NewManager(cfg, clClient, chainSvc, blsSigner, w, logger)
			if err != nil {
				return fmt.Errorf("failed to initialize lifecycle: %w", err)
			}

			// Ensure builder is registered
			logger.Info("Checking builder registration...")

			if err := lifecycleMgr.EnsureBuilderRegistered(ctx); err != nil {
				return fmt.Errorf("builder registration failed: %w", err)
			}
		}

		// 7. Initialize builder service (standalone block building)
		logger.Info("Initializing builder service...")

		// Get fee recipient from wallet or use zero address
		var feeRecipient common.Address
		if w != nil {
			feeRecipient = w.Address()
		}

		builderSvc, err := builder.NewService(cfg, clClient, chainSvc, engineClient, feeRecipient, logger)
		if err != nil {
			return fmt.Errorf("failed to initialize builder: %w", err)
		}

		// 8. Initialize ePBS service (if enabled)
		var epbsSvc *epbs.Service

		if cfg.EPBSEnabled {
			logger.Info("Initializing ePBS service...")

			epbsSvc, err = epbs.NewService(&cfg.EPBS, clClient, chainSvc, blsSigner, logger)
			if err != nil {
				return fmt.Errorf("failed to initialize ePBS: %w", err)
			}
		}

		// 9. Start API server (if configured)
		if cfg.APIPort > 0 {
			logger.WithField("port", cfg.APIPort).Info("Starting API server...")

			privkey := strings.TrimPrefix(cfg.BuilderPrivkey, "0x")
			decoded, _ := hex.DecodeString(privkey)
			apiKeyRaw := sha256.Sum256(append([]byte("buildoor-api-key-"), decoded...))
			apiKey := hex.EncodeToString(apiKeyRaw[:])

			webui.StartHttpServer(&types.FrontendConfig{
				Port:     cfg.APIPort,
				Host:     "0.0.0.0",
				SiteName: "Buildoor",
				Debug:    cfg.Debug,
				Pprof:    cfg.Pprof,
				Minify:   !cfg.Debug,

				AuthKey:    apiKey,
				UserHeader: cfg.APIUserHeader,
				TokenKey:   cfg.APITokenKey,
			}, builderSvc, epbsSvc, lifecycleMgr, chainSvc)
		}

		// 10. Start lifecycle manager (if enabled)
		if lifecycleMgr != nil {
			// Connect bid tracker to lifecycle manager for balance tracking
			if epbsSvc != nil {
				lifecycleMgr.SetBidTracker(epbsSvc.GetBidTracker())
			}

			if err := lifecycleMgr.Start(ctx); err != nil {
				return fmt.Errorf("failed to start lifecycle manager: %w", err)
			}
			defer lifecycleMgr.Stop()
		}

		// 11. Start builder service
		logger.Info("Starting builder service...")

		if err := builderSvc.Start(ctx); err != nil {
			return fmt.Errorf("failed to start builder: %w", err)
		}
		defer builderSvc.Stop()

		// 12. Start ePBS service (if enabled)
		if epbsSvc != nil {
			logger.Info("Starting ePBS service...")

			if err := epbsSvc.Start(ctx, builderSvc); err != nil {
				return fmt.Errorf("failed to start ePBS: %w", err)
			}
			defer epbsSvc.Stop()
		}

		logger.Info("Builder is running. Press Ctrl+C to stop.")

		// 13. Wait for shutdown signal
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

		select {
		case sig := <-sigCh:
			logger.WithField("signal", sig.String()).Info("Received shutdown signal")
		case <-ctx.Done():
			logger.Info("Context cancelled")
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
}
