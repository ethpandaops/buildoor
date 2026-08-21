// Package config handles configuration loading and validation for buildoor.
package config

import "strings"

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
	BuilderMnemonic string `yaml:"builder_mnemonic" json:"-"`
	BuilderKeyIndex uint64 `yaml:"builder_key_index" json:"builder_key_index"`
	// BuilderKeys configures the managed set of internally derived builder keys
	// that the entry key (BuilderPrivkey / BuilderMnemonic+BuilderKeyIndex) roots.
	BuilderKeys       BuilderKeysConfig `yaml:"builder_keys" json:"builder_keys"`
	CLClient          string            `yaml:"cl_client" json:"cl_client,omitempty"`
	ELEngineAPI       string            `yaml:"el_engine_api" json:"el_engine_api,omitempty"`   // Engine API URL (required for payload building)
	ELJWTSecret       string            `yaml:"el_jwt_secret" json:"el_jwt_secret,omitempty"`   // Path to JWT secret file for engine API auth
	ELRPC             string            `yaml:"el_rpc" json:"el_rpc,omitempty"`                 // Optional: EL JSON-RPC for transactions (lifecycle only)
	WalletPrivkey     string            `yaml:"wallet_privkey" json:"wallet_privkey,omitempty"` // Optional: only if lifecycle enabled
	APIPort           int               `yaml:"api_port" json:"api_port"`                       // Optional, 0 = disabled
	AuthProviderURL   string            `yaml:"auth_provider_url" json:"auth_provider_url"`     // Optional: authenticatoor URL; when set, API requests must carry a JWT verified against the authenticatoor's JWKS. When empty, the API is unauthenticated.
	InjectHeadHTML    string            `yaml:"inject_head_html" json:"inject_head_html"`       // Optional: raw HTML snippet (e.g. analytics tags) injected into <head> of the served SPA. Falls back to BUILDOOR_INJECT_HEAD_HTML env var when empty.
	OverviewURL       string            `yaml:"overview_url" json:"overview_url"`               // Optional: URL of the multi-instance overview UI. When set, the dashboard renders an "Overview" entry in the top nav and the brand logo links back to the overview, so operators get consistent navigation across instances.
	LifecycleEnabled  bool              `yaml:"lifecycle_enabled" json:"lifecycle_enabled"`
	EPBSEnabled       bool              `yaml:"epbs_enabled" json:"epbs_enabled"`               // Initial enabled state for ePBS (service available if Gloas fork is scheduled)
	BuilderAPIEnabled bool              `yaml:"builder_api_enabled" json:"builder_api_enabled"` // Initial enabled state for Builder API
	BuilderAPI        BuilderAPIConfig  `yaml:"builder_api" json:"builder_api"`                 // Builder API configuration
	DepositAmount     uint64            `yaml:"deposit_amount" json:"deposit_amount"`           // Gwei, default 10 ETH
	TopupThreshold    uint64            `yaml:"topup_threshold" json:"topup_threshold"`         // Gwei
	TopupAmount       uint64            `yaml:"topup_amount" json:"topup_amount"`               // Gwei
	DepositMaxFeeGwei uint64            `yaml:"deposit_max_fee" json:"deposit_max_fee"`
	Schedule          ScheduleConfig    `yaml:"schedule" json:"schedule"`
	EPBS              EPBSConfig        `yaml:"epbs" json:"epbs"`     // Time-scheduled ePBS config
	Reveal            RevealConfig      `yaml:"reveal" json:"reveal"` // Payload reveal config (shared by p2p bidder + Builder API)
	Build             BuildConfig       `yaml:"build" json:"build"`   // Payload build candidate policy
	Debug             bool              `yaml:"debug" json:"debug"`
	Pprof             bool              `yaml:"pprof" json:"pprof"`
	PayloadBuildTime  uint64            `yaml:"payload_build_time" json:"payload_build_time"` // The time given to the EL to build the payload after triggering the payload build via fcu (in ms)
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

// BuilderKeysConfig defines the managed builder key set: how many keys buildoor
// keeps registered and funded, and how far the internal derivation may reach.
//
// Keys are derived from the entry key (see signer.DeriveInternalKey): internal
// index 0 is the entry key itself, so a TargetCount of 1 behaves exactly like a
// single-key deployment.
type BuilderKeysConfig struct {
	// TargetCount is the number of builder keys kept registered and funded.
	// Raising it deposits new keys; lowering it exits surplus keys when AutoExit
	// is on. 0 is treated as 1.
	TargetCount uint64 `yaml:"target_count" json:"target_count"`

	// MaxIndex caps internal derivation, bounding both the target and the
	// startup discovery scan.
	MaxIndex uint64 `yaml:"max_index" json:"max_index"`

	// DiscoveryGap is how many consecutive never-used indices end the startup
	// scan for previously deposited keys.
	DiscoveryGap uint64 `yaml:"discovery_gap" json:"discovery_gap"`

	// AutoDeposit deposits new keys to reach TargetCount.
	AutoDeposit bool `yaml:"auto_deposit" json:"auto_deposit"`

	// AutoExit exits surplus keys when the managed count exceeds TargetCount.
	// Irreversible: an exited builder key cannot be reactivated until its
	// registry entry is reused by another builder's deposit.
	AutoExit bool `yaml:"auto_exit" json:"auto_exit"`
}

// EffectiveTargetCount returns the target key count clamped into the usable
// range: at least one key, and never more than the derivation cap allows
// (indices 0..MaxIndex, so MaxIndex+1 keys).
func (c *BuilderKeysConfig) EffectiveTargetCount() uint64 {
	target := max(c.TargetCount, 1)

	if c.MaxIndex > 0 && target > c.MaxIndex+1 {
		return c.MaxIndex + 1
	}

	return target
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

	// ServeCandidates controls which built candidate payloads bid requests may
	// be answered from: "all" (default; serve whichever candidate matches the
	// requested parent), "canonical_only" (only parent_full and unclassified
	// payloads), or a comma-separated list of candidate keys.
	ServeCandidates string `yaml:"serve_candidates" json:"serve_candidates"`

	// OnDemandBuild builds a payload on the fly when a bid request asks for a
	// legal parent tuple no candidate covers yet (bounded by the request's
	// response budget).
	OnDemandBuild bool `yaml:"on_demand_build" json:"on_demand_build"`

	// KeyStrategy selects which managed builder key signs served bids. Unlike
	// p2p gossip there is no first-seen rule here, but the choice is sticky per
	// (slot, parent tuple) so a polling proposer keeps seeing the same builder.
	// Empty falls back to the ePBS key strategy.
	KeyStrategy string `yaml:"key_strategy" json:"key_strategy"`
}

// ServeCandidateAllowed reports whether the given serve policy allows
// answering from a payload classified as the given candidate key ("" =
// unclassified, always allowed under "all" and "canonical_only").
func ServeCandidateAllowed(policy, key string) bool {
	switch policy {
	case "", "all":
		return true
	case "canonical_only":
		return key == "" || key == "parent_full"
	default:
		if key == "" {
			return false
		}

		for _, allowed := range strings.Split(policy, ",") {
			if strings.TrimSpace(allowed) == key {
				return true
			}
		}

		return false
	}
}

// ServeCandidateAllowed applies the config's own serve policy.
func (c *BuilderAPIConfig) ServeCandidateAllowed(key string) bool {
	return ServeCandidateAllowed(c.ServeCandidates, key)
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

	// BidCandidate selects which built candidate payload the p2p bids commit
	// to: "auto" (default; match the chain view's current head and payload
	// status), a specific candidate key (parent_full, parent_empty,
	// grandparent_full, grandparent_empty), or "all" (gossip a bid for every
	// built candidate — deliberate multi-parent bidding for gossip testing;
	// most nodes propagate only a builder's first bid per slot).
	BidCandidate string `yaml:"bid_candidate" json:"bid_candidate"`

	// KeyStrategy selects which managed builder key signs each of a slot's
	// bids: round_robin (default), single, random or least_used. Each key bids
	// at most once per slot — the gossip rules ignore a builder's later bids —
	// so with several built candidates the strategy decides which keys cover
	// them.
	KeyStrategy string `yaml:"key_strategy" json:"key_strategy"`

	// BidKeysPerSlot caps how many distinct builder keys bid a slot. 0 means
	// no cap beyond the fleet itself; 1 reproduces single-key behaviour (one
	// gossiped bid per slot) regardless of how many candidates were built.
	BidKeysPerSlot uint64 `yaml:"bid_keys_per_slot" json:"bid_keys_per_slot"`

	// BidKeysPerStep is how many keys bid a payload per interval step, each
	// one value-increment higher than the last. 1 (default) walks the fleet up
	// the interval ladder one key at a time; 0 spends every remaining key at
	// once, so a whole slot can be bid from all active keys in parallel as
	// soon as the bid window opens.
	BidKeysPerStep uint64 `yaml:"bid_keys_per_step" json:"bid_keys_per_step"`

	// BidCandidateSwitch allows the auto selection to switch to a different
	// candidate mid-slot when the chain view changes. Default off: the first
	// gossiped candidate sticks (the gossip first-seen rule makes a switched
	// bid unlikely to propagate anyway).
	BidCandidateSwitch bool `yaml:"bid_candidate_switch" json:"bid_candidate_switch"`

	// HeadVoteThresholdPct is the head-vote participation threshold in percent
	// (0-100) the vote tracker reports against: crossing it fires an immediate
	// update with threshold_met set. 0 disables threshold checking. The default
	// (60) mirrors the Gloas builder payment quorum
	// (BUILDER_PAYMENT_THRESHOLD_NUMERATOR/DENOMINATOR = 6/10) — the
	// participation level at which the builder's payment actually settles.
	HeadVoteThresholdPct uint64 `yaml:"head_vote_threshold_pct" json:"head_vote_threshold_pct"`
}

// Candidate build modes: whether a build-parent candidate is built for a slot.
const (
	// CandidateModeAuto builds the candidate when live chain signals suggest
	// it may be needed (parent payload reveal status, parent block weakness).
	CandidateModeAuto = "auto"
	// CandidateModeAlways builds the candidate every scheduled slot.
	CandidateModeAlways = "always"
	// CandidateModeNever suppresses the candidate.
	CandidateModeNever = "never"
)

// NormalizedCandidateMode returns the candidate mode, falling back to the
// given default for unknown values (UI overrides are free-form strings).
func NormalizedCandidateMode(mode, fallback string) string {
	switch mode {
	case CandidateModeAuto, CandidateModeAlways, CandidateModeNever:
		return mode
	default:
		return fallback
	}
}

// BuildConfig defines which build-parent candidates are built per slot and how
// the engine builds are sequenced. Candidates name the parent tuple a payload
// extends: the head block ("parent") or its parent ("grandparent", a
// deliberate reorg), each on the committed payload ("full") or on the payload
// it built upon ("empty", the Gloas payload-miss case).
type BuildConfig struct {
	// CandidateParentFull: the normal build on the head block and its payload.
	CandidateParentFull string `yaml:"candidate_parent_full" json:"candidate_parent_full"`
	// CandidateParentEmpty: build on the head block but on its execution
	// parent (head payload treated as withheld). Gloas only.
	CandidateParentEmpty string `yaml:"candidate_parent_empty" json:"candidate_parent_empty"`
	// CandidateGrandparentFull: build on the head block's parent (reorg).
	CandidateGrandparentFull string `yaml:"candidate_grandparent_full" json:"candidate_grandparent_full"`
	// CandidateGrandparentEmpty: reorg combined with a withheld grandparent
	// payload. Gloas only.
	CandidateGrandparentEmpty string `yaml:"candidate_grandparent_empty" json:"candidate_grandparent_empty"`

	// Parallel runs the selected candidate builds concurrently against the
	// EL, each with its own payload ID (default). Disable it to serialize
	// them — the canonical candidate builds first so it keeps its scheduled
	// start, and the speculative ones follow.
	Parallel bool `yaml:"parallel" json:"parallel"`

	// SpeculativeBuildTimeMs, when non-zero, is the EL build time granted to
	// speculative (non-parent_full) candidates instead of PayloadBuildTime.
	SpeculativeBuildTimeMs uint64 `yaml:"speculative_build_time_ms" json:"speculative_build_time_ms"`

	// AutoWeakHeadPct is the head-vote participation (percent) below which
	// the head block counts as contested and auto-mode grandparent
	// candidates arm. 0 disables the weak-head signal.
	AutoWeakHeadPct uint64 `yaml:"auto_weak_head_pct" json:"auto_weak_head_pct"`

	// EnforceBidGasLimit adjusts the built payload's gas limit to the exact
	// value the bid gossip rules require (EL parent gas limit stepped toward
	// the proposer's target) when the EL ignored the target. Disabled by
	// default: the override rewrites the block header after building.
	EnforceBidGasLimit bool `yaml:"enforce_bid_gas_limit" json:"enforce_bid_gas_limit"`
}

// CandidateMode returns the normalized mode configured for the given
// candidate key ("" for unknown keys).
func (c *BuildConfig) CandidateMode(key string) string {
	switch key {
	case "parent_full":
		return NormalizedCandidateMode(c.CandidateParentFull, CandidateModeAlways)
	case "parent_empty":
		return NormalizedCandidateMode(c.CandidateParentEmpty, CandidateModeAuto)
	case "grandparent_full":
		return NormalizedCandidateMode(c.CandidateGrandparentFull, CandidateModeAuto)
	case "grandparent_empty":
		return NormalizedCandidateMode(c.CandidateGrandparentEmpty, CandidateModeNever)
	default:
		return ""
	}
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

	// RebindOnReorg re-binds a slot's reveal to a different beacon block when
	// the block the reveal was scheduled for is reorged out and our payload is
	// re-included under a sibling root: the envelope is rebuilt and re-signed
	// for the new root (the payload bytes are unchanged).
	RebindOnReorg bool `yaml:"rebind_on_reorg" json:"rebind_on_reorg"`
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
