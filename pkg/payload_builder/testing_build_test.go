package payload_builder

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/tx_intake"
)

func TestCompareTxLists(t *testing.T) {
	a, b, c := common.Hash{1}, common.Hash{2}, common.Hash{3}

	check := CompareTxLists([]common.Hash{a, b, c}, []common.Hash{a, b, c})
	require.True(t, check.Match)
	require.Equal(t, 3, check.ActualCount)
	require.Equal(t, -1, check.FirstMismatch)

	check = CompareTxLists([]common.Hash{a, b, c}, []common.Hash{a, c, b})
	require.False(t, check.Match, "order matters")
	require.Equal(t, 1, check.FirstMismatch)
	require.Contains(t, check.Detail, "position 1")

	check = CompareTxLists([]common.Hash{a, b, c}, []common.Hash{a, b})
	require.False(t, check.Match, "count matters")
	require.Equal(t, -1, check.FirstMismatch)
	require.Contains(t, check.Detail, "expected 3 transactions, block has 2")

	check = CompareTxLists(nil, nil)
	require.True(t, check.Match, "an empty plan matches an empty block")
}

func TestCalcGasLimitMirrorsGeth(t *testing.T) {
	// Toward a higher target: at most parent/1024 - 1 per block.
	require.Equal(t, uint64(30_000_000+29_295), calcGasLimit(30_000_000, 60_000_000))
	// Exact target within reach.
	require.Equal(t, uint64(30_010_000), calcGasLimit(30_000_000, 30_010_000))
	// Toward a lower target.
	require.Equal(t, uint64(30_000_000-29_295), calcGasLimit(30_000_000, 10_000_000))
	// Unchanged when equal.
	require.Equal(t, uint64(30_000_000), calcGasLimit(30_000_000, 30_000_000))
}

// fakeEL answers eth_chainId and fails every testing_buildBlockV1 call with a
// nonce error naming failAddr, counting the attempts.
type fakeEL struct {
	attempts int
	failAddr common.Address
}

func (f *fakeEL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}

	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)

	w.Header().Set("Content-Type", "application/json")

	if req.Method == "eth_chainId" {
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":"0x301824"}`, req.ID)
		return
	}

	f.attempts++

	fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"nonce too low: address %s, tx: 0 state: 1"}}`,
		req.ID, f.failAddr.Hex())
}

func newFakeBuilder(t *testing.T) (*TestingBuilder, *fakeEL) {
	t.Helper()

	el := &fakeEL{}
	srv := httptest.NewServer(el)

	t.Cleanup(srv.Close)

	tb, err := NewTestingBuilder(context.Background(), srv.URL, 100, logrus.New())
	require.NoError(t, err)

	t.Cleanup(tb.Close)

	return tb, el
}

func queueTx(t *testing.T, tb *TestingBuilder, key *ecdsa.PrivateKey, nonce uint64) *tx_intake.Entry {
	t.Helper()

	tx := types.MustSignNewTx(key, types.LatestSignerForChainID(tb.ChainID()), &types.DynamicFeeTx{
		ChainID:   tb.ChainID(),
		Nonce:     nonce,
		GasTipCap: big.NewInt(1e9),
		GasFeeCap: big.NewInt(100e9),
		Gas:       21000,
		To:        &common.Address{2},
		Value:     big.NewInt(1),
	})

	raw, err := tx.MarshalBinary()
	require.NoError(t, err)

	hash, err := tb.Queue().Add(raw)
	require.NoError(t, err)

	return tb.Queue().Lookup(hash)
}

// An explicit list must never be trimmed: one EL refusal fails the build so
// the operator's plan can never be silently reduced (and then "verified").
func TestAsGivenIsNeverTrimmed(t *testing.T) {
	tb, el := newFakeBuilder(t)

	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	el.failAddr = crypto.PubkeyToAddress(key.PublicKey)

	entries := []*tx_intake.Entry{queueTx(t, tb, key, 0), queueTx(t, tb, key, 1)}
	spec := &TestingBuildSpec{
		Fill:        tx_intake.FillSpec{GasPct: 100, Policy: tx_intake.PolicyAsGiven},
		MaxAttempts: 3,
		MaxStrikes:  3,
	}
	plan := &TxPlan{Policy: spec.Fill.Policy, Skipped: map[string]int{}}

	_, _, err = tb.buildWithRetries(context.Background(), common.Hash{}, nil, enginev.DataVersionAmsterdam, entries, spec, plan)
	require.ErrorContains(t, err, "not trimmed")
	require.Equal(t, 1, el.attempts, "an exact plan must not retry with a reduced list")
	require.Empty(t, plan.Dropped, "an exact plan must not drop transactions")
	require.Zero(t, entries[0].Strikes(), "an exact plan must not strike its transactions")
}

// A fill policy is best-effort: the offender is attributed away and the build
// retries within the attempt budget.
func TestFillPolicyRetriesAfterAttribution(t *testing.T) {
	tb, el := newFakeBuilder(t)

	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	el.failAddr = crypto.PubkeyToAddress(key.PublicKey)

	entries := []*tx_intake.Entry{queueTx(t, tb, key, 0), queueTx(t, tb, key, 1)}
	spec := &TestingBuildSpec{
		Fill:        tx_intake.FillSpec{GasPct: 100, Policy: tx_intake.PolicyFIFO},
		MaxAttempts: 3,
		MaxStrikes:  3,
	}
	plan := &TxPlan{Policy: spec.Fill.Policy, Skipped: map[string]int{}}

	_, _, err = tb.buildWithRetries(context.Background(), common.Hash{}, nil, enginev.DataVersionAmsterdam, entries, spec, plan)
	require.Error(t, err)
	require.Equal(t, 2, el.attempts, "the attributed sender is dropped and the build retries")
	require.Len(t, plan.Dropped, 2, "both transactions of the attributed sender are dropped")
	require.Equal(t, 1, entries[0].Strikes(), "a best-effort drop strikes the transaction")
}
