package config

// DefaultConfig returns a configuration with sensible defaults.
// Timing fields default to 0, which means "auto-compute from slot time".
// Call ApplySlotDefaults after loading the chain spec to fill them in.
func DefaultConfig() *Config {
	return &Config{
		APIPort:           0,
		AuthProviderURL:   "",
		LifecycleEnabled:  false,
		EPBSEnabled:       false, // Disabled by default
		BuilderAPIEnabled: false, // Disabled by default
		BuilderAPI: BuilderAPIConfig{
			BlockValueSubsidyGwei: 100000, // 100k Gwei
		},
		DepositAmount:               50000000000, // 50 ETH in Gwei
		TopupThreshold:              10000000000, // 10 ETH in Gwei
		TopupAmount:                 50000000000, // 50 ETH in Gwei
		DepositMaxFeeGwei:           1000000,     // 0.001 ETH in Gwei; delay deposits/topups above this queue fee
		ExtraData:                   "buildoor/",
		SlotResultRetentionEpochs:   100,
		SlotArtifactRetentionEpochs: 100,
		SlotArtifactCaptureEnabled:  true,
		Schedule: ScheduleConfig{
			Mode:     ScheduleModeAll,
			EveryNth: 1,
		},
		EPBS: EPBSConfig{
			// Timing fields: 0 = auto-compute from slot time (see ApplySlotDefaults).
			// Explicit non-zero values override the auto-computed defaults.
			BidMinAmount:         1000000,   // 1M gwei = 0.001 ETH
			BidIncrease:          100000,    // 100k gwei per subsequent bid
			BidInterval:          500,       // 500ms between bids
			BidSubsidy:           100000000, // 100M gwei = 0.1 ETH; clears validator local-EL threshold
			HeadVoteThresholdPct: 60,        // Gloas builder payment quorum (6/10)
		},
	}
}

// referenceSlotTimeMs is the slot duration the default timing values below are
// tuned for (12s mainnet slot). ApplySlotDefaults scales them linearly to the
// network's actual slot time.
const referenceSlotTimeMs = 12000

// ApplySlotDefaults fills in zero-valued timing fields with slot-relative defaults.
// This is called after the chain spec is loaded so the slot duration is known.
//
// Timing fields are tuned for a 12s slot and scaled linearly to the actual slot
// time (value = reference@12s * slotTimeMs / 12000):
//
//	BuildStartTime:  -2900ms @12s  (e.g. -1450ms @6s)
//	PayloadBuildTime: 2100ms @12s  (e.g.  1050ms @6s)
//	BidStartTime:     -400ms @12s  (e.g.  -200ms @6s)
//	BidEndTime:       -100ms @12s  (e.g.   -50ms @6s)
//	RevealTime:       5000ms @12s  (e.g.  2500ms @6s)
//
// RevealTime (41.7% of the slot) is anchored to the Gloas/EIP-7732 deadlines:
// it sits after the attestation deadline (ATTESTATION_DUE_BPS_GLOAS, 25%) — so
// the builder has seen attestation weight on the block before committing to
// reveal — and comfortably before the hard payload deadline (PAYLOAD_DUE_BPS,
// 50% since consensus-specs#5414), after which the PTC votes the payload
// absent. The ~8% (1s @12s) margin before that deadline lets the envelope
// gossip to PTC members, who attest at 75% (PAYLOAD_ATTESTATION_DUE_BPS).
func (c *Config) ApplySlotDefaults(slotTimeMs int64) {
	if c.EPBS.BuildStartTime == 0 {
		c.EPBS.BuildStartTime = -2900 * slotTimeMs / referenceSlotTimeMs
	}

	if c.PayloadBuildTime == 0 {
		c.PayloadBuildTime = uint64(2100 * slotTimeMs / referenceSlotTimeMs)
	}

	if c.EPBS.BidStartTime == 0 {
		c.EPBS.BidStartTime = -400 * slotTimeMs / referenceSlotTimeMs
	}

	if c.EPBS.BidEndTime == 0 {
		c.EPBS.BidEndTime = -100 * slotTimeMs / referenceSlotTimeMs
	}

	if c.EPBS.RevealTime == 0 {
		c.EPBS.RevealTime = 5000 * slotTimeMs / referenceSlotTimeMs
	}
}
