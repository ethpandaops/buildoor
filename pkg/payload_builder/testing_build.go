package payload_builder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth-engine-client/spec/amsterdam"
	"github.com/ethpandaops/go-eth-engine-client/spec/cancun"
	"github.com/ethpandaops/go-eth-engine-client/spec/osaka"
	"github.com/ethpandaops/go-eth-engine-client/spec/prague"
	"github.com/ethpandaops/go-eth-engine-client/spec/shanghai"
	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/metrics"
	"github.com/ethpandaops/buildoor/pkg/tx_intake"
)

// testingBlockSizeMargin keeps the packed transaction bytes clear of geth's
// block size cap, which also counts the header and withdrawals.
const testingBlockSizeMargin = 64 << 10

// TestingBuildSpec is one slot's testing build instruction, resolved from
// the frozen plan and the live config by the service.
type TestingBuildSpec struct {
	Fill           tx_intake.FillSpec
	Deadline       time.Time // zero = no deadline beyond the build context
	MaxAttempts    int
	MaxStrikes     int
	QueueMaxAge    time.Duration
	BaseFeeCeiling *big.Int // wei; nil = off
}

// DroppedTx is a transaction removed from a build attempt after the EL
// refused the list.
type DroppedTx struct {
	Hash   common.Hash `json:"hash"`
	Reason string      `json:"reason"`
}

// TxPlan is the exact transaction list a testing build committed to, in
// order, with the accounting of how it was reached. The plan is the
// reference the built payload and the included block are checked against.
type TxPlan struct {
	Policy   string
	Hashes   []common.Hash
	GasSum   uint64
	GasCap   uint64
	Blobs    int
	Attempts int
	BuildMs  int64
	Skipped  map[string]int
	Evicted  []tx_intake.Eviction
	Dropped  []DroppedTx
}

// TxPlanCheck is the result of comparing a canonical block's transaction
// list against the plan it was built from.
type TxPlanCheck struct {
	Match         bool
	ActualCount   int
	FirstMismatch int // -1 when the lists agree up to the shorter length
	Detail        string
}

// TestingBuilder builds payloads from the tx intake queue through geth's
// testing_buildBlockV1 on the EL JSON-RPC (the testing namespace is served
// on the unauthenticated HTTP port only).
type TestingBuilder struct {
	rpc     *rpc.Client
	eth     *ethclient.Client
	queue   *tx_intake.Queue
	chainID *big.Int
	log     logrus.FieldLogger
}

// NewTestingBuilder dials the EL JSON-RPC and creates the intake queue.
func NewTestingBuilder(ctx context.Context, elRPC string, queueMaxTxs uint64, log logrus.FieldLogger) (*TestingBuilder, error) {
	rpcClient, err := rpc.DialContext(ctx, elRPC)
	if err != nil {
		return nil, fmt.Errorf("dialing EL RPC: %w", err)
	}

	ethClient := ethclient.NewClient(rpcClient)

	chainID, err := ethClient.ChainID(ctx)
	if err != nil {
		rpcClient.Close()
		return nil, fmt.Errorf("reading chain id: %w", err)
	}

	return &TestingBuilder{
		rpc:     rpcClient,
		eth:     ethClient,
		queue:   tx_intake.NewQueue(chainID, int(queueMaxTxs)),
		chainID: chainID,
		log:     log.WithField("component", "testing-builder"),
	}, nil
}

// Queue returns the intake queue.
func (t *TestingBuilder) Queue() *tx_intake.Queue { return t.queue }

// ChainID returns the EL chain id.
func (t *TestingBuilder) ChainID() *big.Int { return t.chainID }

// Close releases the RPC connection.
func (t *TestingBuilder) Close() { t.rpc.Close() }

// Probe verifies the EL serves the testing namespace. Only geth implements
// testing_buildBlockV1 today, and only when "testing" is in --http.api.
func (t *TestingBuilder) Probe(ctx context.Context) error {
	var modules map[string]string
	if err := t.rpc.CallContext(ctx, &modules, "rpc_modules"); err != nil {
		return fmt.Errorf("rpc_modules: %w", err)
	}

	if _, ok := modules["testing"]; !ok {
		return errors.New("EL RPC does not serve the testing namespace (geth: add testing to --http.api)")
	}

	return nil
}

// Build packs the queue for the block described by attrs on top of parent
// and builds it through testing_buildBlockV1. The EL must already have
// parent as its head. The returned payload holds exactly the plan's
// transactions in order; anything else is an error.
func (t *TestingBuilder) Build(
	ctx context.Context,
	parent common.Hash,
	attrs *engineall.PayloadAttributes,
	version enginev.DataVersion,
	targetGasLimit uint64,
	spec *TestingBuildSpec,
) (*engineall.GetPayloadResponse, *TxPlan, error) {
	started := time.Now()

	header, err := t.eth.HeaderByHash(ctx, parent)
	if err != nil {
		return nil, nil, fmt.Errorf("reading parent header: %w", err)
	}

	bctx, err := t.blockContext(ctx, header, attrs.Timestamp, targetGasLimit)
	if err != nil {
		return nil, nil, err
	}

	plan := &TxPlan{Policy: spec.Fill.Policy, Skipped: map[string]int{}}

	if spec.QueueMaxAge > 0 {
		plan.Evicted = append(plan.Evicted, t.queue.EvictExpired(spec.QueueMaxAge)...)
	}

	fill := spec.Fill
	if spec.BaseFeeCeiling != nil && bctx.BaseFee.Cmp(spec.BaseFeeCeiling) > 0 && fill.GasPct > 50 {
		t.log.WithFields(logrus.Fields{
			"base_fee": bctx.BaseFee, "ceiling": spec.BaseFeeCeiling,
		}).Warn("Next base fee above the ceiling, packing to the 1559 target instead of the full limit")

		fill.GasPct = 50
	}

	// One snapshot for the whole pack: senderStates and Pack must see the
	// same queue, or a sender that arrives in between has no parent state
	// and the slot fails.
	snapshot := t.queue.Snapshot()

	states, err := t.senderStates(ctx, parent, snapshot)
	if err != nil {
		return nil, nil, err
	}

	packed, err := tx_intake.Pack(t.queue, snapshot, states, bctx, fill)
	if err != nil {
		return nil, nil, fmt.Errorf("packing: %w", err)
	}

	plan.GasCap = packed.GasCap
	plan.Skipped = packed.Skipped
	plan.Evicted = append(plan.Evicted, packed.Evicted...)

	resp, entries, err := t.buildWithRetries(ctx, parent, attrs, version, packed.Entries, spec, plan)
	if err != nil {
		return nil, nil, err
	}

	plan.Hashes = make([]common.Hash, len(entries))
	for i, e := range entries {
		plan.Hashes[i] = e.Hash()
		plan.GasSum += e.Tx.Gas()
		plan.Blobs += len(e.Tx.BlobHashes())
	}

	plan.BuildMs = time.Since(started).Milliseconds()

	if err := verifyBuiltPayload(resp.ExecutionPayload, plan.Hashes); err != nil {
		return nil, nil, err
	}

	metrics.TestingBuildSeconds.Observe(time.Since(started).Seconds())
	metrics.TestingBuildTxs.Observe(float64(len(plan.Hashes)))
	metrics.TestingQueueTxs.Set(float64(t.queue.Stats().Txs))

	if gl := resp.ExecutionPayload.GasLimit; gl > 0 {
		metrics.TestingBuildFillRatio.Set(float64(resp.ExecutionPayload.GasUsed) / float64(gl))
	}

	for _, ev := range plan.Evicted {
		metrics.TestingQueueEvictions.WithLabelValues(ev.Reason).Inc()
	}

	t.log.WithFields(logrus.Fields{
		"parent":   fmt.Sprintf("%x", parent[:8]),
		"txs":      len(plan.Hashes),
		"gas_sum":  plan.GasSum,
		"gas_cap":  plan.GasCap,
		"gas_used": resp.ExecutionPayload.GasUsed,
		"blobs":    plan.Blobs,
		"attempts": plan.Attempts,
		"build_ms": plan.BuildMs,
		"skipped":  plan.Skipped,
		"dropped":  len(plan.Dropped),
	}).Info("Testing build complete")

	return resp, plan, nil
}

// buildWithRetries calls testing_buildBlockV1 and, on an attributable EL
// failure, drops the offending transactions and retries within the attempt
// budget. Dropped transactions get a strike in the queue.
func (t *TestingBuilder) buildWithRetries(
	ctx context.Context,
	parent common.Hash,
	attrs *engineall.PayloadAttributes,
	version enginev.DataVersion,
	entries []*tx_intake.Entry,
	spec *TestingBuildSpec,
	plan *TxPlan,
) (*engineall.GetPayloadResponse, []*tx_intake.Entry, error) {
	maxAttempts := spec.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	for attempt := 1; ; attempt++ {
		plan.Attempts = attempt

		resp, err := t.buildBlock(ctx, parent, attrs, version, entries)
		if err == nil {
			return resp, entries, nil
		}

		if ctx.Err() != nil {
			return nil, nil, fmt.Errorf("testing_buildBlockV1 attempt %d: %w", attempt, err)
		}

		// An explicit list is a contract: the block holds exactly these
		// transactions in this order. Dropping the offender would silently
		// build a different block and then verify "match" against the
		// reduced plan, so an exact plan fails instead of retrying.
		if spec.Fill.Policy == tx_intake.PolicyAsGiven {
			return nil, nil, fmt.Errorf(
				"testing_buildBlockV1 refused the explicit transaction list (policy as_given, not trimmed): %w", err)
		}

		attribution := tx_intake.Attribute(err, entries)
		kept, dropped := attribution.Apply(entries)

		if len(dropped) == 0 || attempt >= maxAttempts {
			return nil, nil, fmt.Errorf("testing_buildBlockV1 attempt %d/%d (%s): %w",
				attempt, maxAttempts, attribution.Reason, err)
		}

		for _, e := range dropped {
			plan.Dropped = append(plan.Dropped, DroppedTx{Hash: e.Hash(), Reason: attribution.Reason})

			if ev, out := t.queue.Strike(e.Hash(), spec.MaxStrikes); out {
				plan.Evicted = append(plan.Evicted, ev)
			}
		}

		t.log.WithError(err).WithFields(logrus.Fields{
			"attempt": attempt,
			"reason":  attribution.Reason,
			"dropped": len(dropped),
			"kept":    len(kept),
		}).Warn("EL refused the transaction list, retrying without the attributed transactions")

		entries = kept
	}
}

// buildBlock is one testing_buildBlockV1 call decoded into the fork-agnostic
// getPayload response (geth returns the very same envelope shape).
func (t *TestingBuilder) buildBlock(
	ctx context.Context,
	parent common.Hash,
	attrs *engineall.PayloadAttributes,
	version enginev.DataVersion,
	entries []*tx_intake.Entry,
) (*engineall.GetPayloadResponse, error) {
	raw := make([]hexutil.Bytes, len(entries))
	for i, e := range entries {
		raw[i] = e.Raw
	}

	view, err := payloadViewFor(version)
	if err != nil {
		return nil, err
	}

	var result json.RawMessage
	if err := t.rpc.CallContext(ctx, &result, "testing_buildBlockV1", parent, attrs, raw, hexutil.Bytes{}); err != nil {
		return nil, err
	}

	if err := json.Unmarshal(result, view); err != nil {
		return nil, fmt.Errorf("decoding testing_buildBlockV1 result: %w", err)
	}

	out := &engineall.GetPayloadResponse{Version: version}
	if err := out.FromView(view); err != nil {
		return nil, fmt.Errorf("converting testing_buildBlockV1 result: %w", err)
	}

	if out.ExecutionPayload == nil {
		return nil, errors.New("testing_buildBlockV1 returned no execution payload")
	}

	return out, nil
}

func payloadViewFor(version enginev.DataVersion) (any, error) {
	switch version {
	case enginev.DataVersionShanghai:
		return &shanghai.GetPayloadResponse{}, nil
	case enginev.DataVersionCancun:
		return &cancun.GetPayloadResponse{}, nil
	case enginev.DataVersionPrague:
		return &prague.GetPayloadResponse{}, nil
	case enginev.DataVersionOsaka:
		return &osaka.GetPayloadResponse{}, nil
	case enginev.DataVersionAmsterdam, enginev.DataVersionBogota:
		return &amsterdam.GetPayloadResponse{}, nil
	default:
		return nil, fmt.Errorf("testing build: unsupported engine version %s", version)
	}
}

// blockContext derives the packer's caps for the block after parent: the gas
// limit geth will set, the next base fee, a conservative next blob base fee
// and the fork's blob cap.
func (t *TestingBuilder) blockContext(ctx context.Context, parent *types.Header, timestamp, targetGasLimit uint64) (*tx_intake.BlockContext, error) {
	ceil := targetGasLimit
	if ceil == 0 {
		ceil = parent.GasLimit
	}

	bctx := &tx_intake.BlockContext{
		ParentHash: parent.Hash(),
		GasLimit:   calcGasLimit(parent.GasLimit, ceil),
		BaseFee:    eip1559.CalcBaseFee(&params.ChainConfig{LondonBlock: big.NewInt(0)}, parent),
		MaxBytes:   params.MaxBlockSize - testingBlockSizeMargin,
	}

	maxBlobs, err := t.maxBlobs(ctx, timestamp)
	if err != nil {
		return nil, err
	}

	bctx.MaxBlobs = maxBlobs

	if maxBlobs > 0 {
		// eth_blobBaseFee is the head's blob fee; the next block's is at most
		// one update step higher, so filter against that bound.
		var headBlobFee hexutil.Big
		if err := t.rpc.CallContext(ctx, &headBlobFee, "eth_blobBaseFee"); err != nil {
			return nil, fmt.Errorf("eth_blobBaseFee: %w", err)
		}

		fee := headBlobFee.ToInt()
		fee.Mul(fee, big.NewInt(9))
		fee.Div(fee, big.NewInt(8))
		bctx.BlobBaseFee = fee
	}

	return bctx, nil
}

// calcGasLimit mirrors geth's core.CalcGasLimit: the parent gas limit stepped
// toward the desired limit by at most parent/1024 - 1 (kept local to avoid
// importing geth's core package).
func calcGasLimit(parentGasLimit, desiredLimit uint64) uint64 {
	delta := parentGasLimit/params.GasLimitBoundDivisor - 1
	limit := parentGasLimit

	if desiredLimit < params.MinGasLimit {
		desiredLimit = params.MinGasLimit
	}

	if limit < desiredLimit {
		limit = parentGasLimit + delta
		if limit > desiredLimit {
			limit = desiredLimit
		}

		return limit
	}

	if limit > desiredLimit {
		limit = parentGasLimit - delta
		if limit < desiredLimit {
			limit = desiredLimit
		}
	}

	return limit
}

// maxBlobs reads the blob cap that applies at the block timestamp from the
// EL's eth_config (EIP-7910): the next schedule once it has activated.
func (t *TestingBuilder) maxBlobs(ctx context.Context, timestamp uint64) (int, error) {
	type schedule struct {
		ActivationTime uint64 `json:"activationTime"`
		BlobSchedule   *struct {
			Max uint64 `json:"max"`
		} `json:"blobSchedule"`
	}

	var cfg struct {
		Current *schedule `json:"current"`
		Next    *schedule `json:"next"`
	}

	if err := t.rpc.CallContext(ctx, &cfg, "eth_config"); err != nil {
		return 0, fmt.Errorf("eth_config: %w", err)
	}

	active := cfg.Current
	if cfg.Next != nil && cfg.Next.ActivationTime <= timestamp {
		active = cfg.Next
	}

	if active == nil || active.BlobSchedule == nil {
		return 0, nil
	}

	return int(active.BlobSchedule.Max), nil
}

// senderStates fetches nonce and balance at the parent block for every sender
// in the given queue snapshot, in one batch.
func (t *TestingBuilder) senderStates(ctx context.Context, parent common.Hash, snapshot map[common.Address][]*tx_intake.Entry) (map[common.Address]tx_intake.SenderState, error) {
	states := make(map[common.Address]tx_intake.SenderState, len(snapshot))

	if len(snapshot) == 0 {
		return states, nil
	}

	senders := make([]common.Address, 0, len(snapshot))
	for sender := range snapshot {
		senders = append(senders, sender)
	}

	at := rpc.BlockNumberOrHashWithHash(parent, false)
	nonces := make([]hexutil.Uint64, len(senders))
	balances := make([]hexutil.Big, len(senders))
	batch := make([]rpc.BatchElem, 0, 2*len(senders))

	for i, sender := range senders {
		batch = append(batch,
			rpc.BatchElem{Method: "eth_getTransactionCount", Args: []any{sender, at}, Result: &nonces[i]},
			rpc.BatchElem{Method: "eth_getBalance", Args: []any{sender, at}, Result: &balances[i]},
		)
	}

	if err := t.rpc.BatchCallContext(ctx, batch); err != nil {
		return nil, fmt.Errorf("fetching sender states: %w", err)
	}

	for _, elem := range batch {
		if elem.Error != nil {
			return nil, fmt.Errorf("fetching sender states: %s: %w", elem.Method, elem.Error)
		}
	}

	for i, sender := range senders {
		states[sender] = tx_intake.SenderState{Nonce: uint64(nonces[i]), Balance: balances[i].ToInt()}
	}

	return states, nil
}

// verifyBuiltPayload checks that the EL built exactly the planned list.
func verifyBuiltPayload(payload *engineall.ExecutionPayload, expected []common.Hash) error {
	if len(payload.Transactions) != len(expected) {
		return fmt.Errorf("built payload holds %d transactions, plan has %d", len(payload.Transactions), len(expected))
	}

	for i, raw := range payload.Transactions {
		tx := new(types.Transaction)
		if err := tx.UnmarshalBinary(raw); err != nil {
			return fmt.Errorf("built payload tx %d does not decode: %w", i, err)
		}

		if tx.Hash() != expected[i] {
			return fmt.Errorf("built payload tx %d is %s, plan expects %s", i, tx.Hash().Hex(), expected[i].Hex())
		}
	}

	return nil
}

// VerifyBlock compares the canonical block's transaction list with the
// plan. It waits for the EL to know the block, bounded by ctx.
func (t *TestingBuilder) VerifyBlock(ctx context.Context, blockHash common.Hash, expected []common.Hash) (*TxPlanCheck, error) {
	var block *struct {
		Transactions []common.Hash `json:"transactions"`
	}

	for {
		if err := t.rpc.CallContext(ctx, &block, "eth_getBlockByHash", blockHash, false); err != nil {
			return nil, fmt.Errorf("eth_getBlockByHash: %w", err)
		}

		if block != nil {
			break
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("block %s not known to the EL: %w", blockHash.Hex(), ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}

	return CompareTxLists(expected, block.Transactions), nil
}

// CompareTxLists is the order- and count-sensitive comparison of an expected
// transaction list against what a block holds.
func CompareTxLists(expected, actual []common.Hash) *TxPlanCheck {
	check := &TxPlanCheck{ActualCount: len(actual), FirstMismatch: -1}

	for i := 0; i < len(expected) && i < len(actual); i++ {
		if expected[i] != actual[i] {
			check.FirstMismatch = i
			check.Detail = fmt.Sprintf("position %d: expected %s, block has %s", i, expected[i].Hex(), actual[i].Hex())

			return check
		}
	}

	if len(expected) != len(actual) {
		check.Detail = fmt.Sprintf("expected %d transactions, block has %d", len(expected), len(actual))
		return check
	}

	check.Match = true

	return check
}
