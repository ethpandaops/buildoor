package config

// DefaultConfig returns a configuration with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		APIPort:          0,
		APIUserHeader:    "Cf-Access-Authenticated-User-Email",
		APITokenKey:      "",
		LifecycleEnabled: false,
		DepositAmount:    10000000000, // 10 ETH in Gwei
		TopupThreshold:   1000000000,  // 1 ETH in Gwei
		TopupAmount:      5000000000,  // 5 ETH in Gwei
		Schedule: ScheduleConfig{
			Mode:     ScheduleModeAll,
			EveryNth: 1,
		},
		EPBS: EPBSConfig{
			BuildStartTime: 0,       // Build immediately on payload_attributes event
			BidStartTime:   -1000,   // 1 second before slot start
			BidEndTime:     2000,    // 2 seconds into slot
			RevealTime:     6000,    // 6 seconds into slot
			BidMinAmount:   1000000, // 1M gwei = 0.001 ETH
			BidIncrease:    100000,  // 100k gwei per subsequent bid
			BidInterval:    250,     // 250ms between bids
		},
		ValidateWithdrawals: false,
	}
}
