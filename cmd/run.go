package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	enginejsonrpc "github.com/ethpandaops/go-eth-engine-client/jsonrpc"
	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/ethpandaops/buildoor/pkg/builderapi"
	"github.com/ethpandaops/buildoor/pkg/builderapi/legacy"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/lifecycle"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/p2p_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
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

		engineClient, err := enginejsonrpc.New(ctx,
			enginejsonrpc.WithAddress(cfg.ELEngineAPI),
			enginejsonrpc.WithJWTSecretFile(cfg.ELJWTSecret),
			enginejsonrpc.WithLogger(logger),
		)
		if err != nil {
			return fmt.Errorf("failed to connect to EL engine API: %w", err)
		}

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

		var chainSpec *chain.ChainSpec

		for {
			specData, rawData, err := clClient.GetRawSpecData(ctx)
			if err == nil {
				chainSpec, err = chain.ParseChainSpec(specData, rawData)
				if err != nil {
					return fmt.Errorf("failed to parse chain spec: %w", err)
				}

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
		for _, f := range config.Fields() {
			supplied[f.Key] = v.IsSet(f.FlagKey)
		}

		settingsSvc, err := config.NewService(cfg, defaults, supplied, stateDB, logger)
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

		// 9. Initialize builder service (standalone block building). When the
		// Builder API is available this also creates the shared validator
		// registration memstore and registers the pre-Gloas settings resolver.
		logger.Info("Initializing builder service...")

		// Get fee recipient from wallet or use default address
		feeRecipient := common.HexToAddress("0x8943545177806ED17B9F23F0a21ee5948eCaa776")
		if w != nil {
			feeRecipient = w.Address()
		}

		// Validator registration store when Builder API is available (port > 0):
		// written by the legacy dialect's registerValidators handler, read by the
		// pre-Gloas proposer-settings resolver and the WebUI. Buffered persistence
		// into the state-db's kv_store (registered after stateDB's own close defer,
		// LIFO ⇒ the final flush runs while the state-db is still open).
		var validatorStore *memstore.Store[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration]

		builderAPIAvailable := cfg.APIPort > 0
		if builderAPIAvailable {
			validatorStore = memstore.New[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration]()
			validatorStore.SetPersistence(ctx,
				db.NewKVPersistence(stateDB, legacy.RegistrationsNamespace, legacy.RegistrationCodec{}),
				logger)

			defer validatorStore.Stop()
		}

		builderSvc, err := payload_builder.NewService(cfg, clClient, chainSvc, engineClient, feeRecipient, logger)
		if err != nil {
			return fmt.Errorf("failed to initialize builder: %w", err)
		}

		if builderAPIAvailable {
			// Pre-Gloas proposer settings resolve from Builder API validator
			// registrations; the Gloas+ gossip-preferences resolver is registered
			// in step 10 below. Both self-scope by fork, so the registration
			// order is not load-bearing.
			builderSvc.AddProposerSettingsResolver(
				legacy.NewRegistrationSettingsResolver(validatorStore, chainSvc))
		}

		// 9b. Start shared payment tracker, reveal service, and inclusion tracker.
		// The inclusion tracker runs on ALL networks (it is the single won_blocks
		// writer, covering legacy Builder API deliveries too); the reveal service and
		// payment tracker only exist when Gloas is scheduled. These are independent of
		// both bid flows and of the epbs_enabled flag.
		var paymentTracker *payload_bidder.PaymentTracker

		var revealSvc *payload_bidder.RevealService

		epbsAvailable := chainSpec.IsForkScheduled(version.DataVersionGloas)

		if epbsAvailable {
			paymentTracker = payload_bidder.NewPaymentTracker(chainSvc, logger)

			revealSvc = payload_bidder.NewRevealService(cfg, payload_bidder.NewSigner(blsSigner), clClient, chainSvc, builderSvc, paymentTracker, logger)
			if err := revealSvc.Start(ctx); err != nil {
				return fmt.Errorf("failed to start reveal service: %w", err)
			}
			defer revealSvc.Stop()
		}

		inclusionTracker := payload_bidder.NewInclusionTracker(clClient, chainSvc, builderSvc, revealSvc, paymentTracker, logger)
		inclusionTracker.SetStateDB(stateDB)

		if err := inclusionTracker.Start(ctx); err != nil {
			return fmt.Errorf("failed to start inclusion tracker: %w", err)
		}
		defer inclusionTracker.Stop()

		// 10. Initialize the proposer preferences service (started later in step
		// 20). Its per-slot store feeds the p2p bidder's bid gate, the Builder API
		// epbs dialect's bid construction, and the payload builder's Gloas+
		// proposer-settings resolution.
		var propPrefSvc *payload_bidder.ProposerPreferencesService

		if epbsAvailable {
			propPrefSvc = payload_bidder.NewProposerPreferencesService(clClient, chainSvc, logger)
			propPrefSvc.GetStore().SetPersistence(ctx,
				db.NewKVPersistence(stateDB, payload_bidder.ProposerPreferencesNamespace, payload_bidder.ProposerPreferencesCodec{}),
				logger)
			// Registered after stateDB's own close defer (LIFO) → the store's final
			// flush runs while the state-db is still open.
			defer propPrefSvc.GetStore().Stop()

			builderSvc.AddProposerSettingsResolver(propPrefSvc)
		}

		// 11. Initialize p2p bidder service (if Gloas fork is scheduled)
		var epbsSvc *p2p_bidder.Service

		if epbsAvailable {
			gloasForkEpoch := chainSpec.GetForkEpoch(version.DataVersionGloas)
			logger.WithField("gloas_fork_epoch", gloasForkEpoch).Info("Initializing p2p bidder service...")

			epbsSvc, err = p2p_bidder.NewService(&cfg.EPBS, clClient, chainSvc, blsSigner, propPrefSvc.GetStore(), logger)
			if err != nil {
				return fmt.Errorf("failed to initialize p2p bidder: %w", err)
			}

			epbsSvc.SetEnabled(cfg.EPBSEnabled)
		}

		// 12. Initialize Builder API server (routes served on --api-port via the shared server)
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

			builderAPISrv = builderapi.NewServer(&cfg.BuilderAPI, logger, chainSvc, builderSvc.GetPayloadCache(), blsSigner, validatorStore)
			builderAPISrv.SetFuluPublisher(clClient)
			builderAPISrv.SetCLClient(clClient)
			builderAPISrv.SetEnabled(cfg.BuilderAPIEnabled)

			// Persist builder preferences (max_execution_payment) into the
			// state-db's kv_store so they survive restarts.
			builderAPISrv.GetBuilderPreferencesStore().SetPersistence(ctx, stateDB, logger)
			defer builderAPISrv.GetBuilderPreferencesStore().Stop()

			if revealSvc != nil {
				builderAPISrv.SetRevealService(revealSvc)
			}

			if propPrefSvc != nil {
				builderAPISrv.SetProposerPreferencesStore(propPrefSvc.GetStore())
			}
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
			}, settingsSvc, stateDB, builderSvc, epbsSvc, lifecycleMgr, chainSvc, validatorStore, builderAPISrv, propPrefSvc, valRanges, revealSvc, inclusionTracker, paymentTracker)

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
				if revealSvc != nil {
					revealSvc.SetBuilderIndex(index)
				}
			})
		}

		// 17. Start builder service
		logger.Info("Starting builder service...")

		if err := builderSvc.Start(ctx); err != nil {
			return fmt.Errorf("failed to start builder: %w", err)
		}
		defer builderSvc.Stop()

		// 18. Start p2p bidder service (if available)
		if epbsSvc != nil {
			logger.Info("Starting p2p bidder service...")

			if err := epbsSvc.Start(ctx, builderSvc); err != nil {
				return fmt.Errorf("failed to start p2p bidder: %w", err)
			}
			defer epbsSvc.Stop()
		}

		// 19. Start lifecycle manager
		if lifecycleMgr != nil {
			if paymentTracker != nil {
				lifecycleMgr.SetPaymentTracker(paymentTracker)
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
