package payload_builder

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth-engine-client/spec/paris"
	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/ethpandaops/go-eth2-client/spec/version"
	"github.com/holiman/uint256"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
	"github.com/ethpandaops/buildoor/pkg/txpool"
)

func TestLocalBuildRequestRunEL(t *testing.T) {
	require.True(t, (*LocalBuildRequest)(nil).RunEL(), "no local request = engine build only")
	require.True(t, (&LocalBuildRequest{PayloadSource: config.PayloadSourceEL}).RunEL())
	require.True(t, (&LocalBuildRequest{PayloadSource: config.PayloadSourceLocalOrEL, BuildELPayload: false}).RunEL(),
		"local_or_el needs the engine payload as fallback")
	require.True(t, (&LocalBuildRequest{PayloadSource: config.PayloadSourceHigherValue, BuildELPayload: false}).RunEL(),
		"higher_value compares against the engine payload")
	require.True(t, (&LocalBuildRequest{PayloadSource: config.PayloadSourceLocal, BuildELPayload: true}).RunEL())
	require.False(t, (&LocalBuildRequest{PayloadSource: config.PayloadSourceLocal, BuildELPayload: false}).RunEL())
}

func TestSelectPayload(t *testing.T) {
	el := &Payload{Source: SourceEL, BlockHash: phase0.Hash32{1}}
	local := &Payload{Source: SourceLocal, BlockHash: phase0.Hash32{2}}
	richEL := &Payload{Source: SourceEL, BlockHash: phase0.Hash32{3}, BlockValue: big.NewInt(500)}
	richLocal := &Payload{Source: SourceLocal, BlockHash: phase0.Hash32{4}, BlockValue: big.NewInt(500)}
	b := &PayloadBuilder{}

	tests := []struct {
		name       string
		result     BuildResult
		req        *LocalBuildRequest
		wantSource string
		wantHash   phase0.Hash32
		wantErr    string
		fallback   bool
	}{
		{
			name: "no local request uses el", result: BuildResult{EL: el},
			wantSource: SourceEL, wantHash: el.BlockHash,
		},
		{
			name: "el failure without local request errors", result: BuildResult{ELErr: errors.New("boom")},
			wantErr: "boom",
		},
		{
			name: "source el keeps el even when local succeeded", result: BuildResult{EL: el, Local: local},
			req: &LocalBuildRequest{PayloadSource: config.PayloadSourceEL}, wantSource: SourceEL, wantHash: el.BlockHash,
		},
		{
			name: "source local uses local", result: BuildResult{EL: el, Local: local},
			req: &LocalBuildRequest{PayloadSource: config.PayloadSourceLocal}, wantSource: SourceLocal, wantHash: local.BlockHash,
		},
		{
			name: "source local strict fails without local", result: BuildResult{EL: el, LocalErr: errors.New("nonce too low")},
			req: &LocalBuildRequest{PayloadSource: config.PayloadSourceLocal}, wantErr: "local build failed: nonce too low",
		},
		{
			name: "source local strict reports skip reason", result: BuildResult{EL: el, LocalSkipReason: LocalSkipTxPoolDisabled},
			req: &LocalBuildRequest{PayloadSource: config.PayloadSourceLocal}, wantErr: "local build skipped: txpool_disabled",
		},
		{
			name: "local_or_el prefers local", result: BuildResult{EL: el, Local: local},
			req: &LocalBuildRequest{PayloadSource: config.PayloadSourceLocalOrEL}, wantSource: SourceLocal, wantHash: local.BlockHash,
		},
		{
			name: "local_or_el falls back to el", result: BuildResult{EL: el, LocalErr: errors.New("x")},
			req: &LocalBuildRequest{PayloadSource: config.PayloadSourceLocalOrEL}, wantSource: SourceEL, wantHash: el.BlockHash, fallback: true,
		},
		{
			name: "higher_value takes the more valuable local payload", result: BuildResult{EL: el, Local: richLocal},
			req: &LocalBuildRequest{PayloadSource: config.PayloadSourceHigherValue}, wantSource: SourceLocal, wantHash: richLocal.BlockHash,
		},
		{
			name: "higher_value takes the more valuable el payload", result: BuildResult{EL: richEL, Local: local},
			req: &LocalBuildRequest{PayloadSource: config.PayloadSourceHigherValue}, wantSource: SourceEL, wantHash: richEL.BlockHash,
		},
		{
			name: "higher_value prefers el on a tie", result: BuildResult{EL: el, Local: local},
			req: &LocalBuildRequest{PayloadSource: config.PayloadSourceHigherValue}, wantSource: SourceEL, wantHash: el.BlockHash,
		},
		{
			name: "higher_value uses el when local failed", result: BuildResult{EL: el, LocalErr: errors.New("x")},
			req: &LocalBuildRequest{PayloadSource: config.PayloadSourceHigherValue}, wantSource: SourceEL, wantHash: el.BlockHash,
		},
		{
			name: "higher_value uses local when el failed", result: BuildResult{ELErr: errors.New("el down"), Local: local},
			req: &LocalBuildRequest{PayloadSource: config.PayloadSourceHigherValue}, wantSource: SourceLocal, wantHash: local.BlockHash,
		},
		{
			name: "local_or_el with both failed errors", result: BuildResult{ELErr: errors.New("el down"), LocalErr: errors.New("x")},
			req: &LocalBuildRequest{PayloadSource: config.PayloadSourceLocalOrEL}, wantErr: "el down",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.result
			out, err := b.selectPayload(&result, tc.req)
			require.NotNil(t, out, "the result is always returned for inspection")

			if tc.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErr)
				require.Nil(t, out.Payload)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.wantSource, out.Source)
			require.Equal(t, tc.wantHash, out.Payload.BlockHash)
			require.Equal(t, tc.fallback, out.Fallback)
		})
	}
}

// fakeLocalClient records the testing_buildBlockV1 calls and answers with a
// minimal payload echoing the submitted transactions (or fails the first N
// calls with failErr, "nonce too low" by default).
type fakeLocalClient struct {
	calls    [][][]byte
	failNext int
	failErr  error
	dropLast bool
}

func (f *fakeLocalClient) BuildBlockV1(_ context.Context, v enginev.DataVersion, parent common.Hash,
	_ *engineall.PayloadAttributes, txs [][]byte, _ []byte,
) (*engineall.GetPayloadResponse, error) {
	f.calls = append(f.calls, txs)

	if f.failNext > 0 {
		f.failNext--

		if f.failErr != nil {
			return nil, f.failErr
		}

		return nil, errors.New("nonce too low")
	}

	included := make([]paris.Transaction, 0, len(txs))
	for i, tx := range txs {
		if f.dropLast && i == len(txs)-1 {
			continue
		}

		included = append(included, paris.Transaction(tx))
	}

	payload := &engineall.ExecutionPayload{
		Version:       v,
		ParentHash:    paris.Hash32(parent),
		BaseFeePerGas: uint256.NewInt(7),
		Transactions:  included,
	}

	// The extra-data modifier verifies it can reconstruct the block hash, so
	// the fake payload must carry the hash of its own header.
	header, err := buildHeaderFromPayload(payload, common.Hash{}, nil)
	if err != nil {
		return nil, err
	}

	payload.BlockHash = paris.Hash32(header.Hash())

	return &engineall.GetPayloadResponse{
		Version:          v,
		ExecutionPayload: payload,
		BlockValue:       uint256.NewInt(42),
	}, nil
}

func (f *fakeLocalClient) ProbeTestingAPI(context.Context) execution.TestingAPIStatus {
	return execution.TestingAPIStatus{Available: true}
}

func (f *fakeLocalClient) ClientVersion(context.Context) (string, error) {
	return "Geth/v1.17.6-unstable/linux-amd64/go1.26.5", nil
}

func TestELCodeFromClientVersion(t *testing.T) {
	for version, code := range map[string]string{
		"Geth/v1.17.6-unstable-aa1f2fcf-20260813/linux-amd64/go1.26.5":                      "GE",
		"Nethermind/v1.40.0-unstable+93ca2644-hp/linux-x64/dotnet10.0.11":                   "NM",
		"besu/v26.9-develop-0d7d0f5/linux-x86_64/openjdk-java-25":                           "BU",
		"reth/v2.5.0-3d270d9/x86_64-unknown-linux-gnu":                                      "RH",
		"ethrex/v22.0.0-glamsterdam-devnet-8-092813de/x86_64-unknown-linux-gnu/rustc-v1.91": "EX",
		"erigon/3.7.0/linux-amd64/go1.26.7":                                                 "EG",
		"something/else":                                                                    "",
		"":                                                                                  "",
	} {
		require.Equal(t, code, elCodeFromClientVersion(version), version)
	}
}

// signedTx returns the network encoding of a freshly signed transaction, so
// the fake's echoed payload decodes in the header reconstruction.
func signedTx(t *testing.T, nonce uint64) []byte {
	t.Helper()

	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	chainID := big.NewInt(1)
	tx, err := types.SignNewTx(key, types.LatestSignerForChainID(chainID), &types.DynamicFeeTx{
		ChainID: chainID, Nonce: nonce, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2), Gas: 21000,
	})
	require.NoError(t, err)

	raw, err := tx.MarshalBinary()
	require.NoError(t, err)

	return raw
}

// stubChain is the chain.Service surface buildLocal touches.
type stubChain struct {
	stubChainService
}

func newLocalTestBuilder(client LocalBuildClient) (*PayloadBuilder, *beacon.PayloadAttributesEvent, *buildPrelude) {
	cfg := config.DefaultConfig()
	cfg.ExtraData = "buildoor/"

	chainSvc := &stubChain{stubChainService{spec: &chain.ChainSpec{SlotsPerEpoch: 32}}}
	b := NewPayloadBuilder(nil, nil, client, nil, chainSvc, common.Address{}, cfg, logrus.New(), nil)

	attrs := &beacon.PayloadAttributesEvent{
		ProposalSlot:    100,
		ParentBlockHash: phase0.Hash32{0xaa},
	}

	prelude := &buildPrelude{
		beaconFork:    version.DataVersionElectra,
		engineVersion: enginev.DataVersionPrague,
		payloadAttrs:  &engineall.PayloadAttributes{Version: enginev.DataVersionPrague},
	}

	return b, attrs, prelude
}

func TestBuildLocalTxSources(t *testing.T) {
	t.Run("explicit list is submitted as given", func(t *testing.T) {
		client := &fakeLocalClient{}
		b, attrs, prelude := newLocalTestBuilder(client)

		txs := [][]byte{signedTx(t, 0), signedTx(t, 1)}

		out := b.buildLocal(context.Background(), attrs, prelude, &LocalBuildRequest{
			TxSource:     config.TxSourceExplicit,
			Transactions: txs,
		})
		require.NoError(t, out.err)
		require.Empty(t, out.skipReason)
		require.NotNil(t, out.payload)
		require.Equal(t, SourceLocal, out.payload.Source)
		require.Len(t, client.calls, 1)
		require.Equal(t, txs, client.calls[0])
		require.Equal(t, 2, out.info.SubmittedTxs)
		require.Equal(t, 2, out.info.ExplicitTxs)
		require.Equal(t, 0, out.info.DroppedByEL)
		require.Equal(t, txHashes(txs), out.info.ExpectedHashes)
		require.Equal(t, big.NewInt(42), out.payload.BlockValue)
	})

	t.Run("empty source submits an empty list, never null", func(t *testing.T) {
		client := &fakeLocalClient{}
		b, attrs, prelude := newLocalTestBuilder(client)

		out := b.buildLocal(context.Background(), attrs, prelude, &LocalBuildRequest{TxSource: config.TxSourceEmpty})
		require.NoError(t, out.err)
		require.NotNil(t, client.calls[0], "[] must reach the EL (empty block), not null (EL mempool)")
		require.Len(t, client.calls[0], 0)
		require.Equal(t, 0, out.info.SubmittedTxs)
	})

	t.Run("el_mempool source submits null", func(t *testing.T) {
		client := &fakeLocalClient{}
		b, attrs, prelude := newLocalTestBuilder(client)

		out := b.buildLocal(context.Background(), attrs, prelude, &LocalBuildRequest{TxSource: config.TxSourceELMempool})
		require.NoError(t, out.err)
		require.Nil(t, client.calls[0])
		require.Equal(t, -1, out.info.SubmittedTxs)
	})

	t.Run("txpool source without a pool is skipped", func(t *testing.T) {
		client := &fakeLocalClient{}
		b, attrs, prelude := newLocalTestBuilder(client)

		out := b.buildLocal(context.Background(), attrs, prelude, &LocalBuildRequest{TxSource: config.TxSourceTxPool})
		require.Equal(t, LocalSkipTxPoolUnavailable, out.skipReason)
		require.Empty(t, client.calls)
	})

	t.Run("inclusion list is prepended and dropped on retry", func(t *testing.T) {
		client := &fakeLocalClient{failNext: 1}
		b, attrs, prelude := newLocalTestBuilder(client)
		il := signedTx(t, 5)
		own := signedTx(t, 0)
		attrs.InclusionListTransactions = [][]byte{il}

		out := b.buildLocal(context.Background(), attrs, prelude, &LocalBuildRequest{
			TxSource:     config.TxSourceExplicit,
			Transactions: [][]byte{own},
		})
		require.NoError(t, out.err)
		require.Len(t, client.calls, 2)
		require.Equal(t, [][]byte{il, own}, client.calls[0], "first attempt carries the inclusion list first")
		require.Equal(t, [][]byte{own}, client.calls[1], "retry without the inclusion list")
		require.True(t, out.info.InclusionListDropped)
		require.Equal(t, 1, out.info.InclusionListTxs)
	})

	t.Run("silently filtering EL fails the build", func(t *testing.T) {
		client := &fakeLocalClient{dropLast: true}
		b, attrs, prelude := newLocalTestBuilder(client)

		txs := [][]byte{signedTx(t, 0), signedTx(t, 1), signedTx(t, 2)}

		out := b.buildLocal(context.Background(), attrs, prelude, &LocalBuildRequest{
			TxSource:     config.TxSourceExplicit,
			Transactions: txs,
		})
		require.Error(t, out.err)
		require.Contains(t, out.err.Error(), "deviates from the plan")
		require.Nil(t, out.payload, "a payload that is not the plan is never handed to the bidders")
		require.Equal(t, 1, out.info.DroppedByEL)
		require.Equal(t, txHashes(txs), out.info.ExpectedHashes)
	})

	t.Run("queued source without a pool is skipped", func(t *testing.T) {
		client := &fakeLocalClient{}
		b, attrs, prelude := newLocalTestBuilder(client)

		out := b.buildLocal(context.Background(), attrs, prelude, &LocalBuildRequest{
			TxSource: config.TxSourceQueued,
			Queued:   []common.Hash{{1}},
		})
		require.Equal(t, LocalSkipTxPoolUnavailable, out.skipReason)
	})

	t.Run("EL error propagates", func(t *testing.T) {
		client := &fakeLocalClient{failNext: 5}
		b, attrs, prelude := newLocalTestBuilder(client)

		out := b.buildLocal(context.Background(), attrs, prelude, &LocalBuildRequest{TxSource: config.TxSourceEmpty})
		require.Error(t, out.err)
		require.Nil(t, out.payload)
		require.Len(t, client.calls, 1, "no inclusion list, no retry")
	})

	t.Run("unconfigured client skips as unavailable", func(t *testing.T) {
		b, attrs, prelude := newLocalTestBuilder(nil)

		out := b.buildLocal(context.Background(), attrs, prelude, &LocalBuildRequest{TxSource: config.TxSourceEmpty})
		require.Equal(t, LocalSkipUnavailable, out.skipReason)
	})
}

// fakePoolEL is the txpool ELClient for builder-level pool tests: every
// account is funded at nonce 0.
// fakePoolEL is the pool's EL: the parent state, and the EL mempool the
// mirror reads (txpool_content pending; no filter support).
type fakePoolEL struct {
	pending    []*types.Transaction
	pendingErr error
}

func (f *fakePoolEL) PendingPoolTransactions(context.Context) ([]*types.Transaction, error) {
	return f.pending, f.pendingErr
}

func (f *fakePoolEL) NewPendingTransactionFilter(context.Context) (string, error) {
	return "", errors.New("filters not supported")
}

func (f *fakePoolEL) PendingTransactionFilterChanges(context.Context, string) ([]common.Hash, error) {
	return nil, errors.New("filters not supported")
}

func (f *fakePoolEL) UninstallFilter(context.Context, string) error { return nil }

func (f *fakePoolEL) TransactionsByHash(context.Context, []common.Hash) (map[common.Hash]*types.Transaction, error) {
	return map[common.Hash]*types.Transaction{}, nil
}

func (f *fakePoolEL) GetChainID(context.Context) (*big.Int, error) { return big.NewInt(1), nil }
func (f *fakePoolEL) HeaderByHash(context.Context, common.Hash) (*types.Header, error) {
	return &types.Header{Number: big.NewInt(1), GasLimit: 30_000_000, GasUsed: 15_000_000, BaseFee: big.NewInt(1)}, nil
}

func (f *fakePoolEL) AccountStatesAt(_ context.Context, addrs []common.Address, _ common.Hash, _ uint64,
) (map[common.Address]*execution.AccountState, error) {
	out := make(map[common.Address]*execution.AccountState, len(addrs))
	for _, a := range addrs {
		out[a] = &execution.AccountState{Nonce: 0, Balance: new(big.Int).Mul(big.NewInt(100), big.NewInt(1e18))}
	}

	return out, nil
}

func (f *fakePoolEL) BlobBaseFee(context.Context) (*big.Int, error) { return big.NewInt(1), nil }
func (f *fakePoolEL) BlockTransactions(context.Context, common.Hash) (*execution.BlockTxList, bool, error) {
	return nil, false, nil
}

func (f *fakePoolEL) TransactionBlockHashes(context.Context, []common.Hash) (map[common.Hash]common.Hash, error) {
	return map[common.Hash]common.Hash{}, nil
}

func (f *fakePoolEL) SendRawTransaction(context.Context, []byte) (common.Hash, error) {
	return common.Hash{}, nil
}

func TestBuildLocalAttributedRetry(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.TxPool.Enabled = true

	pool := txpool.NewPool(cfg, &fakePoolEL{}, &stubChain{stubChainService{spec: &chain.ChainSpec{SlotsPerEpoch: 32}}}, nil, logrus.New())
	require.NoError(t, pool.Start(context.Background()))

	t.Cleanup(func() { _ = pool.Stop() })

	keyA, err := crypto.GenerateKey()
	require.NoError(t, err)

	keyB, err := crypto.GenerateKey()
	require.NoError(t, err)

	sign := func(key *ecdsa.PrivateKey, nonce uint64) *types.Transaction {
		tx, err := types.SignNewTx(key, types.LatestSignerForChainID(big.NewInt(1)), &types.DynamicFeeTx{
			ChainID: big.NewInt(1), Nonce: nonce, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2), Gas: 21000,
		})
		require.NoError(t, err)

		raw, err := tx.MarshalBinary()
		require.NoError(t, err)

		_, err = pool.Add(raw)
		require.NoError(t, err)

		return tx
	}

	txA0 := sign(keyA, 0)
	txB0 := sign(keyB, 0)
	txA1 := sign(keyA, 1)
	addrA := crypto.PubkeyToAddress(keyA.PublicKey)

	// The EL refuses sender A's nonce on the first attempt.
	client := &fakeLocalClient{failNext: 1, failErr: errors.New("nonce too low: address " + addrA.Hex() + ", tx: 0 state: 1")}
	b, attrs, prelude := newLocalTestBuilder(client)
	b.txPool = pool

	out := b.buildLocal(context.Background(), attrs, prelude, &LocalBuildRequest{
		TxSource:    config.TxSourceTxPool,
		MaxAttempts: 3,
		MaxStrikes:  3,
		Ordering:    config.TxOrderingFIFO,
		GasFillPct:  100,
	})
	require.NoError(t, out.err)
	require.NotNil(t, out.payload)
	require.Len(t, client.calls, 2)
	require.Len(t, client.calls[0], 3, "first attempt carries every pooled transaction")
	require.Len(t, client.calls[1], 1, "the retry carries only sender B")
	require.Equal(t, 2, out.info.Attempts)
	require.Len(t, out.info.Dropped, 2)
	require.Equal(t, "nonce_too_low", out.info.Dropped[0].Reason)
	require.Equal(t, []string{txB0.Hash().Hex()}, out.info.ExpectedHashes)
	require.Equal(t, 1, pool.Get(txA0.Hash()).Strikes())
	require.Equal(t, 1, pool.Get(txA1.Hash()).Strikes())

	// Exact sources never retry.
	client = &fakeLocalClient{failNext: 1, failErr: errors.New("nonce too low: address " + addrA.Hex())}
	b, attrs, prelude = newLocalTestBuilder(client)
	b.txPool = pool

	out = b.buildLocal(context.Background(), attrs, prelude, &LocalBuildRequest{
		TxSource:    config.TxSourceQueued,
		Queued:      []common.Hash{txA0.Hash(), txB0.Hash()},
		MaxAttempts: 3,
	})
	require.Error(t, out.err)
	require.Len(t, client.calls, 1)
	require.Equal(t, 1, pool.Get(txA0.Hash()).Strikes(), "no strike for an exact plan's refusal")
}

func TestResolveLocalBuildRequestBuildsCanonicalCandidateOnly(t *testing.T) {
	spec := &chain.ChainSpec{SecondsPerSlot: 12 * time.Second, SlotsPerEpoch: 32}
	svc := supersedeTestService(t, &stubChainService{spec: spec})
	svc.cfg.LocalBuild.Enabled = true
	svc.cfg.LocalBuild.StartDelayMs = 900
	svc.localBuild.availability = LocalBuildAvailability{Configured: true, Available: true, BlobBundle: true}

	tests := []struct {
		name      string
		candidate chain.CandidateKey
		wantReq   bool
		wantSkip  string
	}{
		{name: "canonical", candidate: chain.CandidateParentFull, wantReq: true},
		{name: "unclassified tuple", candidate: "", wantReq: true},
		{name: "parent_empty", candidate: chain.CandidateParentEmpty, wantSkip: LocalSkipSpeculativeCandidate},
		{name: "grandparent_full", candidate: chain.CandidateGrandparentFull, wantSkip: LocalSkipSpeculativeCandidate},
		{name: "grandparent_empty", candidate: chain.CandidateGrandparentEmpty, wantSkip: LocalSkipSpeculativeCandidate},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh slot per case: Freeze snapshots the first resolution.
			req, skip := svc.resolveLocalBuildRequest(phase0.Slot(1000+i), tc.candidate)

			require.Equal(t, tc.wantSkip, skip)
			require.Equal(t, tc.wantReq, req != nil)

			if req != nil {
				require.Equal(t, 900*time.Millisecond, req.StartDelay, "the configured start delay reaches the request")
			}
		})
	}
}

// TestBuildLocalELFill appends the EL mempool's pending transactions after the
// pool selection and retries without them when the EL refuses the list.
func TestBuildLocalELFill(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.TxPool.Enabled = true

	sign := func(nonce uint64) *types.Transaction {
		key, err := crypto.GenerateKey()
		require.NoError(t, err)

		tx, err := types.SignNewTx(key, types.LatestSignerForChainID(big.NewInt(1)), &types.DynamicFeeTx{
			ChainID: big.NewInt(1), Nonce: nonce, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2), Gas: 21000,
		})
		require.NoError(t, err)

		return tx
	}

	own := sign(0)
	raw, err := own.MarshalBinary()
	require.NoError(t, err)

	elTx := sign(0)

	// A fresh pool per case: its EL mempool mirror keeps what it read.
	newPool := func(el *fakePoolEL) *txpool.Pool {
		pool := txpool.NewPool(cfg, el, &stubChain{stubChainService{spec: &chain.ChainSpec{SlotsPerEpoch: 32}}}, nil, logrus.New())
		require.NoError(t, pool.Start(context.Background()))

		t.Cleanup(func() { _ = pool.Stop() })

		_, err := pool.Add(raw)
		require.NoError(t, err)

		return pool
	}

	req := &LocalBuildRequest{
		TxSource:    config.TxSourceTxPoolELFill,
		MaxAttempts: 3,
		Ordering:    config.TxOrderingFIFO,
		GasFillPct:  100,
	}

	t.Run("pool first, then the EL mempool", func(t *testing.T) {
		client := &fakeLocalClient{}
		b, attrs, prelude := newLocalTestBuilder(client)
		b.txPool = newPool(&fakePoolEL{pending: []*types.Transaction{own, elTx}})

		out := b.buildLocal(context.Background(), attrs, prelude, req)
		require.NoError(t, out.err)
		require.Len(t, client.calls, 1)
		require.Equal(t, []string{own.Hash().Hex(), elTx.Hash().Hex()}, out.info.ExpectedHashes,
			"the pool transaction first, the EL's own copy of it skipped, the EL transaction after")
		require.Equal(t, 1, out.info.FillTxs)
		require.Equal(t, 1, out.info.Selection.FillSkipped[txpool.SkipDuplicate])
	})

	t.Run("refusal retries without the fill", func(t *testing.T) {
		client := &fakeLocalClient{failNext: 1, failErr: errors.New("invalid transaction")}
		b, attrs, prelude := newLocalTestBuilder(client)
		b.txPool = newPool(&fakePoolEL{pending: []*types.Transaction{elTx}})

		out := b.buildLocal(context.Background(), attrs, prelude, req)
		require.NoError(t, out.err)
		require.Len(t, client.calls, 2)
		require.Len(t, client.calls[0], 2)
		require.Len(t, client.calls[1], 1, "the retry carries the pool transaction only")
		require.True(t, out.info.FillDropped)
		require.Equal(t, []string{own.Hash().Hex()}, out.info.ExpectedHashes)
	})

	t.Run("unreadable EL mempool builds from the pool alone", func(t *testing.T) {
		client := &fakeLocalClient{}
		b, attrs, prelude := newLocalTestBuilder(client)
		b.txPool = newPool(&fakePoolEL{pendingErr: errors.New("method not found")})

		out := b.buildLocal(context.Background(), attrs, prelude, req)
		require.NoError(t, out.err)
		require.Contains(t, out.info.FillError, "method not found")
		require.Equal(t, []string{own.Hash().Hex()}, out.info.ExpectedHashes)
	})
}

// TestFireLocalProgress reports the custom build live: building at the start,
// then its own outcome without waiting for the engine build.
func TestFireLocalProgress(t *testing.T) {
	b, attrs, _ := newLocalTestBuilder(&fakeLocalClient{})
	sub := b.localProgress.Subscribe(8, false)

	defer sub.Unsubscribe()

	req := &LocalBuildRequest{Candidate: "parent_full", TxSource: config.TxSourceTxPool}
	started := time.Now()

	b.fireLocalProgress(attrs, req, started, nil)
	b.fireLocalProgress(attrs, req, started, &localOutcome{payload: &Payload{BlockValue: big.NewInt(7)}})
	b.fireLocalProgress(attrs, req, started, &localOutcome{skipReason: LocalSkipTxPoolDisabled})
	b.fireLocalProgress(attrs, req, started, &localOutcome{err: errors.New("boom")})

	phases := make([]*LocalBuildProgressEvent, 0, 4)
	for range 4 {
		phases = append(phases, <-sub.Channel())
	}

	require.Equal(t, LocalStatusBuilding, phases[0].Phase)
	require.Equal(t, "parent_full", phases[0].Candidate)
	require.Equal(t, attrs.ProposalSlot, phases[0].Slot)
	require.Equal(t, LocalStatusReady, phases[1].Phase)
	require.Equal(t, big.NewInt(7), phases[1].BlockValue)
	require.Equal(t, started, phases[1].StartedAt)
	require.Equal(t, LocalStatusSkipped, phases[2].Phase)
	require.Equal(t, LocalSkipTxPoolDisabled, phases[2].SkipReason)
	require.Equal(t, LocalStatusFailed, phases[3].Phase)
	require.Equal(t, "boom", phases[3].Error)
}

// TestLocalBuildInfoPublicDropsTransactionHashes checks that the public copy
// carries no transaction hash of the custom block (pool selection, expected
// list, EL refusals) while counts and reasons stay.
func TestLocalBuildInfoPublicDropsTransactionHashes(t *testing.T) {
	info := &LocalBuildInfo{
		TxSource:       "txpool",
		Selection:      &txpool.Summary{Selected: 1, Hashes: []string{"0x01"}},
		ExpectedHashes: []string{"0x01", "0x02"},
		Dropped:        []DroppedTx{{Hash: "0x03", Reason: "nonce"}},
		SubmittedTxs:   2,
	}

	public := info.Public()

	require.Nil(t, public.ExpectedHashes)
	require.Empty(t, public.Selection.Hashes)
	require.Equal(t, 1, public.Selection.Selected)
	require.Equal(t, []DroppedTx{{Reason: "nonce"}}, public.Dropped)
	require.Equal(t, 2, public.SubmittedTxs)

	// The original keeps everything (the logged-in view and the verifier use it).
	require.Equal(t, []string{"0x01"}, info.Selection.Hashes)
	require.Equal(t, "0x03", info.Dropped[0].Hash)
}

// TestLocalBuildInfoPrivateTxs counts the transactions buildoor supplied
// itself: the sent list minus the EL mempool fill and the inclusion list,
// unless those were dropped on a retry.
func TestLocalBuildInfoPrivateTxs(t *testing.T) {
	hashes := func(n int) []string { return make([]string, n) }

	tests := []struct {
		name string
		info *LocalBuildInfo
		want int
	}{
		{name: "engine payload", info: nil, want: 0},
		{name: "el mempool source", info: &LocalBuildInfo{TxSource: "el_mempool"}, want: 0},
		{name: "pool only", info: &LocalBuildInfo{ExpectedHashes: hashes(3)}, want: 3},
		{name: "pool and fill", info: &LocalBuildInfo{ExpectedHashes: hashes(30), FillTxs: 29}, want: 1},
		{name: "fill dropped", info: &LocalBuildInfo{ExpectedHashes: hashes(1), FillTxs: 29, FillDropped: true}, want: 1},
		{name: "inclusion list", info: &LocalBuildInfo{ExpectedHashes: hashes(5), InclusionListTxs: 2}, want: 3},
		{name: "inclusion list dropped", info: &LocalBuildInfo{ExpectedHashes: hashes(3), InclusionListTxs: 2, InclusionListDropped: true}, want: 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.info.PrivateTxs())
		})
	}
}

// TestWithoutFillBlobTxs drops the fill's blob transactions and the later
// fill transactions of their senders, keeping everyone else's.
func TestWithoutFillBlobTxs(t *testing.T) {
	blobSender, other := common.Address{0x01}, common.Address{0x02}

	entry := func(sender common.Address, nonce uint64, blob bool) *txpool.PooledTx {
		var inner types.TxData = &types.DynamicFeeTx{Nonce: nonce}
		if blob {
			inner = &types.BlobTx{Nonce: nonce}
		}

		tx := types.NewTx(inner)

		return &txpool.PooledTx{Hash: tx.Hash(), Tx: tx, Sender: sender}
	}

	before := entry(blobSender, 0, false)
	blob := entry(blobSender, 1, true)
	after := entry(blobSender, 2, false)
	kept := entry(other, 0, false)

	require.Equal(t, []*txpool.PooledTx{before, kept}, withoutFillBlobTxs([]*txpool.PooledTx{before, blob, kept, after}))
}
