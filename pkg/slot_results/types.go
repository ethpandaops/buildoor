// Package slot_results implements the generic per-slot outcome history: one
// attempt-aware SlotResult for every slot where ePBS or the Builder API was
// active (build lifecycle, bid attempts, block submissions, reveal attempts,
// inclusion), plus the raw SSZ artifacts (payloads, signed bids, envelopes)
// produced along the way. The frozen action plan is snapshotted into each
// record so later plan or config changes never rewrite history.
package slot_results

import (
	"maps"
	"slices"
	"time"

	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// BuildStatus is the slot's build state: the baseline before any target ran
// and, once targets ran, what the primary target came to.
type BuildStatus string

// Slot build statuses.
const (
	BuildStatusWaitingAttributes BuildStatus = "waiting_attributes"
	BuildStatusNoAttributes      BuildStatus = "no_attributes"
	BuildStatusSkipped           BuildStatus = "skipped"
	// BuildStatusBuilding: the primary target runs and selected nothing yet.
	BuildStatusBuilding BuildStatus = "building"
	// BuildStatusReady: the primary target selected a payload.
	BuildStatusReady BuildStatus = "ready"
	// BuildStatusFailed: no target selected a payload.
	BuildStatusFailed BuildStatus = "failed"
)

// TargetStatus is one build target's state.
type TargetStatus string

// Build target statuses.
const (
	// TargetBuilding: started, nothing selected yet.
	TargetBuilding TargetStatus = "building"
	// TargetSelected: a payload feeds the target's bids (Selections).
	TargetSelected TargetStatus = "selected"
	// TargetNoPayload: the builds ended without a payload to select.
	TargetNoPayload TargetStatus = "no_payload"
	// TargetSuperseded: the target ran from synthesized attributes the beacon
	// node contradicted; its payloads were withdrawn.
	TargetSuperseded TargetStatus = "superseded"
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

// SlotBuild is the slot's build lifecycle: the baseline before any target
// ran (waiting for attributes, none arrived, skipped) and the build targets
// the builder ran, each with its own builds and selections.
type SlotBuild struct {
	Status     BuildStatus `json:"status"`
	SkipReason string      `json:"skip_reason,omitempty"` // action_plan.BuildSkipReason* when skipped
	// Error is the primary target's error when the slot failed.
	Error   string         `json:"error,omitempty"`
	Targets []*BuildTarget `json:"targets,omitempty"`
	At      time.Time      `json:"at"`
}

// BuildTarget is one (slot, parent tuple) the builder ran builds for: the
// engine build, the testing runs and the selections among their payloads.
// A build references its payload by block hash (SlotResult.Payloads), and a
// selection names the payload that fed the bids from then on.
type BuildTarget struct {
	// Seq identifies the target's events (payload_builder
	// BuildTargetEvent.Seq). A slot's candidate can be built more than once
	// (a superseded target and its replacement), so events of an older
	// target never touch a newer one. In-memory only: seqs restart with the
	// process.
	Seq uint64 `json:"-"`

	// Candidate classifies the parent tuple (parent_full, parent_empty,
	// grandparent_full, grandparent_empty; empty = unclassified).
	Candidate string       `json:"candidate,omitempty"`
	Status    TargetStatus `json:"status"`
	Error     string       `json:"error,omitempty"`

	// Attributes is the payload_attributes snapshot the target built from.
	Attributes *AttributesSnapshot `json:"attributes,omitempty"`

	// Testing request: the testing builds' tx and payload source, or why
	// none ran although the slot asked for them.
	TxSource          string `json:"tx_source,omitempty"`
	PayloadSource     string `json:"payload_source,omitempty"`
	TestingSkipReason string `json:"testing_skip_reason,omitempty"`

	Engine     *BuildRecord   `json:"engine,omitempty"`
	Testing    []*BuildRecord `json:"testing,omitempty"`
	Selections []Selection    `json:"selections,omitempty"`

	StartedAt time.Time `json:"started_at"`
	At        time.Time `json:"at"`
}

// BuildRecord is one build of a target: the engine build or one testing run.
type BuildRecord struct {
	Kind       string `json:"kind"` // payload_builder.BuildKind*
	Run        int    `json:"run,omitempty"`
	Status     string `json:"status"` // started/building, ready, failed, skipped
	SkipReason string `json:"skip_reason,omitempty"`
	Error      string `json:"error,omitempty"`
	// PayloadHash names the build's own payload (ready).
	PayloadHash string `json:"payload_hash,omitempty"`
	// Calls times the engine build's calls.
	Calls *EngineCalls `json:"calls,omitempty"`
	// Info is the testing run's transaction assembly.
	Info *payload_builder.LocalBuildInfo `json:"info,omitempty"`

	StartedAt time.Time `json:"started_at"`
	At        time.Time `json:"at"`
}

// EngineCalls are the engine build's call times.
type EngineCalls struct {
	ForkchoiceStart time.Time `json:"forkchoice_start"`
	ForkchoiceEnd   time.Time `json:"forkchoice_end"`
	GetPayloadStart time.Time `json:"get_payload_start"`
	GetPayloadEnd   time.Time `json:"get_payload_end"`
}

// Selection is one change of a target's pick: the payload the cache hands
// to the consumers from At on. What a consumer used is its own record (a
// bid's block hash).
type Selection struct {
	PayloadHash string                   `json:"payload_hash"`
	Build       payload_builder.BuildRef `json:"build"`
	Reason      string                   `json:"reason"` // first | better
	// Fallback marks an engine payload picked because no testing payload the
	// payload source asked for existed (yet).
	Fallback bool      `json:"fallback,omitempty"`
	At       time.Time `json:"at"`
}

// PayloadRecord is one payload built for the slot, recorded once under its
// block hash: which build produced it and its properties (list fields
// aggregated to counts). Written once, never modified.
type PayloadRecord struct {
	BlockHash string                   `json:"block_hash"`
	Build     payload_builder.BuildRef `json:"build"`
	Candidate string                   `json:"candidate,omitempty"`
	// Captured marks that the payload is stored as an artifact (the engine
	// payload and every selected one).
	Captured bool `json:"captured,omitempty"`

	BlockValueWei   string `json:"block_value_wei,omitempty"`
	NumTransactions int    `json:"num_transactions"`
	NumBlobs        int    `json:"num_blobs,omitempty"`
	FeeRecipient    string `json:"fee_recipient,omitempty"`

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

	// InclusionList is the inclusion list view (FOCIL, Heze) the payload
	// was built against.
	InclusionList *payload_builder.InclusionListBuild `json:"inclusion_list,omitempty"`

	BuiltAt time.Time `json:"built_at"`
}

// Primary returns the slot's primary target: the most canonical one with a
// selection, else the most canonical running one, else the newest.
func (b *SlotBuild) Primary() *BuildTarget {
	if b == nil || len(b.Targets) == 0 {
		return nil
	}

	var best *BuildTarget

	bestRank := 0

	for _, target := range b.Targets {
		rank := targetRank(target)
		if best == nil || rank < bestRank {
			best, bestRank = target, rank
		}
	}

	return best
}

// targetRank orders targets for Primary (lower = more primary).
func targetRank(target *BuildTarget) int {
	candidate, classified := buildCandidatePriority[target.Candidate]
	if !classified {
		candidate = len(buildCandidatePriority)
	}

	switch target.Status {
	case TargetSelected:
		return candidate
	case TargetBuilding:
		return 10 + candidate
	case TargetNoPayload:
		return 20 + candidate
	default: // superseded
		return 30 + candidate
	}
}

// buildCandidatePriority orders candidate keys from most to least canonical.
var buildCandidatePriority = map[string]int{
	"parent_full":       0,
	"parent_empty":      1,
	"grandparent_full":  2,
	"grandparent_empty": 3,
}

// LastSelection returns the target's current selection (nil when none).
func (t *BuildTarget) LastSelection() *Selection {
	if t == nil || len(t.Selections) == 0 {
		return nil
	}

	return &t.Selections[len(t.Selections)-1]
}

// SelectedPayload returns the payload the slot's primary target selected
// last (nil when none).
func (r *SlotResult) SelectedPayload() *PayloadRecord {
	selection := r.Build.Primary().LastSelection()
	if selection == nil {
		return nil
	}

	return r.Payloads[selection.PayloadHash]
}

// refreshStatus derives the slot build's status from its primary target.
func (b *SlotBuild) refreshStatus() {
	primary := b.Primary()
	if primary == nil {
		return
	}

	b.Error = ""

	switch primary.Status {
	case TargetSelected:
		b.Status = BuildStatusReady
	case TargetBuilding:
		b.Status = BuildStatusBuilding
	default:
		b.Status = BuildStatusFailed
		b.Error = primary.Error
	}

	b.SkipReason = ""
	b.At = primary.At
}

func (t *BuildTarget) clone() *BuildTarget {
	c := *t

	if t.Engine != nil {
		engine := *t.Engine
		c.Engine = &engine
	}

	if t.Testing != nil {
		c.Testing = make([]*BuildRecord, len(t.Testing))
		for i, run := range t.Testing {
			record := *run
			c.Testing[i] = &record
		}
	}

	c.Selections = append([]Selection(nil), t.Selections...)

	// Attributes, Calls and Info are written once and never mutated
	// afterwards; sharing the pointers is safe.

	return &c
}

func (b *SlotBuild) clone() *SlotBuild {
	if b == nil {
		return nil
	}

	c := *b

	if b.Targets != nil {
		c.Targets = make([]*BuildTarget, len(b.Targets))
		for i, target := range b.Targets {
			c.Targets[i] = target.clone()
		}
	}

	return &c
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

	// Full bid message properties (blob commitments aggregated to a count;
	// pre-Gloas header bids carry block hash, parent hash, fee recipient and
	// gas limit). Empty for pre-construction failures.
	BlockHash       string `json:"block_hash,omitempty"`
	ParentBlockHash string `json:"parent_block_hash,omitempty"`
	ParentBlockRoot string `json:"parent_block_root,omitempty"`
	PrevRandao      string `json:"prev_randao,omitempty"`
	FeeRecipient    string `json:"fee_recipient,omitempty"`
	GasLimit        uint64 `json:"gas_limit,omitempty"`
	BuilderIndex    uint64 `json:"builder_index,omitempty"`
	// KeyIndex is the internal builder key the bid was signed with, resolved
	// from BuilderIndex at record time. Recorded because a builder index is
	// reused by other builders after an exit, so the mapping is only reliable
	// while the bid is being made.
	KeyIndex           *uint64 `json:"key_index,omitempty"`
	NumBlobCommitments int     `json:"num_blob_commitments,omitempty"`

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

	// Target is the beacon URL (or "primary") of an additional reveal target
	// publication; empty on the primary reveal.
	Target string `json:"target,omitempty"`

	// StartedAt is when the attempt began (envelope construction + submit
	// call); nil on skips where nothing was attempted.
	StartedAt *time.Time `json:"started_at,omitempty"`
}

// InclusionResult records that the slot's payload was seen included at the
// head. Field semantics match the WonBlock wire shape (minus the slot).
type InclusionResult struct {
	Source          string `json:"source"` // "epbs" | "builder_api"
	BlockHash       string `json:"block_hash"`
	NumTransactions int    `json:"num_transactions"`
	// NumPrivateTxs counts the transactions buildoor supplied itself (own
	// mempool or plan list); absent on records from before it was tracked.
	NumPrivateTxs *int      `json:"num_private_txs,omitempty"`
	NumBlobs      int       `json:"num_blobs"`
	ValueWei      string    `json:"value_wei"`
	ValueETH      string    `json:"value_eth"`
	Timestamp     time.Time `json:"timestamp"`

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

	// resident marks the in-memory summary of a record whose full version
	// lives in the state-db (see Tracker.compactBefore).
	resident bool

	// AppliedPlan is the frozen plan snapshot the slot executed under.
	AppliedPlan *action_plan.FrozenPlan `json:"applied_plan,omitempty"`

	// Build is the slot's build lifecycle with every target it ran.
	Build *SlotBuild `json:"build,omitempty"`
	// Payloads holds every payload built for the slot, by block hash.
	Payloads map[string]*PayloadRecord `json:"payloads,omitempty"`

	Bids             []BidAttempt      `json:"bids,omitempty"`
	BlockSubmissions []BlockSubmission `json:"block_submissions,omitempty"`
	RevealAttempts   []RevealAttempt   `json:"reveal_attempts,omitempty"`
	Inclusion        *InclusionResult  `json:"inclusion,omitempty"`

	// TxPlan is the post-inclusion verdict of a locally built payload: whether
	// the canonical block holds exactly the transactions buildoor submitted,
	// in order. Only set for included slots whose payload came from an
	// explicit list (local build with a txpool/explicit/queued source).
	TxPlan *TxPlanResult `json:"tx_plan,omitempty"`

	// DroppedAttempts counts attempts beyond the per-kind retention cap,
	// keyed by kind ("bids", "block_submissions", "reveal_attempts").
	DroppedAttempts map[string]int `json:"dropped_attempts,omitempty"`

	UpdatedAt time.Time `json:"updated_at"`
}

// TxPlanStatus is the verdict of a tx plan check.
type TxPlanStatus string

const (
	// TxPlanPending: the payload was included, the EL check has not run yet.
	TxPlanPending TxPlanStatus = "pending"
	// TxPlanMatch: the canonical block holds exactly the plan, in order.
	TxPlanMatch TxPlanStatus = "match"
	// TxPlanMismatch: the block's transaction list differs from the plan.
	TxPlanMismatch TxPlanStatus = "mismatch"
	// TxPlanBlockNotFound: the EL did not serve the included block in time.
	TxPlanBlockNotFound TxPlanStatus = "block_not_found"
	// TxPlanNotIncluded: the slot passed without the payload reaching the chain.
	TxPlanNotIncluded TxPlanStatus = "not_included"
	// TxPlanMissed: the payload was won but the next block built on an older one.
	TxPlanMissed TxPlanStatus = "missed"
	// TxPlanOrphaned: the won block was reorged out.
	TxPlanOrphaned TxPlanStatus = "orphaned"
)

// maxTxPlanHashes bounds the expected-hash list stored per slot; the exact
// list is in the payload artifact.
const maxTxPlanHashes = 4096

// TxPlanResult is the plan a local payload was built from and its
// post-inclusion verdict.
type TxPlanResult struct {
	TxSource       string   `json:"tx_source"`
	ExpectedCount  int      `json:"expected_count"`
	ExpectedHashes []string `json:"expected_hashes,omitempty"`
	Truncated      bool     `json:"truncated,omitempty"`

	Status        TxPlanStatus `json:"status"`
	IncludedCount int          `json:"included_count,omitempty"`
	FirstMismatch int          `json:"first_mismatch,omitempty"`
	Detail        string       `json:"detail,omitempty"`
	VerifiedAt    *time.Time   `json:"verified_at,omitempty"`

	// awaitingSeed marks a verdict recorded before the inclusion seeded the
	// plan (the verifier and the tracker consume the same inclusion event
	// independently); the seed adopts it instead of resetting it to pending.
	awaitingSeed bool
}

// adoptVerdict copies an early verdict onto a freshly seeded plan.
func (p *TxPlanResult) adoptVerdict(verdict *TxPlanResult) {
	p.Status = verdict.Status
	p.IncludedCount = verdict.IncludedCount
	p.FirstMismatch = verdict.FirstMismatch
	p.Detail = verdict.Detail
	p.VerifiedAt = verdict.VerifiedAt
}

func (p *TxPlanResult) clone() *TxPlanResult {
	if p == nil {
		return nil
	}

	c := *p
	c.ExpectedHashes = append([]string(nil), p.ExpectedHashes...)

	if p.VerifiedAt != nil {
		at := *p.VerifiedAt
		c.VerifiedAt = &at
	}

	return &c
}

// Redacted returns the result for readers without a login: the applied
// plan loses its private parts (see action_plan.SlotPlan.Redacted), the
// testing runs and the tx plan lose their transaction hashes (see
// payload_builder.LocalBuildInfo.Public) and the reveal target attempts
// their URLs' credentials (payload_bidder.PublicRevealTarget).
func (r *SlotResult) Redacted() *SlotResult {
	if r == nil {
		return nil
	}

	out := *r
	out.AppliedPlan = r.AppliedPlan.Redacted()
	out.Build = r.Build.public()

	if r.TxPlan != nil && r.TxPlan.ExpectedHashes != nil {
		plan := *r.TxPlan
		plan.ExpectedHashes = nil
		out.TxPlan = &plan
	}

	cloned := false

	for i, attempt := range r.RevealAttempts {
		public := payload_bidder.PublicRevealTarget(attempt.Target)
		if public == attempt.Target {
			continue
		}

		if !cloned {
			out.RevealAttempts = slices.Clone(r.RevealAttempts)
			cloned = true
		}

		out.RevealAttempts[i].Target = public
	}

	return &out
}

// public returns the build with the testing runs' info stripped of
// transaction hashes.
func (b *SlotBuild) public() *SlotBuild {
	if b == nil {
		return nil
	}

	out := *b
	out.Targets = make([]*BuildTarget, len(b.Targets))

	for i, target := range b.Targets {
		out.Targets[i] = target

		if len(target.Testing) == 0 {
			continue
		}

		public := *target
		public.Testing = make([]*BuildRecord, len(target.Testing))

		for j, run := range target.Testing {
			record := *run
			record.Info = run.Info.Public()
			public.Testing[j] = &record
		}

		out.Targets[i] = &public
	}

	return &out
}

// RedactedResults returns the results without raw explicit transactions.
func RedactedResults(results []*SlotResult) []*SlotResult {
	out := make([]*SlotResult, len(results))
	for i, result := range results {
		out[i] = result.Redacted()
	}

	return out
}

// Clone returns a deep copy of the result.
func (r *SlotResult) Clone() *SlotResult {
	if r == nil {
		return nil
	}

	c := *r

	c.Build = r.Build.clone()

	// Payload records are written once and never modified: the map is
	// copied, the records shared.
	if r.Payloads != nil {
		c.Payloads = maps.Clone(r.Payloads)
	}

	if r.Inclusion != nil {
		inclusion := *r.Inclusion
		c.Inclusion = &inclusion
	}

	c.TxPlan = r.TxPlan.clone()

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

// Summary returns the result trimmed to what an overview (the action plan
// grid) shows: the build's status and each target's builds and selections
// without their details, the selected payloads, the attempts, the inclusion
// and the tx plan verdict. The applied plan, the attributes, the testing
// runs' assembly details and the payloads no target selected — most of a
// record's size — are left out; the full record is a per-slot request away.
func (r *SlotResult) Summary() *SlotResult {
	if r == nil {
		return nil
	}

	s := &SlotResult{
		Slot:             r.Slot,
		Epoch:            r.Epoch,
		Fork:             r.Fork,
		Bids:             r.Bids,
		BlockSubmissions: r.BlockSubmissions,
		RevealAttempts:   r.RevealAttempts,
		Inclusion:        r.Inclusion,
		TxPlan:           r.TxPlan,
		DroppedAttempts:  r.DroppedAttempts,
		UpdatedAt:        r.UpdatedAt,
	}

	if b := r.Build; b != nil {
		build := *b
		build.Targets = make([]*BuildTarget, len(b.Targets))

		for i, target := range b.Targets {
			summary := &BuildTarget{
				Seq:               target.Seq,
				Candidate:         target.Candidate,
				Status:            target.Status,
				Error:             target.Error,
				TestingSkipReason: target.TestingSkipReason,
				Selections:        target.Selections,
				StartedAt:         target.StartedAt,
				At:                target.At,
			}

			if target.Engine != nil {
				engine := *target.Engine
				engine.Calls = nil
				summary.Engine = &engine
			}

			if len(target.Testing) > 0 {
				summary.Testing = make([]*BuildRecord, len(target.Testing))

				for j, run := range target.Testing {
					record := *run
					record.Info = nil
					summary.Testing[j] = &record
				}
			}

			build.Targets[i] = summary

			if selection := target.LastSelection(); selection != nil && r.Payloads[selection.PayloadHash] != nil {
				if s.Payloads == nil {
					s.Payloads = make(map[string]*PayloadRecord, len(b.Targets))
				}

				s.Payloads[selection.PayloadHash] = r.Payloads[selection.PayloadHash]
			}
		}

		s.Build = &build
	}

	return s
}
