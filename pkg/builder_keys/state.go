package builder_keys

import (
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
)

// Status is a builder key's lifecycle position, derived on every refresh from
// the beacon state, the pending-deposit queue and our own usage history.
type Status string

const (
	// StatusUnused: derived but never deposited. Available for a first deposit.
	StatusUnused Status = "unused"
	// StatusDepositing: a deposit was submitted (our own transaction or an entry
	// in the pending-deposit queue) but the key is not in the builder registry yet.
	StatusDepositing Status = "depositing"
	// StatusPending: registered, but the deposit epoch is not finalized, so the
	// key may not bid yet.
	StatusPending Status = "pending"
	// StatusActive: registered, finalized and not exiting — usable for bidding.
	StatusActive Status = "active"
	// StatusExiting: an exit was initiated; the withdrawable epoch is set but not
	// reached. The key can never be reactivated.
	StatusExiting Status = "exiting"
	// StatusExited: the withdrawable epoch has passed while the entry is still in
	// the registry.
	StatusExited Status = "exited"
	// StatusWithdrawn: the key was used before and its pubkey has left the builder
	// registry, so it is depositable again.
	StatusWithdrawn Status = "withdrawn"
)

// Managed reports whether the status counts toward the target key count: keys
// that are registered or on their way there. Exiting/exited keys do not count —
// they can never bid again, so the reconciler must replace them.
func (s Status) Managed() bool {
	switch s {
	case StatusDepositing, StatusPending, StatusActive:
		return true
	default:
		return false
	}
}

// Depositable reports whether a fresh deposit may be submitted for the key.
// Exited entries are excluded: per the Gloas spec a deposit cannot reactivate
// them, it is only swept back to the wallet.
func (s Status) Depositable() bool {
	return s == StatusUnused || s == StatusWithdrawn
}

// State is an immutable snapshot of one builder key. It doubles as the API wire
// shape for the builder keys endpoint and SSE event.
type State struct {
	KeyIndex uint64           `json:"key_index"`
	Pubkey   phase0.BLSPubKey `json:"-"`
	// PubkeyHex is the 0x-prefixed public key, for JSON consumers.
	PubkeyHex string `json:"pubkey"`
	Status    Status `json:"status"`

	// BuilderIndex is the on-chain registry index; only meaningful when
	// HasBuilderIndex is set (index 0 is a valid builder index).
	BuilderIndex    uint64 `json:"builder_index"`
	HasBuilderIndex bool   `json:"has_builder_index"`

	Balance           uint64 `json:"balance_gwei"`
	PendingPayments   uint64 `json:"pending_payments_gwei"`
	BalanceAdjustment int64  `json:"balance_adjustment_gwei"`
	// EffectiveBalance is the balance the key can actually bid against:
	// chain balance plus local adjustments minus pending payments.
	EffectiveBalance uint64 `json:"effective_balance_gwei"`

	DepositEpoch      uint64 `json:"deposit_epoch"`
	WithdrawableEpoch uint64 `json:"withdrawable_epoch"`

	// UseCount is how many deposit generations this key has gone through.
	UseCount      uint32 `json:"use_count"`
	LastDepositAt int64  `json:"last_deposit_at,omitempty"`
	LastExitAt    int64  `json:"last_exit_at,omitempty"`

	// LastTopupEpoch guards the per-key top-up cooldown (0 = never topped up).
	LastTopupEpoch phase0.Epoch `json:"last_topup_epoch,omitempty"`

	BidsSubmitted uint64 `json:"bids_submitted"`
	BidsWon       uint64 `json:"bids_won"`
}

// Ready reports whether the key may be used for a bid of the given value: it
// must be active on chain and able to cover the payment. A builder whose
// effective balance is below the bid value has its bid rejected by the
// consensus layer, so bidding from it is pure noise.
func (s *State) Ready(requiredGwei uint64) bool {
	return s.Status == StatusActive && s.EffectiveBalance >= requiredGwei
}

// Aggregate summarises the whole key set for the dashboard.
type Aggregate struct {
	Target  uint64 `json:"target"`
	Managed uint64 `json:"managed"`

	Unused     uint64 `json:"unused"`
	Depositing uint64 `json:"depositing"`
	Pending    uint64 `json:"pending"`
	Active     uint64 `json:"active"`
	Exiting    uint64 `json:"exiting"`
	Exited     uint64 `json:"exited"`
	Withdrawn  uint64 `json:"withdrawn"`

	TotalBalance         uint64 `json:"total_balance_gwei"`
	TotalPendingPayments uint64 `json:"total_pending_payments_gwei"`
	TotalEffective       uint64 `json:"total_effective_gwei"`
}
