package config

import (
	"encoding/json"
	"fmt"
)

// Field describes a single mutable setting: how to read and write it on a
// Config, its canonical storage key, and the viper flag key used to detect
// operator-supplied (CLI/env/config) changes across restarts.
//
// Only mutable settings are registered. Immutable startup-only fields (keys,
// client URLs, ports, the state-db path) are intentionally absent: they are
// never overridable via the UI and never persisted.
type Field struct {
	// Key is the canonical persisted/override key, e.g. "epbs.bid_subsidy".
	Key string
	// FlagKey is the viper flag key, e.g. "epbs-bid-subsidy", used with
	// viper.IsSet for CLI-change detection.
	FlagKey string

	get    func(*Config) any
	set    func(*Config, any) error
	decode func(json.RawMessage) (any, error)
	equal  func(a, b any) bool
}

// Get reads the field's value from c.
func (f Field) Get(c *Config) any { return f.get(c) }

// Set writes v into the field on c (in place).
func (f Field) Set(c *Config, v any) error { return f.set(c, v) }

// Decode parses a JSON value into the field's Go type.
func (f Field) Decode(raw json.RawMessage) (any, error) { return f.decode(raw) }

// Encode serialises a typed value to JSON for storage.
func (f Field) Encode(v any) (json.RawMessage, error) { return json.Marshal(v) }

// Equal reports whether two typed values of this field are equal.
func (f Field) Equal(a, b any) bool { return f.equal(a, b) }

// newField builds a Field for a comparable scalar type using a pointer accessor
// into the Config struct, so the same closure serves both get and set.
func newField[T comparable](key, flag string, ptr func(*Config) *T) Field {
	return Field{
		Key:     key,
		FlagKey: flag,
		get:     func(c *Config) any { return *ptr(c) },
		set: func(c *Config, v any) error {
			tv, ok := v.(T)
			if !ok {
				return fmt.Errorf("settings: %s: invalid value type %T", key, v)
			}

			*ptr(c) = tv

			return nil
		},
		decode: func(raw json.RawMessage) (any, error) {
			var x T
			if err := json.Unmarshal(raw, &x); err != nil {
				return nil, err
			}

			return x, nil
		},
		equal: func(a, b any) bool {
			av, aok := a.(T)
			bv, bok := b.(T)

			return aok && bok && av == bv
		},
	}
}

// Fields returns the registry of all mutable settings, including the per-module
// enable flags (which are configured via CLI flags exactly like other settings
// and therefore follow the same default/cli/ui resolution).
func Fields() []Field {
	return []Field{
		newField(KeyScheduleMode, "schedule-mode", func(c *Config) *ScheduleMode { return &c.Schedule.Mode }),
		newField(KeyScheduleEveryNth, "schedule-every-nth", func(c *Config) *uint64 { return &c.Schedule.EveryNth }),
		newField(KeyScheduleNextN, "schedule-next-n", func(c *Config) *uint64 { return &c.Schedule.NextN }),
		newField(KeyScheduleStartSlot, "schedule-start-slot", func(c *Config) *uint64 { return &c.Schedule.StartSlot }),

		newField(KeyEPBSBuildStartTime, "build-start-time", func(c *Config) *int64 { return &c.EPBS.BuildStartTime }),
		newField(KeyEPBSBidStartTime, "epbs-bid-start", func(c *Config) *int64 { return &c.EPBS.BidStartTime }),
		newField(KeyEPBSBidEndTime, "epbs-bid-end", func(c *Config) *int64 { return &c.EPBS.BidEndTime }),
		newField(KeyEPBSRevealTime, "epbs-reveal-time", func(c *Config) *int64 { return &c.EPBS.RevealTime }),
		newField(KeyEPBSBidMinAmount, "epbs-bid-min", func(c *Config) *uint64 { return &c.EPBS.BidMinAmount }),
		newField(KeyEPBSBidIncrease, "epbs-bid-increase", func(c *Config) *uint64 { return &c.EPBS.BidIncrease }),
		newField(KeyEPBSBidInterval, "epbs-bid-interval", func(c *Config) *int64 { return &c.EPBS.BidInterval }),
		newField(KeyEPBSBidSubsidy, "epbs-bid-subsidy", func(c *Config) *uint64 { return &c.EPBS.BidSubsidy }),

		newField(KeyPayloadBuildTime, "payload-build-time", func(c *Config) *uint64 { return &c.PayloadBuildTime }),
		newField(KeyExtraData, "extra-data", func(c *Config) *string { return &c.ExtraData }),
		newField(KeyBuilderAPISubsidy, "builder-api-subsidy", func(c *Config) *uint64 { return &c.BuilderAPI.BlockValueSubsidyGwei }),

		newField(KeyDepositAmount, "deposit-amount", func(c *Config) *uint64 { return &c.DepositAmount }),
		newField(KeyTopupThreshold, "topup-threshold", func(c *Config) *uint64 { return &c.TopupThreshold }),
		newField(KeyTopupAmount, "topup-amount", func(c *Config) *uint64 { return &c.TopupAmount }),

		newField(KeyEPBSEnabled, "epbs-enabled", func(c *Config) *bool { return &c.EPBSEnabled }),
		newField(KeyBuilderAPIEnabled, "builder-api-enabled", func(c *Config) *bool { return &c.BuilderAPIEnabled }),
		newField(KeyLifecycleEnabled, "lifecycle", func(c *Config) *bool { return &c.LifecycleEnabled }),
	}
}
