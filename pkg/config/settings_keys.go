package config

// Canonical settings keys. These are the persisted/override keys and are shared
// between the field registry (fields.go) and the API handlers that apply UI
// overrides, so the two never drift.
const (
	KeyScheduleMode      = "schedule.mode"
	KeyScheduleEveryNth  = "schedule.every_nth"
	KeyScheduleNextN     = "schedule.next_n"
	KeyScheduleStartSlot = "schedule.start_slot"

	KeyEPBSBuildStartTime    = "epbs.build_start_time"
	KeyEPBSBidStartTime      = "epbs.bid_start_time"
	KeyEPBSBidEndTime        = "epbs.bid_end_time"
	KeyEPBSBidMinAmount      = "epbs.bid_min_amount"
	KeyEPBSBidIncrease       = "epbs.bid_increase"
	KeyEPBSBidInterval       = "epbs.bid_interval"
	KeyEPBSBidSubsidy        = "epbs.bid_subsidy"
	KeyEPBSBidValueOverride  = "epbs.bid_value_override"
	KeyEPBSHeadVoteThreshold = "epbs.head_vote_threshold_pct"

	KeyRevealEnabled             = "reveal.enabled"
	KeyRevealGateMode            = "reveal.gate_mode"
	KeyRevealTimeMs              = "reveal.time_ms"
	KeyRevealVoteThreshold       = "reveal.vote_threshold_pct"
	KeyRevealBroadcastValidation = "reveal.broadcast_validation"
	KeyRevealMaxAttempts         = "reveal.max_attempts"
	KeyRevealRetryInterval       = "reveal.retry_interval_ms"

	KeyPayloadBuildTime        = "payload_build_time"
	KeyExtraData               = "extra_data"
	KeyBuilderAPISubsidy       = "builder_api.block_value_subsidy_gwei"
	KeyBuilderAPIValueOverride = "builder_api.value_override_gwei"

	KeySlotResultRetentionEpochs   = "slot_result_retention_epochs"
	KeySlotArtifactRetentionEpochs = "slot_artifact_retention_epochs"
	KeySlotArtifactCaptureEnabled  = "slot_artifact_capture_enabled"

	KeyDepositAmount  = "deposit_amount"
	KeyTopupThreshold = "topup_threshold"
	KeyTopupAmount    = "topup_amount"

	KeyEPBSEnabled       = "epbs_enabled"
	KeyBuilderAPIEnabled = "builder_api_enabled"
	KeyLifecycleEnabled  = "lifecycle_enabled"
)
