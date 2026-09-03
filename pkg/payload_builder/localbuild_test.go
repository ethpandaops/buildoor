package payload_builder

import (
	"context"
	"errors"
	"math/big"
	"testing"

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

	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/beacon"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
)

func TestLocalBuildRequestRunEL(t *testing.T) {
	require.True(t, (*LocalBuildRequest)(nil).RunEL(), "no local request = engine build only")
	require.True(t, (&LocalBuildRequest{PayloadSource: config.PayloadSourceEL}).RunEL())
	require.True(t, (&LocalBuildRequest{PayloadSource: config.PayloadSourceLocalOrEL, BuildELPayload: false}).RunEL(),
		"local_or_el needs the engine payload as fallback")
	require.True(t, (&LocalBuildRequest{PayloadSource: config.PayloadSourceLocal, BuildELPayload: true}).RunEL())
	require.False(t, (&LocalBuildRequest{PayloadSource: config.PayloadSourceLocal, BuildELPayload: false}).RunEL())
}

func TestSelectPayload(t *testing.T) {
	el := &Payload{Source: SourceEL, BlockHash: phase0.Hash32{1}}
	local := &Payload{Source: SourceLocal, BlockHash: phase0.Hash32{2}}
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
// calls).
type fakeLocalClient struct {
	calls    [][][]byte
	failNext int
	dropLast bool
}

func (f *fakeLocalClient) BuildBlockV1(_ context.Context, v enginev.DataVersion, parent common.Hash,
	_ *engineall.PayloadAttributes, txs [][]byte, _ []byte,
) (*engineall.GetPayloadResponse, error) {
	f.calls = append(f.calls, txs)

	if f.failNext > 0 {
		f.failNext--

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

	b := NewPayloadBuilder(nil, nil, client, nil, &stubChain{}, common.Address{}, cfg, logrus.New(), nil)

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

	t.Run("silently filtering EL is detected", func(t *testing.T) {
		client := &fakeLocalClient{dropLast: true}
		b, attrs, prelude := newLocalTestBuilder(client)

		out := b.buildLocal(context.Background(), attrs, prelude, &LocalBuildRequest{
			TxSource:     config.TxSourceExplicit,
			Transactions: [][]byte{signedTx(t, 0), signedTx(t, 1), signedTx(t, 2)},
		})
		require.NoError(t, out.err)
		require.Equal(t, 1, out.info.DroppedByEL)
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
