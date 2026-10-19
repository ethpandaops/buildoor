package slot_results

import (
	"encoding/json"
	"time"

	"github.com/ethpandaops/buildoor/pkg/payload_builder"
)

// Records persisted before the build model described a slot's builds as
// outcomes: one per candidate, carrying the payload that fed the bids
// (source el or local) plus the testing build's own outcome. They are mapped
// onto targets on read: the outcome's payload becomes a payload record of
// its source's build and the target's one selection, the testing outcome a
// testing run. What those records never captured (the engine payload of a
// target whose testing payload was used) stays unknown.

// legacyBuilds is the part of a persisted record the old build model wrote.
type legacyBuilds struct {
	Build  *legacyBuildOutcome   `json:"build"`
	Builds []*legacyBuildOutcome `json:"builds"`
}

type legacyBuildOutcome struct {
	Status     string `json:"status"`
	SkipReason string `json:"skip_reason"`
	Candidate  string `json:"candidate"`

	BlockHash            string `json:"block_hash"`
	BlockValueWei        string `json:"block_value_wei"`
	NumTransactions      int    `json:"num_transactions"`
	NumBlobs             int    `json:"num_blobs"`
	FeeRecipient         string `json:"fee_recipient"`
	BlockNumber          uint64 `json:"block_number"`
	ParentHash           string `json:"parent_hash"`
	StateRoot            string `json:"state_root"`
	ReceiptsRoot         string `json:"receipts_root"`
	PrevRandao           string `json:"prev_randao"`
	Timestamp            uint64 `json:"timestamp"`
	GasLimit             uint64 `json:"gas_limit"`
	GasUsed              uint64 `json:"gas_used"`
	BaseFeePerGas        string `json:"base_fee_per_gas"`
	ExtraData            string `json:"extra_data"`
	BlobGasUsed          uint64 `json:"blob_gas_used"`
	ExcessBlobGas        uint64 `json:"excess_blob_gas"`
	NumWithdrawals       int    `json:"num_withdrawals"`
	NumExecutionRequests int    `json:"num_execution_requests"`
	ArtifactIdx          *int   `json:"artifact_idx"`

	Attributes    *AttributesSnapshot                 `json:"attributes"`
	Source        string                              `json:"source"`
	Fallback      bool                                `json:"fallback"`
	InclusionList *payload_builder.InclusionListBuild `json:"inclusion_list"`
	LocalBuild    *legacyLocalBuild                   `json:"local_build"`

	Error string    `json:"error"`
	At    time.Time `json:"at"`
}

type legacyLocalBuild struct {
	Status          string                          `json:"status"`
	SkipReason      string                          `json:"skip_reason"`
	Error           string                          `json:"error"`
	TxSource        string                          `json:"tx_source"`
	PayloadSource   string                          `json:"payload_source"`
	BlockHash       string                          `json:"block_hash"`
	BlockValueWei   string                          `json:"block_value_wei"`
	NumTransactions int                             `json:"num_transactions"`
	NumBlobs        int                             `json:"num_blobs"`
	GasUsed         uint64                          `json:"gas_used"`
	GasLimit        uint64                          `json:"gas_limit"`
	ArtifactIdx     *int                            `json:"artifact_idx"`
	Info            *payload_builder.LocalBuildInfo `json:"info"`
	At              time.Time                       `json:"at"`
}

// legacyPickReasons maps the pick reasons of records written while the
// first pick waited for the engine build's getPayload.
var legacyPickReasons = map[string]string{
	"handover": payload_builder.SelectionFirst,
	"upgrade":  payload_builder.SelectionBetter,
}

// adoptLegacyBuilds maps the old build outcomes of a persisted record onto
// the build model (a no-op for records written by the build model).
func adoptLegacyBuilds(result *SlotResult, raw []byte) {
	if result.Build != nil && (len(result.Build.Targets) > 0 || result.Payloads != nil) {
		for _, target := range result.Build.Targets {
			for i, selection := range target.Selections {
				if reason, ok := legacyPickReasons[selection.Reason]; ok {
					target.Selections[i].Reason = reason
				}
			}
		}

		return
	}

	var legacy legacyBuilds
	if err := json.Unmarshal(raw, &legacy); err != nil || legacy.Build == nil {
		return
	}

	outcomes := legacy.Builds
	if len(outcomes) == 0 && legacy.Build.Candidate+legacy.Build.BlockHash != "" {
		outcomes = []*legacyBuildOutcome{legacy.Build}
	}

	for _, outcome := range outcomes {
		target := outcome.target(result)
		if target == nil {
			continue
		}

		if result.Build == nil {
			result.Build = &SlotBuild{}
		}

		result.Build.Targets = append(result.Build.Targets, target)
	}

	if result.Build != nil && len(result.Build.Targets) > 0 {
		result.Build.refreshStatus()
	}
}

// target maps one old outcome onto a target, adding its payloads.
func (o *legacyBuildOutcome) target(result *SlotResult) *BuildTarget {
	if o.Status == string(BuildStatusWaitingAttributes) || o.Status == string(BuildStatusNoAttributes) ||
		o.Status == string(BuildStatusSkipped) {
		return nil
	}

	target := &BuildTarget{
		Candidate:  o.Candidate,
		Status:     TargetBuilding,
		Error:      o.Error,
		Attributes: o.Attributes,
		StartedAt:  o.At,
		At:         o.At,
	}

	if o.Status == "failed" {
		target.Status = TargetNoPayload
	}

	local := o.LocalBuild
	if local != nil {
		target.TxSource, target.PayloadSource = local.TxSource, local.PayloadSource

		run := &BuildRecord{
			Kind:       payload_builder.BuildKindTesting,
			Run:        1,
			Status:     local.Status,
			SkipReason: local.SkipReason,
			Error:      local.Error,
			Info:       local.Info,
			StartedAt:  local.At,
			At:         local.At,
		}

		if local.BlockHash != "" {
			run.PayloadHash = local.BlockHash
			addPayloadRecord(result, &PayloadRecord{
				BlockHash:       local.BlockHash,
				Build:           payload_builder.BuildRef{Kind: payload_builder.BuildKindTesting, Run: 1},
				Candidate:       o.Candidate,
				Captured:        local.ArtifactIdx != nil,
				BlockValueWei:   local.BlockValueWei,
				NumTransactions: local.NumTransactions,
				NumBlobs:        local.NumBlobs,
				GasUsed:         local.GasUsed,
				GasLimit:        local.GasLimit,
				BuiltAt:         local.At,
			})
		}

		if local.Info != nil && local.Info.Run > 0 {
			run.Run = local.Info.Run
		}

		target.Testing = []*BuildRecord{run}
	}

	if o.Status != "ready" || o.BlockHash == "" {
		return target
	}

	ref := payload_builder.BuildRef{Kind: payload_builder.BuildKindEngine}

	if o.Source == "local" {
		ref = payload_builder.BuildRef{Kind: payload_builder.BuildKindTesting, Run: 1}
		if len(target.Testing) > 0 {
			ref.Run = target.Testing[0].Run
		}
	} else {
		target.Engine = &BuildRecord{
			Kind:        payload_builder.BuildKindEngine,
			Status:      payload_builder.EngineStatusReady,
			PayloadHash: o.BlockHash,
			At:          o.At,
		}
	}

	// The outcome's payload replaces a headline-only testing record: it
	// carries the full properties.
	if result.Payloads != nil {
		delete(result.Payloads, o.BlockHash)
	}

	addPayloadRecord(result, &PayloadRecord{
		BlockHash:            o.BlockHash,
		Build:                ref,
		Candidate:            o.Candidate,
		Captured:             o.ArtifactIdx != nil,
		BlockValueWei:        o.BlockValueWei,
		NumTransactions:      o.NumTransactions,
		NumBlobs:             o.NumBlobs,
		FeeRecipient:         o.FeeRecipient,
		BlockNumber:          o.BlockNumber,
		ParentHash:           o.ParentHash,
		StateRoot:            o.StateRoot,
		ReceiptsRoot:         o.ReceiptsRoot,
		PrevRandao:           o.PrevRandao,
		Timestamp:            o.Timestamp,
		GasLimit:             o.GasLimit,
		GasUsed:              o.GasUsed,
		BaseFeePerGas:        o.BaseFeePerGas,
		ExtraData:            o.ExtraData,
		BlobGasUsed:          o.BlobGasUsed,
		ExcessBlobGas:        o.ExcessBlobGas,
		NumWithdrawals:       o.NumWithdrawals,
		NumExecutionRequests: o.NumExecutionRequests,
		InclusionList:        o.InclusionList,
		BuiltAt:              o.At,
	})

	target.Status = TargetSelected
	target.Selections = []Selection{{
		PayloadHash: o.BlockHash,
		Build:       ref,
		Reason:      payload_builder.SelectionFirst,
		Fallback:    o.Fallback,
		At:          o.At,
	}}

	return target
}
