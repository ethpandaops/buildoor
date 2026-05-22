// Package cmd implements the CLI commands for buildoor.
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/config"
)

var (
	cfgFile string
	cfg     *builder.Config
	logger  *logrus.Logger
)

var rootCmd = &cobra.Command{
	Use:   "buildoor",
	Short: "Testing-focused ePBS block builder",
	Long: `Buildoor is a testing tool for ePBS that can build blocks,
submit bids, and reveal payloads with configurable behavior.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		initBootstrapLogger()

		if err := initConfig(); err != nil {
			return err
		}

		initLogger()

		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file path (default: buildoor.yaml in current directory or $HOME/.buildoor/)")
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

// initBootstrapLogger sets up a default info-level logger used before config is loaded.
func initBootstrapLogger() {
	logger = logrus.New()
	logger.SetOutput(os.Stdout)
	logger.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	logger.SetLevel(logrus.InfoLevel)
}

// initLogger applies the log level from the loaded config.
func initLogger() {
	level, err := logrus.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = logrus.InfoLevel
	}
	logger.SetLevel(level)
}

func initConfig() error {
	if cfgFile == "" {
		candidates := []string{
			"buildoor.yaml",
			filepath.Join(os.Getenv("HOME"), ".buildoor", "buildoor.yaml"),
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				cfgFile = c
				break
			}
		}
	}

	if cfgFile == "" {
		return fmt.Errorf("no config file found; use --config or place buildoor.yaml in the current directory")
	}

	loaded, err := config.NewLoader(logger).LoadConfig(cfgFile)
	if err != nil {
		return err
	}

	cfg = loaded

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
