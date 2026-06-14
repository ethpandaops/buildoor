package cmd

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/builderapi"
	"github.com/ethpandaops/buildoor/pkg/builderapi/validators"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/epbs"
	"github.com/ethpandaops/buildoor/pkg/epbs/lifecycle"
	"github.com/ethpandaops/buildoor/pkg/proposerpreferences"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/rpc/engine"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
	"github.com/ethpandaops/buildoor/pkg/settings"
	"github.com/ethpandaops/buildoor/pkg/signer"
	"github.com/ethpandaops/buildoor/pkg/validatorranges"
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
		if cfg.BuilderPrivkey == "" && cfg.BuilderMnemonic == "" {
			return fmt.Errorf("--builder-privkey or --builder-mnemonic is required")
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

		// 3. Initialize BLS signer (raw hex key or mnemonic-derived)
		blsSigner, err := signer.NewBuilderSigner(cfg.BuilderPrivkey, cfg.BuilderMnemonic, cfg.BuilderKeyIndex)
		if err != nil {
			return fmt.Errorf("invalid builder key: %w", err)
		}

		pubkey := blsSigner.PublicKey()
		logger.WithField("pubkey", fmt.Sprintf("%x", pubkey[:8])).Info("Builder key loaded")

		// 4. Initialize RPC client and wallet (if lifecycle enabled)
		var rpcClient *execution.Client

		var w *wallet.Wallet

		// Initialize RPC client and wallet when prerequisites are available.
		// This makes lifecycle management available for on-the-fly toggling
		// even when not enabled at startup via --lifecycle.
		lifecycleAvailable := cfg.ELRPC != "" && cfg.WalletPrivkey != ""

		if cfg.LifecycleEnabled && !lifecycleAvailable {
			return fmt.Errorf("--el-rpc and --wallet-privkey are required when lifecycle is enabled")
		}

		if lifecycleAvailable {
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

		// 5. Fetch chain spec & genesis (wait for the beacon node), then apply
		// slot-time timing defaults. Retry until the beacon node is ready.
		logger.Info("Waiting for beacon node to serve chain spec and genesis...")

		var chainSpec *beacon.ChainSpec

		for {
			chainSpec, err = clClient.GetChainSpec(ctx)
			if err == nil {
				break
			}

			logger.WithError(err).Warn("Beacon node not ready, retrying in 5s...")

			select {
			case <-ctx.Done():
				return fmt.Errorf("failed to get chain spec: %w", ctx.Err())
			case <-time.After(5 * time.Second):
			}
		}

		var genesis *beacon.Genesis

		for {
			genesis, err = clClient.GetGenesis(ctx)
			if err == nil {
				break
			}

			logger.WithError(err).Warn("Beacon node genesis not available, retrying in 5s...")

			select {
			case <-ctx.Done():
				return fmt.Errorf("failed to get genesis: %w", ctx.Err())
			case <-time.After(5 * time.Second):
			}
		}

		// Configure the global dynssz instance with this network's spec so the
		// go-eth2-client SSZ codecs (block.Root(), envelope HashTreeRoot, ...)
		// compute correct hash-tree-roots on non-mainnet presets (e.g. minimal).
		if err := clClient.InitGlobalSSZSpecs(ctx); err != nil {
			return fmt.Errorf("failed to init global SSZ specs: %w", err)
		}

		// Apply slot-relative timing defaults now that we know the slot duration
		slotTimeMs := chainSpec.SecondsPerSlot.Milliseconds()
		cfg.ApplySlotDefaults(slotTimeMs)

		logger.WithFields(logrus.Fields{
			"slot_time_ms":       slotTimeMs,
			"build_start_time":   cfg.EPBS.BuildStartTime,
			"payload_build_time": cfg.PayloadBuildTime,
			"bid_start_time":     cfg.EPBS.BidStartTime,
			"bid_end_time":       cfg.EPBS.BidEndTime,
		}).Info("Timing defaults applied")

		// 6. Open the optional state-db and build the central settings service.
		// The settings service applies persisted UI overrides (and detects CLI
		// changes) into cfg in place BEFORE any service reads it, so every
		// module starts from the effective configuration.
		stateDB := db.NewDatabase(&db.Config{File: cfg.StateDBPath}, logger)
		if err := stateDB.Init(); err != nil {
			return fmt.Errorf("failed to init state-db: %w", err)
		}
		defer stateDB.Close() //nolint:errcheck // cleanup

		defaults := config.DefaultConfig()
		defaults.ApplySlotDefaults(slotTimeMs)

		// Only operator-supplied keys (flag/env/config) form the CLI layer.
		supplied := make(map[string]bool)
		for _, f := range settings.Fields() {
			supplied[f.Key] = v.IsSet(f.FlagKey)
		}

		settingsSvc, err := settings.New(cfg, defaults, supplied, stateDB, logger)
		if err != nil {
			return fmt.Errorf("failed to init settings service: %w", err)
		}

		// 7. Start chain service (epoch-level state management)
		chainSvc := chain.NewService(clClient, chainSpec, genesis, logger)
		if err := chainSvc.Start(ctx); err != nil {
			return fmt.Errorf("failed to start chain service: %w", err)
		}
		defer chainSvc.Stop() //nolint:errcheck // cleanup

		// 8. Initialize lifecycle manager (if prerequisites available)
		var lifecycleMgr *lifecycle.Manager

		if lifecycleAvailable {
			lifecycleMgr, err = lifecycle.NewManager(cfg, clClient, chainSvc, blsSigner, w, logger)
			if err != nil {
				return fmt.Errorf("failed to initialize lifecycle: %w", err)
			}

			lifecycleMgr.SetEnabled(cfg.LifecycleEnabled)
		}

		// 9. Initialize builder service (standalone block building)
		logger.Info("Initializing builder service...")

		// Get fee recipient from wallet or use default address
		feeRecipient := common.HexToAddress("0x8943545177806ED17B9F23F0a21ee5948eCaa776")
		if w != nil {
			feeRecipient = w.Address()
		}

		// Validator store and index cache when Builder API is available (port > 0)
		// (fee recipient from registrations; cache avoids beacon state lookup every build)
		validatorIndexCache := chain.NewValidatorIndexCache(clClient, chainSvc, logger)
		if err := validatorIndexCache.Start(ctx); err != nil {
			return fmt.Errorf("failed to start validator index cache: %w", err)
		}
		defer validatorIndexCache.Stop()

		var validatorStore *validators.Store
		builderAPIAvailable := cfg.APIPort > 0
		if builderAPIAvailable {
			validatorStore = validators.NewStore()
			validatorStore.SetStateDB(stateDB, logger)
		}
		builderSvc, err := builder.NewService(cfg, clClient, chainSvc, engineClient, feeRecipient, validatorStore, validatorIndexCache, logger)
		if err != nil {
			return fmt.Errorf("failed to initialize builder: %w", err)
		}

		// 10. Initialize ePBS service (if Gloas fork is scheduled)
		var epbsSvc *epbs.Service
		epbsAvailable := chainSpec.GloasForkEpoch != nil && *chainSpec.GloasForkEpoch < math.MaxUint64

		if chainSpec.GloasForkEpoch != nil {
			logger.WithField("gloas_fork_epoch", *chainSpec.GloasForkEpoch).Info("Gloas fork epoch detected")
		} else {
			logger.Info("Gloas fork epoch not found in chain spec, ePBS not available")
		}

		if epbsAvailable {
			logger.Info("Initializing ePBS service...")

			epbsSvc, err = epbs.NewService(&cfg.EPBS, clClient, chainSvc, blsSigner, logger)
			if err != nil {
				return fmt.Errorf("failed to initialize ePBS: %w", err)
			}

			epbsSvc.SetEnabled(cfg.EPBSEnabled)
			epbsSvc.SetStateDB(stateDB)
		}

		// 11. Initialize Builder API server (routes served on --api-port via the shared server)
		var builderAPISrv *builderapi.Server

		if builderAPIAvailable {
			logger.Info("Initializing Builder API server...")

			// Get genesis parameters from beacon client
			g := chainSvc.GetGenesis()
			if g == nil {
				return fmt.Errorf("failed to get genesis from beacon node")
			}

			genesisForkVersion := g.GenesisForkVersion
			genesisValidatorsRoot := g.GenesisValidatorsRoot

			logger.WithFields(logrus.Fields{
				"genesis_fork_version":    fmt.Sprintf("0x%x", genesisForkVersion[:]),
				"genesis_validators_root": fmt.Sprintf("0x%x", genesisValidatorsRoot[:]),
			}).Info("Using genesis parameters from beacon node")

			// Get current fork version from chain service (for chain-specific verification)
			var forkVersion phase0.Version
			if fv, err := chainSvc.GetForkVersion(ctx); err == nil {
				forkVersion = fv
			}

			builderAPISrv = builderapi.NewServer(&cfg.BuilderAPI, logger, builderSvc, blsSigner, validatorStore, genesisForkVersion, forkVersion, genesisValidatorsRoot)
			builderAPISrv.SetFuluPublisher(clClient)
			builderAPISrv.SetCLClient(clClient)
			builderAPISrv.SetEnabled(cfg.BuilderAPIEnabled)
			builderAPISrv.SetChainService(chainSvc)
			if err := builderAPISrv.Start(ctx); err != nil {
				return fmt.Errorf("failed to start Builder API server: %w", err)
			}
			builderAPISrv.SetStateDB(stateDB)
		}

		// 12. Initialize proposer preferences service early (not started yet) so it can be passed to the API handler.
		var propPrefSvc *proposerpreferences.Service

		if epbsAvailable {
			propPrefSvc = proposerpreferences.NewService(clClient, logger)
			propPrefSvc.GetCache().SetStateDB(stateDB, logger)
			builderSvc.SetProposerPreferencesCache(propPrefSvc.GetCache())
			if builderAPISrv != nil {
				builderAPISrv.SetProposerPreferencesCache(propPrefSvc.GetCache())
			}
			chainSvc.SetProposerPreferencesCache(propPrefSvc.GetCache())
		}

		// 13. Initialize and start validator ranges resolver.
		valRanges := validatorranges.NewResolver(&cfg.ValidatorRanges, logger)
		valRanges.Start(ctx)

		// 14. Register settings OnChange subscribers: route changes through the
		// modules. The settings service has already mutated cfg in place; these
		// callbacks trigger module-side resets (schedule counters, scheduler) and
		// sync the enable flags.
		settingsSvc.OnChange(func() {
			if err := builderSvc.UpdateConfig(cfg); err != nil {
				logger.WithError(err).Warn("failed to apply builder config update")
			}

			if epbsSvc != nil {
				epbsSvc.UpdateConfig(&cfg.EPBS)
				epbsSvc.SetEnabled(cfg.EPBSEnabled)
			}

			if builderAPISrv != nil {
				builderAPISrv.SetEnabled(cfg.BuilderAPIEnabled)
			}

			if lifecycleMgr != nil {
				lifecycleMgr.SetEnabled(cfg.LifecycleEnabled)
			}
		})

		// 15. Start WebUI/API server (if configured)
		if cfg.APIPort > 0 {
			logger.WithField("port", cfg.APIPort).Info("Starting API server...")

			apiHandler := webui.StartHttpServer(&types.FrontendConfig{
				Port:     cfg.APIPort,
				Host:     "0.0.0.0",
				SiteName: "Buildoor",
				Debug:    cfg.Debug,
				Pprof:    cfg.Pprof,
				Minify:   !cfg.Debug,

				AuthProviderURL: cfg.AuthProviderURL,
				InjectHeadHTML:  cfg.InjectHeadHTML,
				OverviewURL:     cfg.OverviewURL,
			}, settingsSvc, stateDB, builderSvc, epbsSvc, lifecycleMgr, chainSvc, validatorStore, builderAPISrv, propPrefSvc, valRanges)

			// Connect Builder API server to event stream (if both are enabled)
			if builderAPISrv != nil && apiHandler != nil {
				eventStreamMgr := apiHandler.GetEventStreamManager()
				if eventStreamMgr != nil {
					builderAPISrv.SetEventBroadcaster(eventStreamMgr)
					logger.Info("Connected Builder API server to WebUI event stream")
				}
			}
		}

		// 16. Wire lifecycle manager callbacks to ePBS (if both present)
		if lifecycleMgr != nil && epbsSvc != nil {
			lifecycleMgr.SetDepositPendingCallback(func() {
				epbsSvc.SetRegistrationPending()
			})
			lifecycleMgr.SetRegistrationCallback(func(index uint64) {
				epbsSvc.SetBuilderRegistered(index)
				if builderAPISrv != nil {
					builderAPISrv.SetBuilderIndex(index)
				}
			})
		}

		// 17. Start builder service
		logger.Info("Starting builder service...")

		if err := builderSvc.Start(ctx); err != nil {
			return fmt.Errorf("failed to start builder: %w", err)
		}
		defer builderSvc.Stop()

		// 18. Start ePBS service (if available)
		if epbsSvc != nil {
			logger.Info("Starting ePBS service...")

			if err := epbsSvc.Start(ctx, builderSvc); err != nil {
				return fmt.Errorf("failed to start ePBS: %w", err)
			}
			defer epbsSvc.Stop()
		}

		// 19. Start lifecycle manager (after ePBS so bid tracker is available)
		if lifecycleMgr != nil {
			if epbsSvc != nil {
				lifecycleMgr.SetBidTracker(epbsSvc.GetBidTracker())
			}

			if err := lifecycleMgr.Start(ctx); err != nil {
				return fmt.Errorf("failed to start lifecycle manager: %w", err)
			}
			defer lifecycleMgr.Stop()
		}

		// 20. Start proposer preferences service (if initialized)
		if propPrefSvc != nil {
			if err := propPrefSvc.Start(ctx); err != nil {
				return fmt.Errorf("failed to start proposer preferences service: %w", err)
			}
			defer propPrefSvc.Stop()

			logger.Info("Proposer preferences SSE listener started")
		}

		logger.Info("Builder is running. Press Ctrl+C to stop.")

		// 21. Wait for shutdown signal
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
