package tx_intake

import (
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

var testChainID = big.NewInt(3151908)

type testSender struct {
	key  *ecdsa.PrivateKey
	addr common.Address
}

func newSender(t *testing.T) testSender {
	t.Helper()

	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	return testSender{key: key, addr: crypto.PubkeyToAddress(key.PublicKey)}
}

func (s testSender) tx(t *testing.T, nonce, gas uint64, feeCapGwei int64) []byte {
	t.Helper()

	tx := types.MustSignNewTx(s.key, types.LatestSignerForChainID(testChainID), &types.DynamicFeeTx{
		ChainID:   testChainID,
		Nonce:     nonce,
		GasTipCap: big.NewInt(1e9),
		GasFeeCap: big.NewInt(feeCapGwei * 1e9),
		Gas:       gas,
		To:        &common.Address{1},
		Value:     big.NewInt(1),
	})

	raw, err := tx.MarshalBinary()
	require.NoError(t, err)

	return raw
}

func hashOf(t *testing.T, raw []byte) common.Hash {
	t.Helper()

	tx := new(types.Transaction)
	require.NoError(t, tx.UnmarshalBinary(raw))

	return tx.Hash()
}

func blockCtx(gasLimit uint64) *BlockContext {
	return &BlockContext{
		GasLimit:    gasLimit,
		BaseFee:     big.NewInt(7),
		BlobBaseFee: big.NewInt(1),
		MaxBlobs:    6,
	}
}

func richState(nonce uint64) SenderState {
	bal, _ := new(big.Int).SetString("1000000000000000000000", 10)
	return SenderState{Nonce: nonce, Balance: bal}
}

func TestQueueAddReplaceAndPendingNonce(t *testing.T) {
	q := NewQueue(testChainID, 10)
	s := newSender(t)

	raw0 := s.tx(t, 0, 21000, 10)
	h0, err := q.Add(raw0)
	require.NoError(t, err)

	again, err := q.Add(raw0)
	require.NoError(t, err)
	require.Equal(t, h0, again, "same hash is idempotent")
	require.Equal(t, 1, q.Stats().Txs)

	_, err = q.Add(s.tx(t, 1, 21000, 10))
	require.NoError(t, err)
	require.Equal(t, uint64(2), q.PendingNonce(s.addr, 0))
	require.Equal(t, uint64(2), q.PendingNonce(s.addr, 1))
	require.Equal(t, uint64(5), q.PendingNonce(s.addr, 5), "chain nonce past the queue wins")

	// A different tx for the same nonce replaces the older one.
	replacement := s.tx(t, 1, 30000, 20)
	hr, err := q.Add(replacement)
	require.NoError(t, err)
	require.Equal(t, 2, q.Stats().Txs)
	require.NotNil(t, q.Lookup(hr))
	require.Equal(t, uint64(1), q.Stats().Evictions[EvictReplaced])

	// Stale nonces are evicted by the state-nonce rule.
	evicted := q.EvictBelow(s.addr, 1)
	require.Len(t, evicted, 1)
	require.Equal(t, EvictNonceTooLow, evicted[0].Reason)
	require.Nil(t, q.Lookup(h0))
}

func TestQueueRejectsWrongChainAndFull(t *testing.T) {
	q := NewQueue(testChainID, 1)
	s := newSender(t)

	_, err := q.Add(s.tx(t, 0, 21000, 10))
	require.NoError(t, err)

	_, err = q.Add(s.tx(t, 1, 21000, 10))
	require.ErrorIs(t, err, ErrQueueFull)

	other := types.MustSignNewTx(s.key, types.LatestSignerForChainID(big.NewInt(1)), &types.DynamicFeeTx{
		ChainID: big.NewInt(1), Nonce: 0, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(1), Gas: 21000, To: &common.Address{1},
	})
	raw, _ := other.MarshalBinary()
	_, err = NewQueue(testChainID, 10).Add(raw)
	require.ErrorContains(t, err, "wrong chain id")
}

func TestPackFIFORespectsNonceOrderAndGasCap(t *testing.T) {
	q := NewQueue(testChainID, 100)
	a, b := newSender(t), newSender(t)

	// b submits first, then a; each has a two-tx chain. Gas cap fits three.
	rawB0 := b.tx(t, 0, 100_000, 10)
	rawA0 := a.tx(t, 0, 100_000, 10)
	rawB1 := b.tx(t, 1, 100_000, 10)
	rawA1 := a.tx(t, 1, 100_000, 10)

	for _, raw := range [][]byte{rawB0, rawA0, rawB1, rawA1} {
		_, err := q.Add(raw)
		require.NoError(t, err)
		time.Sleep(time.Millisecond)
	}

	states := map[common.Address]SenderState{a.addr: richState(0), b.addr: richState(0)}
	plan, err := Pack(q, states, blockCtx(300_000), FillSpec{GasPct: 100, Policy: PolicyFIFO})
	require.NoError(t, err)

	require.Equal(t, []common.Hash{hashOf(t, rawB0), hashOf(t, rawA0), hashOf(t, rawB1)}, plan.Hashes())
	require.Equal(t, uint64(300_000), plan.GasSum)
	require.Equal(t, 1, plan.Skipped[SkipGasCap])
}

func TestPackStateNonceRuleAndGaps(t *testing.T) {
	q := NewQueue(testChainID, 100)
	s := newSender(t)

	stale := s.tx(t, 0, 21000, 10)
	next := s.tx(t, 1, 21000, 10)
	gap := s.tx(t, 3, 21000, 10)

	for _, raw := range [][]byte{stale, next, gap} {
		_, err := q.Add(raw)
		require.NoError(t, err)
	}

	plan, err := Pack(q, map[common.Address]SenderState{s.addr: richState(1)}, blockCtx(1_000_000), FillSpec{GasPct: 100, Policy: PolicyFIFO})
	require.NoError(t, err)

	require.Equal(t, []common.Hash{hashOf(t, next)}, plan.Hashes())
	require.Len(t, plan.Evicted, 1, "nonce 0 is below the state nonce")
	require.Equal(t, 1, plan.Skipped[SkipNonceGap])
	require.Nil(t, q.Lookup(hashOf(t, stale)))
	require.NotNil(t, q.Lookup(hashOf(t, gap)), "gapped tx stays queued")
}

func TestPackFeeAndBalanceFilters(t *testing.T) {
	q := NewQueue(testChainID, 100)
	cheap, poor := newSender(t), newSender(t)

	_, err := q.Add(cheap.tx(t, 0, 21000, 10))
	require.NoError(t, err)
	_, err = q.Add(poor.tx(t, 0, 21000, 10))
	require.NoError(t, err)

	bctx := blockCtx(1_000_000)
	bctx.BaseFee = big.NewInt(50e9) // above cheap's 10 gwei cap

	states := map[common.Address]SenderState{
		cheap.addr: richState(0),
		poor.addr:  {Nonce: 0, Balance: big.NewInt(1)},
	}

	plan, err := Pack(q, states, bctx, FillSpec{GasPct: 100, Policy: PolicyFIFO})
	require.NoError(t, err)
	require.Empty(t, plan.Entries)
	require.Equal(t, 2, plan.Skipped[SkipFeeTooLow])

	bctx.BaseFee = big.NewInt(7)
	plan, err = Pack(q, states, bctx, FillSpec{GasPct: 100, Policy: PolicyFIFO})
	require.NoError(t, err)
	require.Len(t, plan.Entries, 1)
	require.Equal(t, 1, plan.Skipped[SkipInsufficientFunds])
}

func TestPackAsGivenIsExactOrFails(t *testing.T) {
	q := NewQueue(testChainID, 100)
	a, b := newSender(t), newSender(t)

	rawA0 := a.tx(t, 0, 21000, 10)
	rawA1 := a.tx(t, 1, 21000, 10)
	rawB0 := b.tx(t, 0, 21000, 10)

	for _, raw := range [][]byte{rawA0, rawA1, rawB0} {
		_, err := q.Add(raw)
		require.NoError(t, err)
	}

	states := map[common.Address]SenderState{a.addr: richState(0), b.addr: richState(0)}
	want := []common.Hash{hashOf(t, rawB0), hashOf(t, rawA0), hashOf(t, rawA1)}

	plan, err := Pack(q, states, blockCtx(1_000_000), FillSpec{GasPct: 100, Policy: PolicyAsGiven, Txs: want})
	require.NoError(t, err)
	require.Equal(t, want, plan.Hashes())

	// Out of nonce order for a sender: refused, never reordered.
	_, err = Pack(q, states, blockCtx(1_000_000), FillSpec{GasPct: 100, Policy: PolicyAsGiven, Txs: []common.Hash{hashOf(t, rawA1), hashOf(t, rawA0)}})
	require.ErrorContains(t, err, "has nonce 1")

	// Unknown hash: refused.
	_, err = Pack(q, states, blockCtx(1_000_000), FillSpec{GasPct: 100, Policy: PolicyAsGiven, Txs: []common.Hash{{9}}})
	require.ErrorContains(t, err, "is not queued")

	// Does not fit: refused, never trimmed.
	_, err = Pack(q, states, blockCtx(30_000), FillSpec{GasPct: 100, Policy: PolicyAsGiven, Txs: want})
	require.ErrorContains(t, err, "does not fit: gas_cap")
}

func TestAttribute(t *testing.T) {
	a, b := newSender(t), newSender(t)
	q := NewQueue(testChainID, 10)

	var plan []*Entry
	for _, raw := range [][]byte{a.tx(t, 0, 21000, 10), b.tx(t, 0, 21000, 10), a.tx(t, 1, 21000, 10)} {
		h, err := q.Add(raw)
		require.NoError(t, err)
		plan = append(plan, q.Lookup(h))
	}

	attr := Attribute(errors.New("nonce too low: address "+a.addr.Hex()+", tx: 0 state: 1"), plan)
	require.Equal(t, "nonce_too_low", attr.Reason)
	kept, dropped := attr.Apply(plan)
	require.Len(t, dropped, 2, "every tx of the sender goes")
	require.Len(t, kept, 1)
	require.Equal(t, b.addr, kept[0].Sender)

	attr = Attribute(errors.New("invalid transaction 1: rlp: oops"), plan)
	require.Equal(t, 1, attr.Index)
	kept, dropped = attr.Apply(plan)
	require.Len(t, dropped, 1)
	require.Len(t, kept, 2)

	attr = Attribute(errors.New("gas limit reached"), plan)
	require.Equal(t, 2, attr.Index)

	attr = Attribute(errors.New("something new"), plan)
	require.True(t, attr.Bisect)
	kept, dropped = attr.Apply(plan)
	require.Len(t, kept, 2)
	require.Len(t, dropped, 1)
}

func TestProxyInterceptsAndForwards(t *testing.T) {
	s := newSender(t)
	q := NewQueue(testChainID, 10)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqs []rpcRequest

		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)

		single := body[0] != '['
		if single {
			var one rpcRequest
			require.NoError(t, json.Unmarshal(body, &one))
			reqs = []rpcRequest{one}
		} else {
			require.NoError(t, json.Unmarshal(body, &reqs))
		}

		var out []json.RawMessage
		for _, req := range reqs {
			switch req.Method {
			case "eth_getTransactionCount":
				out = append(out, resultResponse(req.ID, "0x5"))
			case "eth_chainId":
				out = append(out, resultResponse(req.ID, "0x1"))
			default:
				out = append(out, errorResponse(req.ID, -32601, "method not found"))
			}
		}

		if single {
			_, _ = w.Write(out[0])
			return
		}

		_ = json.NewEncoder(w).Encode(out)
	}))
	defer upstream.Close()

	proxy := NewProxy(q, upstream.URL, logrus.New())
	srv := httptest.NewServer(proxy)
	defer srv.Close()

	call := func(body string) string {
		resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		var buf strings.Builder
		b := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(b)
			buf.Write(b[:n])
			if err != nil {
				break
			}
		}

		return buf.String()
	}

	raw := s.tx(t, 5, 21000, 10)
	rawHex := "0x" + common.Bytes2Hex(raw)
	out := call(`{"jsonrpc":"2.0","id":1,"method":"eth_sendRawTransaction","params":["` + rawHex + `"]}`)
	require.Contains(t, out, hashOf(t, raw).Hex())
	require.Equal(t, 1, q.Stats().Txs)

	// pending nonce = upstream latest (5) advanced over the queued chain.
	out = call(`{"jsonrpc":"2.0","id":2,"method":"eth_getTransactionCount","params":["` + s.addr.Hex() + `","pending"]}`)
	require.Contains(t, out, `"0x6"`)

	// latest is forwarded verbatim; batch keeps order and mixes local/forwarded.
	out = call(`[{"jsonrpc":"2.0","id":"a","method":"eth_chainId","params":[]},` +
		`{"jsonrpc":"2.0","id":"b","method":"eth_getTransactionCount","params":["` + s.addr.Hex() + `","pending"]},` +
		`{"jsonrpc":"2.0","id":"c","method":"eth_getTransactionCount","params":["` + s.addr.Hex() + `","latest"]}]`)

	var batch []rpcResponse
	require.NoError(t, json.Unmarshal([]byte(out), &batch))
	require.Len(t, batch, 3)
	require.Equal(t, `"a"`, string(batch[0].ID))
	require.Equal(t, `"0x1"`, string(batch[0].Result))
	require.Equal(t, `"0x6"`, string(batch[1].Result))
	require.Equal(t, `"0x5"`, string(batch[2].Result))
}
