// Package cmd implements the CLI commands for buildoor.
package cmd

import (
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
	rootCmd.PersistentFlags().String("cl-client", "", "Consensus layer client URL")
	rootCmd.PersistentFlags().String("el-engine-api", "", "Execution layer engine API URL (JWT-authenticated)")
	rootCmd.PersistentFlags().String("el-jwt-secret", "", "Path to JWT secret file for engine API authentication")
	rootCmd.PersistentFlags().String("el-rpc", "", "Execution layer JSON-RPC URL (for lifecycle transactions)")
	rootCmd.PersistentFlags().String("wallet-privkey", "", "Wallet ECDSA private key (hex)")
	rootCmd.PersistentFlags().Int("api-port", 0, "HTTP API port (0 = disabled)")
	rootCmd.PersistentFlags().Bool("lifecycle", false, "Enable builder lifecycle management")
	rootCmd.PersistentFlags().Bool("epbs", true, "Enable ePBS bidding/revealing")
	rootCmd.PersistentFlags().Uint64("deposit-amount", defaults.DepositAmount, "Builder deposit amount in Gwei")
	rootCmd.PersistentFlags().Uint64("topup-threshold", defaults.TopupThreshold, "Balance threshold for auto top-up in Gwei")
	rootCmd.PersistentFlags().Uint64("topup-amount", defaults.TopupAmount, "Amount to top-up in Gwei")
	rootCmd.PersistentFlags().String("log-level", "info", "Log level (debug, info, warn, error)")

	// Schedule flags
	rootCmd.PersistentFlags().String("schedule-mode", string(defaults.Schedule.Mode), "Schedule mode: all, every_nth, next_n")
	rootCmd.PersistentFlags().Uint64("schedule-every-nth", defaults.Schedule.EveryNth, "Build every Nth slot")
	rootCmd.PersistentFlags().Uint64("schedule-next-n", defaults.Schedule.NextN, "Build next N slots then stop")
	rootCmd.PersistentFlags().Uint64("schedule-start-slot", defaults.Schedule.StartSlot, "Start building at this slot")

	// Build start time flag
	rootCmd.PersistentFlags().Int64("build-start-time", defaults.EPBS.BuildStartTime, "Build start time in ms relative to slot start")

	// ePBS time-based flags (use defaults from config package)
	rootCmd.PersistentFlags().Int64("epbs-bid-start", defaults.EPBS.BidStartTime, "First bid time in ms relative to slot start")
	rootCmd.PersistentFlags().Int64("epbs-bid-end", defaults.EPBS.BidEndTime, "Last bid time in ms relative to slot start")
	rootCmd.PersistentFlags().Int64("epbs-reveal-time", defaults.EPBS.RevealTime, "Reveal time in ms relative to slot start")
	rootCmd.PersistentFlags().Uint64("epbs-bid-min", defaults.EPBS.BidMinAmount, "Minimum bid amount in gwei")
	rootCmd.PersistentFlags().Uint64("epbs-bid-increase", defaults.EPBS.BidIncrease, "Bid increase per subsequent bid in gwei")
	rootCmd.PersistentFlags().Int64("epbs-bid-interval", defaults.EPBS.BidInterval, "Interval between bids in ms (0 = single bid)")

	// Validate withdrawals flag
	rootCmd.PersistentFlags().Bool("validate-withdrawals", defaults.ValidateWithdrawals, "Validate expected vs actual withdrawals")

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
		BuilderPrivkey:   v.GetString("builder-privkey"),
		CLClient:         v.GetString("cl-client"),
		ELEngineAPI:      v.GetString("el-engine-api"),
		ELJWTSecret:      v.GetString("el-jwt-secret"),
		ELRPC:            v.GetString("el-rpc"),
		WalletPrivkey:    v.GetString("wallet-privkey"),
		APIPort:          v.GetInt("api-port"),
		LifecycleEnabled: v.GetBool("lifecycle"),
		EPBSEnabled:      v.GetBool("epbs"),
		DepositAmount:    v.GetUint64("deposit-amount"),
		TopupThreshold:   v.GetUint64("topup-threshold"),
		TopupAmount:      v.GetUint64("topup-amount"),
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
		},
		ValidateWithdrawals: v.GetBool("validate-withdrawals"),
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
