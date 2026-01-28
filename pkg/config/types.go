// Package config handles configuration loading and validation for buildoor.
package config

// Config represents the complete configuration for the buildoor application.
type Config struct {
	BuilderPrivkey      string         `yaml:"builder_privkey" json:"builder_privkey,omitempty"`
	CLClient            string         `yaml:"cl_client" json:"cl_client,omitempty"`
	ELEngineAPI         string         `yaml:"el_engine_api" json:"el_engine_api,omitempty"`   // Engine API URL (required for payload building)
	ELJWTSecret         string         `yaml:"el_jwt_secret" json:"el_jwt_secret,omitempty"`   // Path to JWT secret file for engine API auth
	ELRPC               string         `yaml:"el_rpc" json:"el_rpc,omitempty"`                 // Optional: EL JSON-RPC for transactions (lifecycle only)
	WalletPrivkey       string         `yaml:"wallet_privkey" json:"wallet_privkey,omitempty"` // Optional: only if lifecycle enabled
	APIPort             int            `yaml:"api_port" json:"api_port"`                       // Optional, 0 = disabled
	APIUserHeader       string         `yaml:"api_user_header" json:"api_user_header"`         // Optional: header to use for authentication
	APITokenKey         string         `yaml:"api_token_key" json:"api_token_key"`             // Optional: key to use for API token authentication
	LifecycleEnabled    bool           `yaml:"lifecycle_enabled" json:"lifecycle_enabled"`
	EPBSEnabled         bool           `yaml:"epbs_enabled" json:"epbs_enabled"`       // Enable ePBS bidding/revealing
	DepositAmount       uint64         `yaml:"deposit_amount" json:"deposit_amount"`   // Gwei, default 10 ETH
	TopupThreshold      uint64         `yaml:"topup_threshold" json:"topup_threshold"` // Gwei
	TopupAmount         uint64         `yaml:"topup_amount" json:"topup_amount"`       // Gwei
	Schedule            ScheduleConfig `yaml:"schedule" json:"schedule"`
	EPBS                EPBSConfig     `yaml:"epbs" json:"epbs"` // Time-scheduled ePBS config
	Debug               bool           `yaml:"debug" json:"debug"`
	Pprof               bool           `yaml:"pprof" json:"pprof"`
	ValidateWithdrawals bool           `yaml:"validate_withdrawals" json:"validate_withdrawals"` // Validate expected vs actual withdrawals
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

	// RevealTime is milliseconds relative to slot start for reveal.
	RevealTime int64 `yaml:"reveal_time" json:"reveal_time"`

	// BidMinAmount is the minimum bid amount in gwei.
	BidMinAmount uint64 `yaml:"bid_min_amount" json:"bid_min_amount"`

	// BidIncrease is the amount to increase bid per subsequent bid in gwei.
	BidIncrease uint64 `yaml:"bid_increase" json:"bid_increase"`

	// BidInterval is milliseconds between bids. 0 means single bid.
	BidInterval int64 `yaml:"bid_interval" json:"bid_interval"`
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
