package builder

import "github.com/ethpandaops/buildoor/pkg/config"

// Type aliases for backwards compatibility - prefer using config package directly
type (
	Config              = config.Config
	ScheduleConfig      = config.ScheduleConfig
	ScheduleMode        = config.ScheduleMode
	EPBSConfig          = config.EPBSConfig
	LegacyBuilderConfig = config.LegacyBuilderConfig
)

// Constants aliases for backwards compatibility
const (
	ScheduleModeAll    = config.ScheduleModeAll
	ScheduleModeEveryN = config.ScheduleModeEveryN
	ScheduleModeNextN  = config.ScheduleModeNextN
)

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
