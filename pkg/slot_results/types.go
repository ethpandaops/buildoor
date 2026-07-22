// Package slot_results implements the generic per-slot outcome history: one
// attempt-aware SlotResult for every slot where ePBS or the Builder API was
// active (build lifecycle, bid attempts, block submissions, reveal attempts,
// inclusion), plus the raw SSZ artifacts (payloads, signed bids, envelopes)
// produced along the way. The frozen action plan is snapshotted into each
// record so later plan or config changes never rewrite history.
package slot_results

import (
	"maps"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
)

// BuildStatus is the lifecycle state of a slot's payload build.
type BuildStatus string

// Build lifecycle statuses.
const (
	BuildStatusWaitingAttributes BuildStatus = "waiting_attributes"
	BuildStatusNoAttributes      BuildStatus = "no_attributes"
	BuildStatusStarted           BuildStatus = "started"
	BuildStatusReady             BuildStatus = "ready"
	BuildStatusFailed            BuildStatus = "failed"
	BuildStatusSkipped           BuildStatus = "skipped"
)

// BidStatus is the outcome of one bid attempt (p2p or Builder API).
type BidStatus string

// Bid attempt statuses.
const (
	BidStatusSuppressed  BidStatus = "suppressed"
	BidStatusConstructed BidStatus = "constructed"
	BidStatusSubmitted   BidStatus = "submitted"
	BidStatusServed      BidStatus = "served"
	BidStatusFailed      BidStatus = "failed"
	BidStatusCancelled   BidStatus = "cancelled"
)

// SubmissionStatus is the outcome of a proposer block submission.
type SubmissionStatus string

// Block submission statuses.
const (
	SubmissionStatusReceived SubmissionStatus = "received"
	SubmissionStatusAccepted SubmissionStatus = "accepted"
	SubmissionStatusFailed   SubmissionStatus = "failed"
)

// RevealStatus is the outcome of one reveal attempt.
type RevealStatus string

// Reveal attempt statuses.
const (
	RevealStatusSuppressed RevealStatus = "suppressed"
	RevealStatusPublished  RevealStatus = "published"
	RevealStatusFailed     RevealStatus = "failed"
	RevealStatusSkipped    RevealStatus = "skipped"
)

// PayloadStatus tracks whether a won slot's payload became canonical.
type PayloadStatus string

// Payload canonical statuses (Gloas+; decided by the canonical chain's first
// block after the won slot and revised on reorgs while the slot is inside
// the inclusion tracker's reorg window).
const (
	// PayloadStatusPending: won, but no follow-up block seen yet.
	PayloadStatusPending PayloadStatus = "pending"
	// PayloadStatusCanonical: the next canonical block builds on our payload.
	PayloadStatusCanonical PayloadStatus = "canonical"
	// PayloadStatusMissed: the won block is canonical but the next block
	// builds on an older execution block — the payload was withheld, revealed
	// too late, or voted empty.
	PayloadStatusMissed PayloadStatus = "missed"
	// PayloadStatusOrphaned: the won beacon block itself was reorged out of
	// the canonical chain.
	PayloadStatusOrphaned PayloadStatus = "orphaned"
)

// maxAttemptsPerKind caps the retained attempts per kind on one slot result;
// beyond the cap the DroppedAttempts counter grows instead (protects against
// unauthenticated Builder API traffic growing a record without bound while
// preserving evidence of truncation).
const maxAttemptsPerKind = 256

// BuildOutcome is the slot's payload build state and, once ready, the built
// payload's summary.
type BuildOutcome struct {
	Status     BuildStatus `json:"status"`
	SkipReason string      `json:"skip_reason,omitempty"` // action_plan.BuildSkipReason* when skipped

	BlockHash       string `json:"block_hash,omitempty"`
	BlockValueWei   string `json:"block_value_wei,omitempty"`
	NumTransactions int    `json:"num_transactions,omitempty"`
	NumBlobs        int    `json:"num_blobs,omitempty"`
	FeeRecipient    string `json:"fee_recipient,omitempty"`

	// Full built-payload properties; the list fields (transactions, blobs,
	// withdrawals, execution requests) are aggregated to counts above/below.
	BlockNumber          uint64 `json:"block_number,omitempty"`
	ParentHash           string `json:"parent_hash,omitempty"`
	StateRoot            string `json:"state_root,omitempty"`
	ReceiptsRoot         string `json:"receipts_root,omitempty"`
	PrevRandao           string `json:"prev_randao,omitempty"`
	Timestamp            uint64 `json:"timestamp,omitempty"`
	GasLimit             uint64 `json:"gas_limit,omitempty"`
	GasUsed              uint64 `json:"gas_used,omitempty"`
	BaseFeePerGas        string `json:"base_fee_per_gas,omitempty"` // wei
	ExtraData            string `json:"extra_data,omitempty"`       // 0x-hex
	BlobGasUsed          uint64 `json:"blob_gas_used,omitempty"`
	ExcessBlobGas        uint64 `json:"excess_blob_gas,omitempty"`
	NumWithdrawals       int    `json:"num_withdrawals,omitempty"`
	NumExecutionRequests int    `json:"num_execution_requests,omitempty"`

	// Attributes is the payload_attributes snapshot the build ran on.
	Attributes *AttributesSnapshot `json:"attributes,omitempty"`

	Error string    `json:"error,omitempty"`
	At    time.Time `json:"at"`
}

// AttributesSnapshot captures the payload_attributes a build ran on (list
// fields aggregated to counts).
type AttributesSnapshot struct {
	ProposerIndex         uint64 `json:"proposer_index"`
	ParentBlockRoot       string `json:"parent_block_root"`
	ParentBlockHash       string `json:"parent_block_hash"`
	ParentBlockNumber     uint64 `json:"parent_block_number"`
	Timestamp             uint64 `json:"timestamp"`
	PrevRandao            string `json:"prev_randao"`
	SuggestedFeeRecipient string `json:"suggested_fee_recipient"`
	ParentBeaconBlockRoot string `json:"parent_beacon_block_root,omitempty"`
	TargetGasLimit        uint64 `json:"target_gas_limit,omitempty"`
	NumWithdrawals        int    `json:"num_withdrawals"`
	NumInclusionListTxs   int    `json:"num_inclusion_list_txs,omitempty"`
}

// BidAttempt is one bid we constructed, served, submitted — or failed to.
type BidAttempt struct {
	Status    BidStatus `json:"status"`
	Transport string    `json:"transport"` // payload_builder.BidTransport values

	TotalValueGwei       uint64 `json:"total_value_gwei"`
	ExecutionPaymentGwei uint64 `json:"execution_payment_gwei,omitempty"`

	// CompetitorHighGwei is the highest known competitor bid at submission
	// time (p2p only, our own builder index excluded).
	CompetitorHighGwei *uint64 `json:"competitor_high_gwei,omitempty"`

	// Full bid message properties (Gloas+ bids; blob commitments aggregated
	// to a count). Empty for legacy Builder API bids and pre-construction
	// failures.
	BlockHash          string `json:"block_hash,omitempty"`
	ParentBlockHash    string `json:"parent_block_hash,omitempty"`
	ParentBlockRoot    string `json:"parent_block_root,omitempty"`
	PrevRandao         string `json:"prev_randao,omitempty"`
	FeeRecipient       string `json:"fee_recipient,omitempty"`
	GasLimit           uint64 `json:"gas_limit,omitempty"`
	BuilderIndex       uint64 `json:"builder_index,omitempty"`
	NumBlobCommitments int    `json:"num_blob_commitments,omitempty"`

	// ArtifactIndex references the slot's stored 'bid' SSZ artifact; nil when
	// no artifact exists (suppressed, pre-construction failure, or capture
	// disabled/failed).
	ArtifactIndex *int `json:"artifact_index,omitempty"`

	Error string    `json:"error,omitempty"`
	At    time.Time `json:"at"`
}

// BlockSubmission is a proposer's block submission through the Builder API.
type BlockSubmission struct {
	Dialect string           `json:"dialect"` // "legacy" | "epbs"
	Status  SubmissionStatus `json:"status"`
	Error   string           `json:"error,omitempty"`
	At      time.Time        `json:"at"`
}

// RevealAttempt is one envelope reveal attempt (or its suppression/skip).
type RevealAttempt struct {
	Status     RevealStatus `json:"status"`
	Transport  string       `json:"transport"`
	SkipReason string       `json:"skip_reason,omitempty"` // plan_disabled | disabled | late | vote_gate_timeout
	Error      string       `json:"error,omitempty"`
	Attempt    int          `json:"attempt"`
	At         time.Time    `json:"at"`

	// StartedAt is when the attempt began (envelope construction + submit
	// call); nil on skips where nothing was attempted.
	StartedAt *time.Time `json:"started_at,omitempty"`
}

// InclusionResult records that the slot's payload was seen included at the
// head. Field semantics match the WonBlock wire shape (minus the slot).
type InclusionResult struct {
	Source          string    `json:"source"` // "epbs" | "builder_api"
	BlockHash       string    `json:"block_hash"`
	NumTransactions int       `json:"num_transactions"`
	NumBlobs        int       `json:"num_blobs"`
	ValueWei        string    `json:"value_wei"`
	ValueETH        string    `json:"value_eth"`
	Timestamp       time.Time `json:"timestamp"`

	// PayloadStatus is the canonical verdict for the won payload: pending
	// until the first follow-up block is seen, then canonical or missed based
	// on that block's committed parent execution hash. Pre-Gloas wins are
	// canonical immediately (the payload is embedded in the block).
	PayloadStatus PayloadStatus `json:"payload_status,omitempty"`
	// PayloadCheckSlot is the follow-up block's slot the verdict came from.
	PayloadCheckSlot phase0.Slot `json:"payload_check_slot,omitempty"`
}

// SlotResult is the complete recorded history of one slot. Values held by the
// tracker are immutable snapshots: every mutation clones, and every value
// crossing the package boundary is a clone.
type SlotResult struct {
	Slot  phase0.Slot `json:"slot"`
	Epoch uint64      `json:"epoch"`
	Fork  string      `json:"fork"`

	// AppliedPlan is the frozen plan snapshot the slot executed under.
	AppliedPlan *action_plan.FrozenPlan `json:"applied_plan,omitempty"`

	Build            *BuildOutcome     `json:"build,omitempty"`
	Bids             []BidAttempt      `json:"bids,omitempty"`
	BlockSubmissions []BlockSubmission `json:"block_submissions,omitempty"`
	RevealAttempts   []RevealAttempt   `json:"reveal_attempts,omitempty"`
	Inclusion        *InclusionResult  `json:"inclusion,omitempty"`

	// DroppedAttempts counts attempts beyond the per-kind retention cap,
	// keyed by kind ("bids", "block_submissions", "reveal_attempts").
	DroppedAttempts map[string]int `json:"dropped_attempts,omitempty"`

	UpdatedAt time.Time `json:"updated_at"`
}

// Clone returns a deep copy of the result.
func (r *SlotResult) Clone() *SlotResult {
	if r == nil {
		return nil
	}

	c := *r

	if r.Build != nil {
		build := *r.Build
		c.Build = &build
	}

	if r.Inclusion != nil {
		inclusion := *r.Inclusion
		c.Inclusion = &inclusion
	}

	if r.Bids != nil {
		c.Bids = make([]BidAttempt, len(r.Bids))
		for i, bid := range r.Bids {
			c.Bids[i] = bid
			if bid.CompetitorHighGwei != nil {
				v := *bid.CompetitorHighGwei
				c.Bids[i].CompetitorHighGwei = &v
			}

			if bid.ArtifactIndex != nil {
				v := *bid.ArtifactIndex
				c.Bids[i].ArtifactIndex = &v
			}
		}
	}

	if r.BlockSubmissions != nil {
		c.BlockSubmissions = make([]BlockSubmission, len(r.BlockSubmissions))
		copy(c.BlockSubmissions, r.BlockSubmissions)
	}

	if r.RevealAttempts != nil {
		c.RevealAttempts = make([]RevealAttempt, len(r.RevealAttempts))
		copy(c.RevealAttempts, r.RevealAttempts)
	}

	if r.DroppedAttempts != nil {
		c.DroppedAttempts = make(map[string]int, len(r.DroppedAttempts))
		maps.Copy(c.DroppedAttempts, r.DroppedAttempts)
	}

	// The frozen plan is immutable by contract; sharing the pointer is safe.

	return &c
}
