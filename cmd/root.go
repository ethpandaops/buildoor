// Package cmd implements the CLI commands for buildoor.
package cmd

import (
	"fmt"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/config"
)

var (
	cfgFile string
	cfg     *builder.Config
	logger  *logrus.Logger
	v       *viper.Viper
)

var rootCmd = &cobra.Command{
	Use:   "buildoor",
	Short: "Testing-focused ePBS block builder",
	Long: `Buildoor is a testing tool for ePBS that can build blocks,
submit bids, and reveal payloads with configurable behavior.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		initLogger()

		if err := initConfig(); err != nil {
			return err
		}

		return nil
	},
}

func init() {
	v = viper.New()
	cobra.OnInitialize(loadConfigFile)

	// Get defaults from config package
	defaults := config.DefaultConfig()

	// Global flags
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file path")
	rootCmd.PersistentFlags().String("builder-privkey", "", "Builder BLS private key (hex)")
	rootCmd.PersistentFlags().String("builder-mnemonic", "", "BIP-39 mnemonic to derive the builder BLS key from (path m/12381/3600/{index}/0/0; mutually exclusive with --builder-privkey)")
	rootCmd.PersistentFlags().Uint64("builder-key-index", 0, "Account index for --builder-mnemonic key derivation")
	rootCmd.PersistentFlags().String("cl-client", "", "Consensus layer client URL")
	rootCmd.PersistentFlags().String("el-engine-api", "", "Execution layer engine API URL (JWT-authenticated)")
	rootCmd.PersistentFlags().String("el-jwt-secret", "", "Path to JWT secret file for engine API authentication")
	rootCmd.PersistentFlags().String("el-rpc", "", "Execution layer JSON-RPC URL (for lifecycle transactions)")
	rootCmd.PersistentFlags().String("wallet-privkey", "", "Wallet ECDSA private key (hex)")
	rootCmd.PersistentFlags().Int("api-port", 0, "HTTP API port (0 = disabled)")
	rootCmd.PersistentFlags().String("auth-provider-url", "", "Optional authenticatoor URL (e.g. https://auth.<devnet>.example.io); when set, API requests must carry a JWT verified against the authenticatoor's JWKS. When empty the API is unauthenticated.")
	rootCmd.PersistentFlags().String("inject-head-html", "", "Raw HTML snippet injected into <head> of the served SPA (e.g. global panda menu loader). Falls back to the BUILDOOR_INJECT_HEAD_HTML env var when empty.")
	rootCmd.PersistentFlags().String("overview-url", "", "Optional URL of the multi-instance overview UI. When set, the dashboard renders an Overview entry as the first top-nav item so navigation stays consistent across instances.")
	rootCmd.PersistentFlags().Bool("lifecycle", false, "Enable builder lifecycle management")
	rootCmd.PersistentFlags().Bool("epbs-enabled", false, "Enable ePBS bidding/revealing at startup")
	rootCmd.PersistentFlags().Bool("builder-api-enabled", defaults.BuilderAPIEnabled, "Enable traditional Builder API at startup (served on --api-port)")
	rootCmd.PersistentFlags().Uint64("builder-api-subsidy", defaults.BuilderAPI.BlockValueSubsidyGwei, "Block value subsidy added to bids in Gwei")
	rootCmd.PersistentFlags().Uint64("deposit-amount", defaults.DepositAmount, "Builder deposit amount in Gwei")
	rootCmd.PersistentFlags().Uint64("topup-threshold", defaults.TopupThreshold, "Balance threshold for auto top-up in Gwei")
	rootCmd.PersistentFlags().Uint64("topup-amount", defaults.TopupAmount, "Amount to top-up in Gwei")
	rootCmd.PersistentFlags().String("log-level", "info", "Log level (debug, info, warn, error)")
	rootCmd.PersistentFlags().String("state-db", "", "Optional path to a SQLite state-db. When set, UI setting overrides, won blocks, validator registrations, proposer preferences and an audit log are persisted across restarts. When empty, runtime changes are in-memory only.")

	// Schedule flags
	rootCmd.PersistentFlags().String("schedule-mode", string(defaults.Schedule.Mode), "Schedule mode: all, every_nth, next_n")
	rootCmd.PersistentFlags().Uint64("schedule-every-nth", defaults.Schedule.EveryNth, "Build every Nth slot")
	rootCmd.PersistentFlags().Uint64("schedule-next-n", defaults.Schedule.NextN, "Build next N slots then stop")
	rootCmd.PersistentFlags().Uint64("schedule-start-slot", defaults.Schedule.StartSlot, "Start building at this slot")

	// Build start time flag (0 = auto from slot time, scaled from the 12s value)
	rootCmd.PersistentFlags().Int64("build-start-time", 0, "Build start time in ms relative to slot start (0 = auto: -2900ms @12s, scaled to slot time)")

	// ePBS time-based flags (0 = auto from slot time, scaled from the 12s value)
	rootCmd.PersistentFlags().Int64("epbs-bid-start", 0, "First bid time in ms relative to slot start (0 = auto: -400ms @12s, scaled to slot time)")
	rootCmd.PersistentFlags().Int64("epbs-bid-end", 0, "Last bid time in ms relative to slot start (0 = auto: -100ms @12s, scaled to slot time)")
	rootCmd.PersistentFlags().Int64("epbs-reveal-time", 0, "Reveal time in ms relative to slot start (0 = auto: 7000ms @12s, scaled to slot time)")
	rootCmd.PersistentFlags().Uint64("epbs-bid-min", defaults.EPBS.BidMinAmount, "Minimum bid amount in gwei")
	rootCmd.PersistentFlags().Uint64("epbs-bid-increase", defaults.EPBS.BidIncrease, "Bid increase per subsequent bid in gwei")
	rootCmd.PersistentFlags().Int64("epbs-bid-interval", defaults.EPBS.BidInterval, "Interval between bids in ms (0 = single bid)")
	rootCmd.PersistentFlags().Uint64("epbs-bid-subsidy", defaults.EPBS.BidSubsidy, "Gwei added to every bid so it clears the proposer's local-EL threshold")

	// Validate withdrawals flag
	rootCmd.PersistentFlags().Bool("validate-withdrawals", defaults.ValidateWithdrawals, "Validate expected vs actual withdrawals")

	// Payload Build Time (0 = auto from slot time, scaled from the 12s value)
	rootCmd.PersistentFlags().Uint64("payload-build-time", 0, "Time to allow the EL to build the payload in ms (0 = auto: 2100ms @12s, scaled to slot time)")

	// Validator ranges
	rootCmd.PersistentFlags().String("validator-ranges-file", "", "Path to validator ranges YAML file (format: '0-127: client-name')")
	rootCmd.PersistentFlags().String("validator-ranges-url", "", "URL to fetch validator ranges JSON (format: {\"ranges\": {\"0-127\": \"client-name\"}})")

	// Bind all flags to viper
	if err := v.BindPFlags(rootCmd.PersistentFlags()); err != nil {
		logger.WithError(err).Fatal("Failed to bind flags")
	}
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func initLogger() {
	logger = logrus.New()
	logger.SetOutput(os.Stdout)
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	levelStr := v.GetString("log-level")

	level, err := logrus.ParseLevel(levelStr)
	if err != nil {
		level = logrus.InfoLevel
	}

	logger.SetLevel(level)
}

func loadConfigFile() {
	if cfgFile != "" {
		v.SetConfigFile(cfgFile)
	} else {
		v.SetConfigName("buildoor")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("$HOME/.buildoor")
	}

	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			if logger != nil {
				logger.WithError(err).Warn("Error reading config file")
			}
		}
	}
}

func initConfig() error {
	cfg = &builder.Config{
		BuilderPrivkey:    v.GetString("builder-privkey"),
		BuilderMnemonic:   v.GetString("builder-mnemonic"),
		BuilderKeyIndex:   v.GetUint64("builder-key-index"),
		CLClient:          v.GetString("cl-client"),
		ELEngineAPI:       v.GetString("el-engine-api"),
		ELJWTSecret:       v.GetString("el-jwt-secret"),
		ELRPC:             v.GetString("el-rpc"),
		WalletPrivkey:     v.GetString("wallet-privkey"),
		APIPort:           v.GetInt("api-port"),
		AuthProviderURL:   v.GetString("auth-provider-url"),
		InjectHeadHTML:    v.GetString("inject-head-html"),
		OverviewURL:       v.GetString("overview-url"),
		LifecycleEnabled:  v.GetBool("lifecycle"),
		EPBSEnabled:       v.GetBool("epbs-enabled"),
		BuilderAPIEnabled: v.GetBool("builder-api-enabled"),
		BuilderAPI: builder.BuilderAPIConfig{
			BlockValueSubsidyGwei: v.GetUint64("builder-api-subsidy"),
		},
		DepositAmount:  v.GetUint64("deposit-amount"),
		TopupThreshold: v.GetUint64("topup-threshold"),
		TopupAmount:    v.GetUint64("topup-amount"),
		Schedule: builder.ScheduleConfig{
			Mode:      builder.ScheduleMode(v.GetString("schedule-mode")),
			EveryNth:  v.GetUint64("schedule-every-nth"),
			NextN:     v.GetUint64("schedule-next-n"),
			StartSlot: v.GetUint64("schedule-start-slot"),
		},
		EPBS: builder.EPBSConfig{
			BuildStartTime: v.GetInt64("build-start-time"),
			BidStartTime:   v.GetInt64("epbs-bid-start"),
			BidEndTime:     v.GetInt64("epbs-bid-end"),
			RevealTime:     v.GetInt64("epbs-reveal-time"),
			BidMinAmount:   v.GetUint64("epbs-bid-min"),
			BidIncrease:    v.GetUint64("epbs-bid-increase"),
			BidInterval:    v.GetInt64("epbs-bid-interval"),
			BidSubsidy:     v.GetUint64("epbs-bid-subsidy"),
		},
		ValidateWithdrawals: v.GetBool("validate-withdrawals"),
		PayloadBuildTime:    v.GetUint64("payload-build-time"),
		ValidatorRanges: builder.ValidatorRangesConfig{
			File: v.GetString("validator-ranges-file"),
			URL:  v.GetString("validator-ranges-url"),
		},
		StateDBPath: v.GetString("state-db"),
	}

	if cfg.BuilderPrivkey != "" && cfg.BuilderMnemonic != "" {
		return fmt.Errorf("provide only one of --builder-privkey or --builder-mnemonic, not both")
	}

	return nil
}

// GetConfig returns the current configuration.
func GetConfig() *builder.Config {
	return cfg
}

// GetLogger returns the application logger.
func GetLogger() *logrus.Logger {
	return logger
}
