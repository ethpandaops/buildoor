package config

// DefaultConfig returns a configuration with sensible defaults.
// Timing fields default to 0, which means "auto-compute from slot time".
// Call ApplySlotDefaults after loading the chain spec to fill them in.
func DefaultConfig() *Config {
	return &Config{
		APIPort:           0,
		APIUserHeader:     "Cf-Access-Authenticated-User-Email",
		APITokenKey:       "",
		LifecycleEnabled:  false,
		EPBSEnabled:       false, // Disabled by default
		BuilderAPIEnabled: false, // Disabled by default
		BuilderAPI: BuilderAPIConfig{
			Port:                  0,      // Default 0 = disabled; set > 0 to enable Builder API availability
			BlockValueSubsidyGwei: 100000, // 100k Gwei
		},
		DepositAmount:  10000000000, // 10 ETH in Gwei
		TopupThreshold: 2000000000,  // 2 ETH in Gwei
		TopupAmount:    5000000000,  // 5 ETH in Gwei
		Schedule: ScheduleConfig{
			Mode:     ScheduleModeAll,
			EveryNth: 1,
		},
		EPBS: EPBSConfig{
			// Timing fields: 0 = auto-compute from slot time (see ApplySlotDefaults).
			// Explicit non-zero values override the auto-computed defaults.
			BidMinAmount: 1000000, // 1M gwei = 0.001 ETH
			BidIncrease:  100000,  // 100k gwei per subsequent bid
			BidInterval:  250,     // 250ms between bids
		},
		ValidateWithdrawals: false,
	}
}

// ApplySlotDefaults fills in zero-valued timing fields with slot-relative defaults.
// This is called after the chain spec is loaded so the slot duration is known.
//
// Default timing ratios (relative to slot time):
//
//	BuildStartTime:  -1/3 slot  (e.g. -4000ms for 12s, -2000ms for 6s)
//	PayloadBuildTime: 1/6 slot  (e.g.  2000ms for 12s,  1000ms for 6s)
//	BidStartTime:    -1/12 slot (e.g. -1000ms for 12s,  -500ms for 6s)
//	BidEndTime:       1/12 slot (e.g.  1000ms for 12s,   500ms for 6s)
//	RevealTime:       0         (at slot start)
func (c *Config) ApplySlotDefaults(slotTimeMs int64) {
	if c.EPBS.BuildStartTime == 0 {
		c.EPBS.BuildStartTime = -slotTimeMs / 3
	}

	if c.PayloadBuildTime == 0 {
		c.PayloadBuildTime = uint64(slotTimeMs / 6)
	}

	if c.EPBS.BidStartTime == 0 {
		c.EPBS.BidStartTime = -slotTimeMs / 12
	}

	if c.EPBS.BidEndTime == 0 {
		c.EPBS.BidEndTime = slotTimeMs / 12
	}

	// RevealTime 0 is the intended default (at slot start), so no auto-compute needed.
}
