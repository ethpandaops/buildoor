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
			BuildStartTime:    -2000,   // 2 seconds before slot start
			BidStartTime:      -1000,   // 1 second before slot start
			BidEndTime:        1000,    // 1 second into slot
			RevealTime:        6000,    // 6 seconds into slot
			BidMinAmount:      1000000, // 1M gwei = 0.001 ETH
			BidIncrease:       100000,  // 100k gwei per subsequent bid
			BidInterval:       250,     // 250ms between bids
			PayloadBuildDelay: 500,     // 500ms delay for EL to build block with transactions
		},
		LegacyBuilder: LegacyBuilderConfig{
			Schedule: ScheduleConfig{
				Mode:     ScheduleModeAll,
				EveryNth: 1,
			},
			SubmitStartTime:   -2000,
			SubmitEndTime:     1000,
			SubmitInterval:    500,
			BidIncrease:       100000,
			PaymentMode:       "fixed",
			FixedPayment:      "10000000000000000", // 0.01 ETH
			PaymentPercentage: 9000,                // 90%
			PaymentGasLimit:   21000,
			PayloadBuildDelay: 500, // 500ms delay for EL to build block with transactions
			ValidatorPollSecs: 60,
		},
		ValidateWithdrawals: false,
	}
}
