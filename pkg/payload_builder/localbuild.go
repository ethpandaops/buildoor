package payload_builder

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
	"github.com/ethpandaops/buildoor/pkg/txpool"
	"github.com/ethpandaops/buildoor/pkg/utils"
)

// Payload sources recorded on a built payload.
const (
	// SourceEL marks a payload built by the EL from its own mempool through
	// the engine API (forkchoiceUpdated + getPayload).
	SourceEL = "el"
	// SourceLocal marks a payload built through testing_buildBlockV1 from a
	// buildoor-chosen transaction list.
	SourceLocal = "local"
)

// Local build skip reasons (LocalBuildEvent.SkipReason).
const (
	// LocalSkipUnavailable: the EL does not expose the testing namespace.
	LocalSkipUnavailable = "unavailable"
	// LocalSkipDisabled: the local build is disabled for the slot.
	LocalSkipDisabled = "disabled"
	// LocalSkipTxPoolUnavailable: tx source txpool without a pool (no --el-rpc).
	LocalSkipTxPoolUnavailable = "txpool_unavailable"
	// LocalSkipTxPoolDisabled: tx source txpool while the pool is switched off.
	LocalSkipTxPoolDisabled = "txpool_disabled"
)

// Local build statuses (LocalBuildEvent.Status).
const (
	LocalStatusReady   = "ready"
	LocalStatusFailed  = "failed"
	LocalStatusSkipped = "skipped"
)

// LocalBuildClient is the EL surface the local build uses: the testing
// namespace call and its availability probe. Satisfied by *execution.Client.
type LocalBuildClient interface {
	BuildBlockV1(ctx context.Context, engineVersion enginev.DataVersion, parentHash common.Hash,
		attrs *engineall.PayloadAttributes, transactions [][]byte, extraData []byte,
	) (*engineall.GetPayloadResponse, error)
	ProbeTestingAPI(ctx context.Context) execution.TestingAPIStatus
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
	// PayloadSource decides which payload feeds consumers: el, local or
	// local_or_el.
	PayloadSource string
	// BuildELPayload keeps the engine build running when PayloadSource is
	// local.
	BuildELPayload bool

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
}

// RunEL reports whether the engine-API build runs alongside the local build.
// The el and local_or_el payload sources need the engine payload; local only
// keeps it when BuildELPayload is set.
func (r *LocalBuildRequest) RunEL() bool {
	if r == nil {
		return true
	}

	return r.PayloadSource != config.PayloadSourceLocal || r.BuildELPayload
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
	// InclusionListTxs is how many inclusion-list transactions were prepended.
	InclusionListTxs int `json:"inclusion_list_txs,omitempty"`
	// InclusionListDropped marks that the build only succeeded after the
	// inclusion list was dropped (a spec-violating payload, for testing).
	InclusionListDropped bool `json:"inclusion_list_dropped,omitempty"`
	// DroppedByEL counts submitted transactions the EL left out of the payload
	// (ELs that silently filter instead of failing the call).
	DroppedByEL int `json:"dropped_by_el,omitempty"`
	// SubmittedTxs is how many transactions were handed to the EL (-1 for the
	// el_mempool source, where the EL chooses).
	SubmittedTxs int `json:"submitted_txs"`
	// Attempts is how many testing_buildBlockV1 calls the build took.
	Attempts int `json:"attempts,omitempty"`
	// Dropped lists the pool transactions removed from the attempt after an
	// attributed EL refusal (txpool source only), with the failure class.
	Dropped []DroppedTx `json:"dropped,omitempty"`
	// BuiltAt is when testing_buildBlockV1 returned the payload. The local
	// build runs during the engine build's wait, so this is usually well
	// before the payload's ReadyAt (the hand-over to the consumers).
	BuiltAt time.Time `json:"built_at"`
}

// DroppedTx is a pool transaction removed from a build attempt after the EL
// refused the list.
type DroppedTx struct {
	Hash   string `json:"hash"`
	Reason string `json:"reason"`
}

// BuildResult is the outcome of one build target: the payload feeding the
// consumers plus both underlying builds for inspection.
type BuildResult struct {
	// Payload feeds bids and reveals (nil when the target produced none).
	Payload *Payload
	// Source is the Payload's source (el | local).
	Source string
	// Fallback marks that the local payload was wanted but the engine payload
	// was used instead.
	Fallback bool

	// EL is the engine-API payload (nil when skipped or failed).
	EL    *Payload
	ELErr error

	// Local is the local payload (nil when skipped or failed).
	Local           *Payload
	LocalErr        error
	LocalSkipReason string
	LocalInfo       *LocalBuildInfo
}

// LocalBuildEvent reports a slot's local build outcome (one per build target
// where the local build was requested), for the slot results tracker and the
// WebUI.
type LocalBuildEvent struct {
	Slot      phase0.Slot
	Candidate string
	// Status is ready, failed or skipped.
	Status     string
	SkipReason string
	Error      string

	TxSource      string
	PayloadSource string
	// Selected marks that the local payload is the one feeding the consumers.
	Selected bool
	// Fallback marks that the engine payload was used because the local build
	// failed or was skipped.
	Fallback bool

	// Payload is the local payload (nil unless ready).
	Payload *Payload
	// ELPayload is the engine payload of the same target (nil when skipped or
	// failed).
	ELPayload *Payload
	Info      *LocalBuildInfo
	At        time.Time
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
		if p, ok := elBlobProfiles[v.Code]; ok {
			profile = p
		}
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

// SubscribeLocalBuild subscribes to local build outcome events. Authoritative
// consumers (the slot results tracker) should pass blocking=true.
func (s *Service) SubscribeLocalBuild(capacity int, blocking bool) *utils.Subscription[*LocalBuildEvent] {
	return s.localBuildDispatcher.Subscribe(capacity, blocking)
}

// resolveLocalBuildRequest turns the slot's frozen local-build settings into
// the builder request, applying the runtime gates the plan cannot know: the
// probed availability of the testing namespace and the EL's blob handling.
// The second return is the skip reason when no local build runs (empty when a
// request is returned or the extension is simply off without a plan).
func (s *Service) resolveLocalBuildRequest(slot phase0.Slot) (*LocalBuildRequest, string) {
	frozen := s.planSvc.Freeze(slot)
	if frozen.Build == nil || frozen.Build.Local == nil {
		return nil, ""
	}

	settings := frozen.Build.Local
	if !settings.Enabled {
		return nil, ""
	}

	availability := s.LocalBuildAvailability()
	if !availability.Available {
		return nil, LocalSkipUnavailable
	}

	req := &LocalBuildRequest{
		TxSource:       settings.TxSource,
		PayloadSource:  settings.PayloadSource,
		BuildELPayload: settings.BuildELPayload,
		MaxTxs:         settings.MaxTxs,
		GasFillPct:     settings.GasFillPct,
		Ordering:       settings.Ordering,
		IncludeBlobTxs: availability.BlobBundle || settings.AllowBlobsWithoutBundle,
		BlobEncoding:   availability.BlobEncoding,
		MaxAttempts:    settings.MaxAttempts,
		MaxStrikes:     settings.MaxStrikes,
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
