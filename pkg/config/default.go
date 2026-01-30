package config

// DefaultConfig returns a configuration with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		APIPort:           0,
		APIUserHeader:     "Cf-Access-Authenticated-User-Email",
		APITokenKey:       "",
		LifecycleEnabled:  false,
		BuilderAPIEnabled: false, // Disabled by default
		BuilderAPI: BuilderAPIConfig{
			Port: 9000, // Default Builder API port
		},
		DepositAmount:  10000000000, // 10 ETH in Gwei
		TopupThreshold: 1000000000,  // 1 ETH in Gwei
		TopupAmount:    5000000000,  // 5 ETH in Gwei
		Schedule: ScheduleConfig{
			Mode:     ScheduleModeAll,
			EveryNth: 1,
		},
		EPBS: EPBSConfig{
			BuildStartTime: 7000,   // 2 seconds before slot start
			BidStartTime:   -1000,   // 1 second before slot start
			BidEndTime:     1000,    // 1 second into slot
			RevealTime:     6000,    // 6 seconds into slot
			BidMinAmount:   1000000, // 1M gwei = 0.001 ETH
			BidIncrease:    100000,  // 100k gwei per subsequent bid
			BidInterval:    250,     // 250ms between bids
		},
		ValidateWithdrawals: false,
	}
}
