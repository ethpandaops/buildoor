package lifecycle

// BuilderState represents the current state of a builder in the beacon chain.
type BuilderState struct {
	Pubkey            []byte
	Index             uint64
	IsRegistered      bool
	Balance           uint64 // Gwei
	DepositEpoch      uint64
	WithdrawableEpoch uint64
}
