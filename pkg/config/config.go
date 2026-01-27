// Package config handles configuration loading and validation for buildoor.
package config

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// Loader handles configuration loading from files and flags.
type Loader struct {
	log logrus.FieldLogger
}

// NewLoader creates a new configuration loader.
func NewLoader(log logrus.FieldLogger) *Loader {
	return &Loader{
		log: log.WithField("component", "config"),
	}
}

// LoadConfig loads configuration from a YAML file.
func (l *Loader) LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return cfg, nil
}

// LoadConfigFromFlags loads configuration from viper flags.
func (l *Loader) LoadConfigFromFlags(v *viper.Viper) (*Config, error) {
	cfg := DefaultConfig()

	// Core settings
	if val := v.GetString("builder-privkey"); val != "" {
		cfg.BuilderPrivkey = val
	}

	if val := v.GetString("cl-client"); val != "" {
		cfg.CLClient = val
	}

	if val := v.GetString("el-engine-api"); val != "" {
		cfg.ELEngineAPI = val
	}

	if val := v.GetString("el-jwt-secret"); val != "" {
		cfg.ELJWTSecret = val
	}

	if val := v.GetString("el-rpc"); val != "" {
		cfg.ELRPC = val
	}

	if val := v.GetString("wallet-privkey"); val != "" {
		cfg.WalletPrivkey = val
	}

	cfg.APIPort = v.GetInt("api-port")
	cfg.APIUserHeader = v.GetString("api-user-header")
	cfg.APITokenKey = v.GetString("api-token-key")
	cfg.LifecycleEnabled = v.GetBool("lifecycle")
	cfg.DepositAmount = v.GetUint64("deposit-amount")
	cfg.TopupThreshold = v.GetUint64("topup-threshold")
	cfg.TopupAmount = v.GetUint64("topup-amount")

	// Schedule config
	cfg.Schedule.Mode = ScheduleMode(v.GetString("schedule-mode"))
	cfg.Schedule.EveryNth = v.GetUint64("schedule-every-nth")
	cfg.Schedule.NextN = v.GetUint64("schedule-next-n")
	cfg.Schedule.StartSlot = v.GetUint64("schedule-start-slot")

	return cfg, nil
}

// ValidateConfig validates the configuration for consistency and completeness.
func ValidateConfig(cfg *Config) error {
	// Builder private key validation (32 bytes hex)
	if cfg.BuilderPrivkey != "" {
		privkey := strings.TrimPrefix(cfg.BuilderPrivkey, "0x")
		decoded, err := hex.DecodeString(privkey)

		if err != nil {
			return fmt.Errorf("builder_privkey: invalid hex encoding: %w", err)
		}

		if len(decoded) != 32 {
			return fmt.Errorf("builder_privkey: must be 32 bytes, got %d", len(decoded))
		}
	}

	// CL client URL validation
	if cfg.CLClient != "" {
		if _, err := url.Parse(cfg.CLClient); err != nil {
			return fmt.Errorf("cl_client: invalid URL: %w", err)
		}
	}

	// EL Engine API URL validation (always required for payload building)
	if cfg.ELEngineAPI != "" {
		if _, err := url.Parse(cfg.ELEngineAPI); err != nil {
			return fmt.Errorf("el_engine_api: invalid URL: %w", err)
		}
	}

	// JWT secret validation (required when engine API is configured)
	if cfg.ELEngineAPI != "" && cfg.ELJWTSecret != "" {
		if _, err := os.Stat(cfg.ELJWTSecret); os.IsNotExist(err) {
			return fmt.Errorf("el_jwt_secret: file does not exist: %s", cfg.ELJWTSecret)
		}
	}

	// Lifecycle-specific validations
	if cfg.LifecycleEnabled {
		if cfg.ELRPC == "" {
			return fmt.Errorf("el_rpc is required when lifecycle is enabled")
		}

		if _, err := url.Parse(cfg.ELRPC); err != nil {
			return fmt.Errorf("el_rpc: invalid URL: %w", err)
		}

		if cfg.WalletPrivkey == "" {
			return fmt.Errorf("wallet_privkey is required when lifecycle is enabled")
		}

		walletKey := strings.TrimPrefix(cfg.WalletPrivkey, "0x")
		decoded, err := hex.DecodeString(walletKey)

		if err != nil {
			return fmt.Errorf("wallet_privkey: invalid hex encoding: %w", err)
		}

		if len(decoded) != 32 {
			return fmt.Errorf("wallet_privkey: must be 32 bytes, got %d", len(decoded))
		}
	}

	// Schedule mode validation
	switch cfg.Schedule.Mode {
	case ScheduleModeAll, ScheduleModeEveryN, ScheduleModeNextN:
		// Valid modes
	case "":
		cfg.Schedule.Mode = ScheduleModeAll
	default:
		return fmt.Errorf("schedule.mode: invalid value %q (must be all, every_nth, or next_n)",
			cfg.Schedule.Mode)
	}

	// EPBS config validation
	if cfg.EPBS.BidStartTime > cfg.EPBS.BidEndTime {
		return fmt.Errorf("epbs.bid_start_time cannot be after epbs.bid_end_time")
	}

	if cfg.EPBS.BidInterval < 0 {
		return fmt.Errorf("epbs.bid_interval must be >= 0")
	}

	return nil
}

// MergeConfigs merges override config values into the base config.
// Non-zero values in override replace values in base.
func MergeConfigs(base, override *Config) *Config {
	result := *base

	if override.BuilderPrivkey != "" {
		result.BuilderPrivkey = override.BuilderPrivkey
	}

	if override.CLClient != "" {
		result.CLClient = override.CLClient
	}

	if override.ELEngineAPI != "" {
		result.ELEngineAPI = override.ELEngineAPI
	}

	if override.ELJWTSecret != "" {
		result.ELJWTSecret = override.ELJWTSecret
	}

	if override.ELRPC != "" {
		result.ELRPC = override.ELRPC
	}

	if override.WalletPrivkey != "" {
		result.WalletPrivkey = override.WalletPrivkey
	}

	if override.APIPort != 0 {
		result.APIPort = override.APIPort
	}

	if override.LifecycleEnabled {
		result.LifecycleEnabled = override.LifecycleEnabled
	}

	if override.DepositAmount != 0 {
		result.DepositAmount = override.DepositAmount
	}

	if override.TopupThreshold != 0 {
		result.TopupThreshold = override.TopupThreshold
	}

	if override.TopupAmount != 0 {
		result.TopupAmount = override.TopupAmount
	}

	// Schedule config
	if override.Schedule.Mode != "" {
		result.Schedule.Mode = override.Schedule.Mode
	}

	if override.Schedule.EveryNth != 0 {
		result.Schedule.EveryNth = override.Schedule.EveryNth
	}

	if override.Schedule.NextN != 0 {
		result.Schedule.NextN = override.Schedule.NextN
	}

	if override.Schedule.StartSlot != 0 {
		result.Schedule.StartSlot = override.Schedule.StartSlot
	}

	// EPBS config
	if override.EPBS.BidStartTime != 0 {
		result.EPBS.BidStartTime = override.EPBS.BidStartTime
	}

	if override.EPBS.BidEndTime != 0 {
		result.EPBS.BidEndTime = override.EPBS.BidEndTime
	}

	if override.EPBS.RevealTime != 0 {
		result.EPBS.RevealTime = override.EPBS.RevealTime
	}

	if override.EPBS.BidMinAmount != 0 {
		result.EPBS.BidMinAmount = override.EPBS.BidMinAmount
	}

	if override.EPBS.BidIncrease != 0 {
		result.EPBS.BidIncrease = override.EPBS.BidIncrease
	}

	if override.EPBS.BidInterval != 0 {
		result.EPBS.BidInterval = override.EPBS.BidInterval
	}

	return &result
}
