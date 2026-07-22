// Package config handles configuration loading and validation for buildoor.
package config

// ValidatorRangesConfig configures how to load validator index → client name mappings.
// If both are set, URL takes precedence.
type ValidatorRangesConfig struct {
	// File is a path to a YAML file in the format produced by ethereum-package:
	//   "0-127": "01-geth-lighthouse"
	//   "128-255": "02-nethermind-teku"
	File string `yaml:"file" json:"file,omitempty"`

	// URL is fetched on startup and refreshed every 5 minutes.
	// Expected JSON: {"ranges": {"0-199": "prysm-ethrex-1", ...}}
	// Template: https://config.<network>.ethpandaops.io/api/v1/nodes/validator-ranges
	URL string `yaml:"url" json:"url,omitempty"`
}

// Config represents the complete configuration for the buildoor application.
type Config struct {
	BuilderPrivkey string `yaml:"builder_privkey" json:"builder_privkey,omitempty"`
	// BuilderMnemonic, when set, derives the builder BLS key from this BIP-39 mnemonic and
	// BuilderKeyIndex using the standard validator key path m/12381/3600/{index}/0/0.
	// Mutually exclusive with BuilderPrivkey. json:"-" keeps the secret out of every JSON
	// serialization path (WebUI REST + SSE); YAML config loading is unaffected.
	BuilderMnemonic   string           `yaml:"builder_mnemonic" json:"-"`
	BuilderKeyIndex   uint64           `yaml:"builder_key_index" json:"builder_key_index"`
	CLClient          string           `yaml:"cl_client" json:"cl_client,omitempty"`
	ELEngineAPI       string           `yaml:"el_engine_api" json:"el_engine_api,omitempty"`   // Engine API URL (required for payload building)
	ELJWTSecret       string           `yaml:"el_jwt_secret" json:"el_jwt_secret,omitempty"`   // Path to JWT secret file for engine API auth
	ELRPC             string           `yaml:"el_rpc" json:"el_rpc,omitempty"`                 // Optional: EL JSON-RPC for transactions (lifecycle only)
	WalletPrivkey     string           `yaml:"wallet_privkey" json:"wallet_privkey,omitempty"` // Optional: only if lifecycle enabled
	APIPort           int              `yaml:"api_port" json:"api_port"`                       // Optional, 0 = disabled
	AuthProviderURL   string           `yaml:"auth_provider_url" json:"auth_provider_url"`     // Optional: authenticatoor URL; when set, API requests must carry a JWT verified against the authenticatoor's JWKS. When empty, the API is unauthenticated.
	InjectHeadHTML    string           `yaml:"inject_head_html" json:"inject_head_html"`       // Optional: raw HTML snippet (e.g. analytics tags) injected into <head> of the served SPA. Falls back to BUILDOOR_INJECT_HEAD_HTML env var when empty.
	OverviewURL       string           `yaml:"overview_url" json:"overview_url"`               // Optional: URL of the multi-instance overview UI. When set, the dashboard renders an "Overview" entry in the top nav so operators get consistent navigation across instances.
	LifecycleEnabled  bool             `yaml:"lifecycle_enabled" json:"lifecycle_enabled"`
	EPBSEnabled       bool             `yaml:"epbs_enabled" json:"epbs_enabled"`               // Initial enabled state for ePBS (service available if Gloas fork is scheduled)
	BuilderAPIEnabled bool             `yaml:"builder_api_enabled" json:"builder_api_enabled"` // Initial enabled state for Builder API
	BuilderAPI        BuilderAPIConfig `yaml:"builder_api" json:"builder_api"`                 // Builder API configuration
	DepositAmount     uint64           `yaml:"deposit_amount" json:"deposit_amount"`           // Gwei, default 10 ETH
	TopupThreshold    uint64           `yaml:"topup_threshold" json:"topup_threshold"`         // Gwei
	TopupAmount       uint64           `yaml:"topup_amount" json:"topup_amount"`               // Gwei
	DepositMaxFeeGwei uint64           `yaml:"deposit_max_fee" json:"deposit_max_fee"`
	Schedule          ScheduleConfig   `yaml:"schedule" json:"schedule"`
	EPBS              EPBSConfig       `yaml:"epbs" json:"epbs"`     // Time-scheduled ePBS config
	Reveal            RevealConfig     `yaml:"reveal" json:"reveal"` // Payload reveal config (shared by p2p bidder + Builder API)
	Debug             bool             `yaml:"debug" json:"debug"`
	Pprof             bool             `yaml:"pprof" json:"pprof"`
	PayloadBuildTime  uint64           `yaml:"payload_build_time" json:"payload_build_time"` // The time given to the EL to build the payload after triggering the payload build via fcu (in ms)
	// ExtraData is the prefix injected into the built payload's extra-data field
	// (then padded with the EL's original extra data, truncated to 32 bytes). Used
	// to mark blocks built by this builder. Defaulted to "buildoor/" when empty.
	ExtraData       string                `yaml:"extra_data" json:"extra_data"`
	ValidatorRanges ValidatorRangesConfig `yaml:"validator_ranges" json:"validator_ranges"`
	// SlotResultRetentionEpochs is how many epochs of per-slot result history
	// (plans + outcome summaries) are kept before pruning, in memory and in the
	// state-db. Must be > 0.
	SlotResultRetentionEpochs uint64 `yaml:"slot_result_retention_epochs" json:"slot_result_retention_epochs"`
	// SlotArtifactRetentionEpochs is how many epochs of raw SSZ artifacts
	// (payloads, signed bids, envelopes) are kept in the slot_artifacts table.
	// Raw payloads dominate disk usage — lower this on disk-sensitive
	// deployments. Must be > 0.
	SlotArtifactRetentionEpochs uint64 `yaml:"slot_artifact_retention_epochs" json:"slot_artifact_retention_epochs"`
	// SlotArtifactCaptureEnabled toggles raw SSZ artifact capture. Result
	// summaries are recorded regardless.
	SlotArtifactCaptureEnabled bool `yaml:"slot_artifact_capture_enabled" json:"slot_artifact_capture_enabled"`
	// StateDBPath, when set, enables the optional SQLite state-db at this path.
	// It persists UI setting overrides, won blocks, validator registrations,
	// proposer preferences and an audit log across restarts. Startup-only and
	// never itself persisted. Empty disables persistence (in-memory only).
	StateDBPath string `yaml:"state_db" json:"state_db,omitempty"`
}

// ScheduleConfig defines when the builder should build blocks.
type ScheduleConfig struct {
	Mode      ScheduleMode `yaml:"mode" json:"mode"`             // all, every_nth, next_n
	EveryNth  uint64       `yaml:"every_nth" json:"every_nth"`   // For every_nth mode
	NextN     uint64       `yaml:"next_n" json:"next_n"`         // For next_n mode
	StartSlot uint64       `yaml:"start_slot" json:"start_slot"` // Optional start slot
}

// ScheduleMode represents the scheduling strategy for block building.
type ScheduleMode string

const (
	// ScheduleModeAll builds for all slots.
	ScheduleModeAll ScheduleMode = "all"
	// ScheduleModeEveryN builds for every Nth slot.
	ScheduleModeEveryN ScheduleMode = "every_nth"
	// ScheduleModeNextN builds for the next N slots then stops.
	ScheduleModeNextN ScheduleMode = "next_n"
)

// BuilderAPIConfig defines configuration for the traditional Builder API (pre-ePBS).
type BuilderAPIConfig struct {
	// BuilderURL is this builder's publicly reachable URL (e.g. "https://builder.example.com").
	// Used to verify the auth.message.data field (set to the builder URL) in
	// SignedRequestAuthV1 messages from proposers. If empty, this validation is skipped.
	BuilderURL string `yaml:"builder_url" json:"builder_url"`

	// RequireRequestAuth controls whether a SignedRequestAuthV1 body is mandatory on
	// getExecutionPayloadBid requests. When true, requests without an auth body are
	// rejected with 401. When false (default), auth is optional — but if supplied it
	// is always fully validated.
	RequireRequestAuth bool `yaml:"require_request_auth" json:"require_request_auth"`

	// BlockValueSubsidyGwei is added to the bid value so the proposer sees a higher bid:
	// to the getHeader bid value in the Fulu Builder API, and to the block value that
	// forms bid.ExecutionPayment/Value in Gloas getExecutionPayloadBid calls.
	BlockValueSubsidyGwei uint64 `yaml:"block_value_subsidy_gwei" json:"block_value_subsidy_gwei"`

	// ValueOverrideGwei, when non-zero, replaces the served bid's total value
	// (block value + subsidy) with this absolute amount in gwei — an alternative
	// to the subsidy for testing. Per-slot action plans override this per slot.
	ValueOverrideGwei uint64 `yaml:"value_override_gwei" json:"value_override_gwei"`
}

// EPBSConfig defines time-scheduled bidding parameters for ePBS.
type EPBSConfig struct {
	// BuildStartTime is milliseconds relative to the proposal slot start when we
	// start building. Negative values mean before the slot starts (e.g. -3000 =
	// 3 seconds before slot start). Positive values mean after slot start.
	// Set to 0 to build immediately when payload_attributes is received.
	// Default: -3000.
	BuildStartTime int64 `yaml:"build_start_time" json:"build_start_time"`

	// BidStartTime is milliseconds relative to slot start for first bid.
	// Can be negative to bid before slot starts.
	BidStartTime int64 `yaml:"bid_start_time" json:"bid_start_time"`

	// BidEndTime is milliseconds relative to slot start for last bid.
	BidEndTime int64 `yaml:"bid_end_time" json:"bid_end_time"`

	// BidMinAmount is the minimum bid amount in gwei.
	// Bids use max(blockValue, BidMinAmount) as the starting bid value.
	BidMinAmount uint64 `yaml:"bid_min_amount" json:"bid_min_amount"`

	// BidIncrease is the amount to increase bid per subsequent bid in gwei.
	BidIncrease uint64 `yaml:"bid_increase" json:"bid_increase"`

	// BidInterval is milliseconds between bids. 0 means single bid.
	BidInterval int64 `yaml:"bid_interval" json:"bid_interval"`

	// BidSubsidy is added to every bid in gwei so the bid clears the proposer's
	// local-EL threshold (the BN otherwise self-builds when its local EL value is higher).
	BidSubsidy uint64 `yaml:"bid_subsidy" json:"bid_subsidy"`

	// BidValueOverride, when non-zero, replaces the bid base value
	// (max(blockValue, BidMinAmount) + BidSubsidy) with this absolute amount in
	// gwei — an alternative to the subsidy for testing; allows underbidding the
	// block value. BidIncrease still applies per subsequent bid. Per-slot action
	// plans override this per slot.
	BidValueOverride uint64 `yaml:"bid_value_override" json:"bid_value_override"`

	// HeadVoteThresholdPct is the head-vote participation threshold in percent
	// (0-100) the vote tracker reports against: crossing it fires an immediate
	// update with threshold_met set. 0 disables threshold checking. The default
	// (60) mirrors the Gloas builder payment quorum
	// (BUILDER_PAYMENT_THRESHOLD_NUMERATOR/DENOMINATOR = 6/10) — the
	// participation level at which the builder's payment actually settles.
	HeadVoteThresholdPct uint64 `yaml:"head_vote_threshold_pct" json:"head_vote_threshold_pct"`
}

// Reveal gate modes: how the reveal moment of a won slot is decided.
const (
	// RevealGateTime reveals at TimeMs into the slot.
	RevealGateTime = "time"
	// RevealGateVote reveals as soon as head-vote participation on the
	// committing block reaches VoteThresholdPct.
	RevealGateVote = "vote"
	// RevealGateVoteOrTime reveals at whichever gate opens first.
	RevealGateVoteOrTime = "vote_or_time"
	// RevealGateVoteAndTime reveals at TimeMs, but only once the vote
	// threshold is also reached (whichever happens last).
	RevealGateVoteAndTime = "vote_and_time"
)

// Broadcast validation levels for the envelope submission API
// (beacon-API broadcast_validation query parameter).
const (
	BroadcastValidationGossip                   = "gossip"
	BroadcastValidationConsensus                = "consensus"
	BroadcastValidationConsensusAndEquivocation = "consensus_and_equivocation"
)

// RevealConfig defines the payload reveal behaviour shared by both flows
// (p2p ePBS bidding and the Builder API): the reveal service publishes every
// won slot's envelope according to these settings, per-slot overridable via
// the action plan's reveal category.
type RevealConfig struct {
	// Enabled globally enables payload reveals. A plan-custom slot still
	// force-activates its reveal (mirroring the bid/builder_api categories).
	Enabled bool `yaml:"enabled" json:"enabled"`

	// GateMode decides the reveal moment: time | vote | vote_or_time |
	// vote_and_time (see the RevealGate* constants). Unknown values fall
	// back to time.
	GateMode string `yaml:"gate_mode" json:"gate_mode"`

	// TimeMs is milliseconds relative to slot start for the time gate.
	// 0 = auto-compute from slot time (see ApplySlotDefaults).
	TimeMs int64 `yaml:"time_ms" json:"time_ms"`

	// VoteThresholdPct is the head-vote participation (percent of the slot's
	// attesting balance on the committing block) that opens the vote gate.
	VoteThresholdPct uint64 `yaml:"vote_threshold_pct" json:"vote_threshold_pct"`

	// BroadcastValidation is the validation level the beacon node must apply
	// before broadcasting the envelope: gossip (default) | consensus |
	// consensus_and_equivocation (recommended for builders against
	// unbundling via equivocating blocks). Unknown values fall back to
	// gossip.
	BroadcastValidation string `yaml:"broadcast_validation" json:"broadcast_validation"`

	// MaxAttempts is the total number of publish attempts per reveal.
	MaxAttempts uint64 `yaml:"max_attempts" json:"max_attempts"`

	// RetryIntervalMs is the wait between failed publish attempts.
	RetryIntervalMs int64 `yaml:"retry_interval_ms" json:"retry_interval_ms"`
}

// NormalizedGateMode returns the gate mode, falling back to RevealGateTime
// for unknown values (UI overrides are free-form strings).
func (c *RevealConfig) NormalizedGateMode() string {
	switch c.GateMode {
	case RevealGateTime, RevealGateVote, RevealGateVoteOrTime, RevealGateVoteAndTime:
		return c.GateMode
	default:
		return RevealGateTime
	}
}

// NormalizedBroadcastValidation returns the broadcast validation level,
// falling back to gossip for unknown values.
func (c *RevealConfig) NormalizedBroadcastValidation() string {
	switch c.BroadcastValidation {
	case BroadcastValidationGossip, BroadcastValidationConsensus,
		BroadcastValidationConsensusAndEquivocation:
		return c.BroadcastValidation
	default:
		return BroadcastValidationGossip
	}
}

// BuilderState represents the current state of a builder in the beacon chain.
type BuilderState struct {
	Pubkey            []byte
	Index             uint64
	IsRegistered      bool
	Balance           uint64 // Gwei
	DepositEpoch      uint64
	WithdrawableEpoch uint64
}

// BuilderStats tracks statistics for builder operations.
type BuilderStats struct {
	SlotsBuilt     uint64
	BidsSubmitted  uint64
	BidsWon        uint64
	BlocksIncluded uint64 // Blocks where our payload was included
	TotalPaid      uint64 // Gwei paid for won bids
	RevealsSuccess uint64
	RevealsFailed  uint64
	RevealsSkipped uint64
}
