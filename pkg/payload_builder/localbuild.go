package payload_builder

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/inclusionlist"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
	"github.com/ethpandaops/buildoor/pkg/txpool"
)

// Testing build skip reasons (TestingBuildEvent.SkipReason,
// BuildTargetEvent.TestingSkipReason).
const (
	// LocalSkipUnavailable: the EL does not expose the testing namespace.
	LocalSkipUnavailable = "unavailable"
	// LocalSkipDisabled: the local build is disabled for the slot.
	LocalSkipDisabled = "disabled"
	// LocalSkipTxPoolUnavailable: tx source txpool without a pool (no --el-rpc).
	LocalSkipTxPoolUnavailable = "txpool_unavailable"
	// LocalSkipTxPoolDisabled: tx source txpool while the pool is switched off.
	LocalSkipTxPoolDisabled = "txpool_disabled"
	// LocalSkipSpeculativeCandidate: the target is a speculative candidate
	// (parent_empty, grandparent_*). testing_buildBlockV1 builds on the EL's
	// CURRENT head only, and a speculative parent is not the head: the CL
	// re-pins the head to the canonical block with its own forkchoice
	// updates, and parallel candidate builds move it with theirs, so the
	// call would fail with "parentHash is not current head". Only the
	// canonical candidate builds locally.
	LocalSkipSpeculativeCandidate = "speculative_candidate"
)

// Testing build run statuses (TestingBuildEvent.Status).
const (
	LocalStatusBuilding = "building"
	LocalStatusReady    = "ready"
	LocalStatusFailed   = "failed"
	LocalStatusSkipped  = "skipped"
)

// LocalBuildClient is the EL surface the local build uses: the testing
// namespace call and its availability probe. Satisfied by *execution.Client.
type LocalBuildClient interface {
	BuildBlockV1(ctx context.Context, engineVersion enginev.DataVersion, parentHash common.Hash,
		attrs *engineall.PayloadAttributes, transactions [][]byte, extraData []byte,
	) (*engineall.GetPayloadResponse, error)
	ProbeTestingAPI(ctx context.Context) execution.TestingAPIStatus
	ClientVersion(ctx context.Context) (string, error)
}

var _ LocalBuildClient = (*execution.Client)(nil)

// LocalBuildRequest is a slot's resolved local-build instruction handed to the
// payload builder alongside the engine build.
type LocalBuildRequest struct {
	// TxSource is txpool, empty, el_mempool or explicit.
	TxSource string
	// Transactions is the explicit transaction list (tx source explicit).
	Transactions [][]byte
	// Queued is the exact ordered list of pool transaction hashes (tx source
	// queued).
	Queued []common.Hash
	// PayloadSource decides which payload feeds consumers: el, local,
	// local_or_el or higher_value.
	PayloadSource string
	// EngineEnabled runs the engine-API build next to the testing builds
	// (the slot's build.engine_enabled). Without it the testing build alone
	// feeds the consumers, whatever PayloadSource says.
	EngineEnabled bool

	// Pool selection tweaks (tx source txpool).
	MaxTxs     uint64
	GasFillPct uint64
	Ordering   string
	// IncludeBlobTxs allows blob transactions in the selection; false when the
	// EL cannot produce a blobs bundle on the testing path (unless the
	// operator opted into unavailable blobs).
	IncludeBlobTxs bool
	// BlobEncoding is the blob transaction encoding the EL expects.
	BlobEncoding string
	// MaxAttempts / MaxStrikes: retry policy of the txpool source after an
	// attributed EL refusal.
	MaxAttempts uint64
	MaxStrikes  uint64
	// BaseFeeCeilingGwei halves the pool fill above this next base fee (0 = off).
	BaseFeeCeilingGwei uint64
	// StartMs / EndMs are the testing build window relative to the slot
	// start; RebuildInterval the minimum gap of change-triggered rebuilds (0 =
	// one build timed to the window end; see runLocalBuilds).
	StartMs         int64
	EndMs           int64
	RebuildInterval time.Duration
}

// RunEL reports whether the engine-API build runs alongside the local build
// (a target without a local build only reaches the builder with the engine
// build enabled).
func (r *LocalBuildRequest) RunEL() bool {
	return r == nil || r.EngineEnabled
}

// selectionRequest is the request the payload choice works with: without the
// engine build there is only the testing payload to pick, whatever source the
// settings name.
func (r *LocalBuildRequest) selectionRequest() *LocalBuildRequest {
	if r == nil || r.EngineEnabled {
		return r
	}

	local := *r
	local.PayloadSource = config.PayloadSourceLocal

	return &local
}

// LocalBuildInfo describes how a local payload was assembled.
type LocalBuildInfo struct {
	TxSource string `json:"tx_source"`
	// Selection is the pool selection summary (tx source txpool).
	Selection *txpool.Summary `json:"selection,omitempty"`
	// ExplicitTxs is the explicit list length (tx source explicit / queued).
	ExplicitTxs int `json:"explicit_txs,omitempty"`
	// ExpectedHashes are the hashes of the transactions handed to the EL, in
	// order (nil for the el_mempool source, where the EL chooses). The built
	// payload is checked against this list before it is used, and the
	// included block after inclusion.
	ExpectedHashes []string `json:"expected_hashes,omitempty"`
	// InclusionList is the inclusion list view this build carried (nil
	// before Heze).
	InclusionList *InclusionListBuild `json:"inclusion_list,omitempty"`
	// InclusionListTxs is how many inclusion-list transactions the slot had
	// (prepended to the list, or ignored by the el_mempool source).
	InclusionListTxs int `json:"inclusion_list_txs,omitempty"`
	// InclusionListDropped marks a payload without the slot's inclusion list
	// (a spec-violating payload): the build only succeeded after dropping it,
	// or the el_mempool source never carried it.
	InclusionListDropped bool `json:"inclusion_list_dropped,omitempty"`
	// DroppedByEL counts submitted transactions the EL left out of the payload
	// (ELs that silently filter instead of failing the call).
	DroppedByEL int `json:"dropped_by_el,omitempty"`
	// SubmittedTxs is how many transactions were handed to the EL (-1 for the
	// el_mempool source, where the EL chooses).
	SubmittedTxs int `json:"submitted_txs"`
	// FillTxs is how many EL mempool transactions the private_first source
	// placed after the pool selection (most_profitable: among it); FillError
	// is why the EL mempool
	// could not be read (the build then used the pool alone); FillDropped
	// marks that the EL refused the list and the build retried without the
	// fill; FillBlobsDropped that a refused list was first retried without
	// the fill's blob transactions (and their senders' later fill txs).
	FillTxs          int    `json:"fill_txs,omitempty"`
	FillError        string `json:"fill_error,omitempty"`
	FillDropped      bool   `json:"fill_dropped,omitempty"`
	FillBlobsDropped bool   `json:"fill_blobs_dropped,omitempty"`
	// Attempts is how many testing_buildBlockV1 calls the build took.
	Attempts int `json:"attempts,omitempty"`
	// Dropped lists the pool transactions removed from the attempt after an
	// attributed EL refusal (txpool source only), with the failure class.
	Dropped []DroppedTx `json:"dropped,omitempty"`
	// StartedAt is when the testing build began (its selection);
	// SelectedAt when the list was assembled and the testing call started.
	StartedAt  time.Time `json:"started_at"`
	SelectedAt time.Time `json:"selected_at,omitempty"`
	// BuiltAt is when testing_buildBlockV1 returned the payload.
	BuiltAt time.Time `json:"built_at"`
	// Run is the run's number within its target (1-based).
	Run int `json:"run,omitempty"`
	// MirrorStarted marks a selection that started the EL mempool mirror
	// (a full read): untypically slow, so it does not train the timing model.
	MirrorStarted bool `json:"mirror_started,omitempty"`
}

// DroppedTx is a pool transaction removed from a build attempt after the EL
// refused the list.
type DroppedTx struct {
	Hash   string `json:"hash,omitempty"`
	Reason string `json:"reason"`
}

// Public returns the info without transaction hashes, for readers who must
// not learn what the custom block holds before it is published: the
// buildoor mempool keeps its transactions private, and a withheld or
// orphaned payload never publishes them at all. Counts and reasons stay.
func (i *LocalBuildInfo) Public() *LocalBuildInfo {
	if i == nil || (i.ExpectedHashes == nil && len(i.Dropped) == 0 && (i.Selection == nil || len(i.Selection.Hashes) == 0)) {
		return i
	}

	out := *i
	out.ExpectedHashes = nil

	if i.Selection != nil && len(i.Selection.Hashes) > 0 {
		selection := *i.Selection
		selection.Hashes = nil
		out.Selection = &selection
	}

	if len(i.Dropped) > 0 {
		out.Dropped = make([]DroppedTx, len(i.Dropped))
		for idx, dropped := range i.Dropped {
			out.Dropped[idx] = DroppedTx{Reason: dropped.Reason}
		}
	}

	return &out
}

// buildState is a target's builds as they stand, the input of the payload
// source's rule (selectPayload).
type buildState struct {
	// EL is the engine build's payload (nil when not run, running or failed).
	EL    *Payload
	ELErr error

	// Local is the best testing run's payload so far (nil when none).
	Local           *Payload
	LocalErr        error
	LocalSkipReason string
}

// selection is one pick of the payload source among a target's payloads.
type selection struct {
	Payload  *Payload
	Fallback bool
}

// LocalBuildAvailability is the probed state of the EL's testing namespace
// plus the per-EL blob handling derived from its identity.
type LocalBuildAvailability struct {
	// Configured is true when an EL RPC client exists (--el-rpc).
	Configured bool `json:"configured"`
	// Available is true when testing_buildBlockV1 answered the probe.
	Available bool `json:"available"`
	// Reason explains an unavailable state.
	Reason string `json:"reason,omitempty"`
	// CheckedAt is the last probe time (zero before the first probe).
	CheckedAt time.Time `json:"checked_at"`
	// ELCode is the EL's engine_getClientVersionV1 code (GE, RH, ...).
	ELCode string `json:"el_code,omitempty"`
	// EnableHint is the EL flag that exposes the namespace.
	EnableHint string `json:"enable_hint,omitempty"`
	// BlobBundle is false on ELs whose testing path returns an empty blobs
	// bundle (reth, ethrex).
	BlobBundle bool `json:"blob_bundle"`
	// BlobEncoding is the effective blob transaction encoding for the EL.
	BlobEncoding string `json:"blob_encoding"`
}

// elBlobProfile is what the EL identity implies for blob transactions on the
// testing path.
type elBlobProfile struct {
	encoding   string
	bundle     bool
	enableHint string
}

// elBlobProfiles keys by engine_getClientVersionV1 client code.
var elBlobProfiles = map[string]elBlobProfile{
	"GE": {encoding: config.BlobEncodingNetwork, bundle: true, enableHint: "--http.api ...,testing"},
	"RH": {encoding: config.BlobEncodingCanonical, bundle: false, enableHint: "--http.api ...,testing"},
	"BU": {encoding: config.BlobEncodingNetwork, bundle: true, enableHint: "--rpc-http-api ...,TESTING"},
	"EG": {encoding: config.BlobEncodingNetwork, bundle: true, enableHint: "--http.api ...,testing"},
	"EX": {encoding: config.BlobEncodingCanonical, bundle: false, enableHint: "--http.api eth,net,web3,testing"},
	"NM": {encoding: config.BlobEncodingNetwork, bundle: true, enableHint: "--JsonRpc.EnabledModules ...,Testing"},
	"NB": {encoding: config.BlobEncodingNetwork, bundle: false, enableHint: "not implemented by nimbus-eth1"},
}

// defaultBlobProfile applies to unknown ELs.
var defaultBlobProfile = elBlobProfile{
	encoding:   config.BlobEncodingNetwork,
	bundle:     true,
	enableHint: "enable the testing JSON-RPC namespace",
}

// elCodeFromClientVersion maps a web3_clientVersion string to the two-letter
// client code of engine_getClientVersionV1 ("" when unrecognized).
func elCodeFromClientVersion(version string) string {
	name := strings.ToLower(version)
	if i := strings.IndexByte(name, '/'); i > 0 {
		name = name[:i]
	}

	switch name {
	case "geth", "go-ethereum":
		return "GE"
	case "reth":
		return "RH"
	case "besu":
		return "BU"
	case "erigon":
		return "EG"
	case "ethrex":
		return "EX"
	case "nethermind":
		return "NM"
	case "nimbus", "nimbus-eth1":
		return "NB"
	default:
		return ""
	}
}

// localBuildState is the service's view of the local build extension.
type localBuildState struct {
	mu           sync.RWMutex
	client       LocalBuildClient
	availability LocalBuildAvailability
}

// SetLocalBuildClient wires the EL RPC client the local build uses. Must be
// called before Start; nil leaves the extension unconfigured.
func (s *Service) SetLocalBuildClient(client LocalBuildClient) {
	s.localBuild.mu.Lock()
	defer s.localBuild.mu.Unlock()

	s.localBuild.client = client
	s.localBuild.availability = LocalBuildAvailability{
		Configured: client != nil,
		Reason:     "not probed yet",
	}

	if client == nil {
		s.localBuild.availability.Reason = "no --el-rpc configured"
	}
}

// SetTxPool wires the owned transaction pool the txpool tx source selects
// from. Must be called before Start; nil leaves the source unavailable.
func (s *Service) SetTxPool(pool *txpool.Pool) {
	s.txPool = pool
}

// InclusionLists returns the inclusion list store (FOCIL, Heze) the builds
// and bids read.
func (s *Service) InclusionLists() *inclusionlist.Store {
	return s.ilStore
}

// TxPool returns the wired transaction pool (nil when unconfigured).
func (s *Service) TxPool() *txpool.Pool {
	return s.txPool
}

// LocalBuildAvailability returns the current probed availability.
func (s *Service) LocalBuildAvailability() LocalBuildAvailability {
	s.localBuild.mu.RLock()
	defer s.localBuild.mu.RUnlock()

	return s.localBuild.availability
}

// ProbeLocalBuild re-probes the EL's testing namespace now and returns the
// resulting availability.
func (s *Service) ProbeLocalBuild(ctx context.Context) LocalBuildAvailability {
	s.localBuild.mu.RLock()
	client := s.localBuild.client
	s.localBuild.mu.RUnlock()

	if client == nil {
		return s.LocalBuildAvailability()
	}

	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	status := client.ProbeTestingAPI(probeCtx)

	profile := defaultBlobProfile
	elCode := ""

	if v := s.GetELClientVersion(); v != nil {
		elCode = v.Code
	}

	if elCode == "" {
		// Not every EL answers engine_getClientVersionV1 (nethermind and besu
		// on the devnets); the RPC's web3_clientVersion names it too.
		if version, err := client.ClientVersion(probeCtx); err == nil {
			elCode = elCodeFromClientVersion(version)
		}
	}

	if p, ok := elBlobProfiles[elCode]; ok {
		profile = p
	}

	encoding := config.NormalizedBlobEncoding(s.cfg.LocalBuild.BlobEncoding)
	if encoding == config.BlobEncodingAuto {
		encoding = profile.encoding
	}

	availability := LocalBuildAvailability{
		Configured:   true,
		Available:    status.Available,
		Reason:       status.Reason,
		CheckedAt:    time.Now(),
		ELCode:       elCode,
		EnableHint:   profile.enableHint,
		BlobBundle:   profile.bundle,
		BlobEncoding: encoding,
	}

	s.localBuild.mu.Lock()
	changed := s.localBuild.availability.Available != availability.Available
	s.localBuild.availability = availability
	s.localBuild.mu.Unlock()

	if changed {
		s.log.WithField("available", availability.Available).WithField("reason", availability.Reason).
			Info("Local build (testing_buildBlockV1) availability changed")
	}

	return availability
}

// GuardSetting vetoes enabling the local build while the EL does not expose
// the testing namespace, and enabling the pool without an EL RPC client.
func (s *Service) GuardSetting(key string, value any) error {
	enable, isBool := value.(bool)
	if !isBool || !enable {
		return nil
	}

	switch key {
	case config.KeyLocalBuildEnabled:
		availability := s.LocalBuildAvailability()
		if !availability.Available {
			return fmt.Errorf("cannot enable the local build: testing_buildBlockV1 not available on the EL: %s",
				availability.Reason)
		}
	case config.KeyTxPoolEnabled:
		if s.txPool == nil {
			return fmt.Errorf("cannot enable the transaction pool: no --el-rpc configured")
		}
	}

	return nil
}

// resolveLocalBuildRequest turns the slot's frozen local-build settings into
// the builder request for one candidate, applying the runtime gates the plan
// cannot know: the candidate (only the canonical parent_full candidate — or
// an unclassified tuple — is the EL head the testing call builds on), the
// probed availability of the testing namespace and the EL's blob handling.
// The second return is the skip reason when no local build runs (empty when a
// request is returned or the extension is simply off without a plan).
func (s *Service) resolveLocalBuildRequest(slot phase0.Slot, candidate chain.CandidateKey) (*LocalBuildRequest, string) {
	frozen := s.planSvc.Freeze(slot)
	if frozen.Build == nil || frozen.Build.Local == nil {
		return nil, ""
	}

	settings := frozen.Build.Local
	if !settings.Enabled {
		return nil, ""
	}

	if candidate != "" && candidate != chain.CandidateParentFull {
		return nil, LocalSkipSpeculativeCandidate
	}

	availability := s.LocalBuildAvailability()
	if !availability.Available {
		return nil, LocalSkipUnavailable
	}

	req := &LocalBuildRequest{
		TxSource:           settings.TxSource,
		PayloadSource:      settings.PayloadSource,
		EngineEnabled:      frozen.Build.EngineEnabled,
		MaxTxs:             settings.MaxTxs,
		GasFillPct:         settings.GasFillPct,
		Ordering:           settings.Ordering,
		IncludeBlobTxs:     availability.BlobBundle || settings.AllowBlobsWithoutBundle,
		BlobEncoding:       availability.BlobEncoding,
		MaxAttempts:        settings.MaxAttempts,
		MaxStrikes:         settings.MaxStrikes,
		RebuildInterval:    time.Duration(settings.RebuildIntervalMs) * time.Millisecond,
		StartMs:            settings.StartTimeMs,
		EndMs:              settings.EndTimeMs,
		BaseFeeCeilingGwei: settings.BaseFeeCeilingGwei,
	}

	if len(settings.Queued) > 0 {
		req.TxSource = config.TxSourceQueued
		req.Queued = make([]common.Hash, 0, len(settings.Queued))

		for _, hexHash := range settings.Queued {
			req.Queued = append(req.Queued, common.HexToHash(hexHash))
		}
	}

	if len(settings.Transactions) > 0 {
		req.TxSource = config.TxSourceExplicit
		req.Transactions = make([][]byte, 0, len(settings.Transactions))

		for _, hexTx := range settings.Transactions {
			raw, err := decodeHexBytes(hexTx)
			if err != nil {
				// Validated at plan-update time; a decode failure here means
				// a corrupted persisted plan. Build without the list rather
				// than guessing.
				s.log.WithError(err).WithField("slot", slot).Error("Invalid explicit transaction in frozen plan")

				continue
			}

			req.Transactions = append(req.Transactions, raw)
		}
	}

	return req, ""
}

// decodeHexBytes parses a 0x-prefixed hex string.
func decodeHexBytes(s string) ([]byte, error) {
	return hexutil.Decode(s)
}

// PrivateTxs counts the transactions of a testing build that buildoor
// supplied itself — from its own mempool or an action plan's list — rather
// than taking them from the EL's public mempool (the EL mempool fill and the
// inclusion-list transactions). The list sent to the EL is exactly what the
// payload holds (it is verified against the payload before use).
func (i *LocalBuildInfo) PrivateTxs() int {
	if i == nil || i.ExpectedHashes == nil {
		return 0
	}

	public := 0
	if !i.FillDropped {
		public += i.FillTxs
	}

	if !i.InclusionListDropped {
		public += i.InclusionListTxs
	}

	return max(0, len(i.ExpectedHashes)-public)
}
