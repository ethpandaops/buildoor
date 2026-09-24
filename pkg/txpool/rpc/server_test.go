package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
	"github.com/ethpandaops/buildoor/pkg/txpool"
)

var chainID = big.NewInt(4711)

type stubChain struct {
	chain.Service
}

func (s *stubChain) GetCurrentSlot() phase0.Slot        { return 7 }
func (s *stubChain) GetHeadTracker() *chain.HeadTracker { return nil }

// fakeEL satisfies both the pool's ELClient and the ingress' ELProxy.
type fakeEL struct {
	calls []string
	nonce uint64
}

func (f *fakeEL) GetChainID(context.Context) (*big.Int, error) { return chainID, nil }
func (f *fakeEL) HeaderByHash(context.Context, common.Hash) (*types.Header, error) {
	return nil, errors.New("not used")
}

func (f *fakeEL) AccountStatesAt(context.Context, []common.Address, common.Hash, uint64,
) (map[common.Address]*execution.AccountState, error) {
	return nil, errors.New("not used")
}
func (f *fakeEL) BlobBaseFee(context.Context) (*big.Int, error) { return big.NewInt(1), nil }
func (f *fakeEL) BlockTransactions(context.Context, common.Hash) (*execution.BlockTxList, bool, error) {
	return nil, false, nil
}

func (f *fakeEL) TransactionBlockHashes(context.Context, []common.Hash) (map[common.Hash]common.Hash, error) {
	return map[common.Hash]common.Hash{}, nil
}

func (f *fakeEL) SendRawTransaction(context.Context, []byte) (common.Hash, error) {
	return common.Hash{}, nil
}

func (f *fakeEL) RawCall(_ context.Context, method string, params []any) ([]byte, error) {
	f.calls = append(f.calls, method)

	switch method {
	case "eth_getTransactionCount":
		return json.Marshal(hexutil.Uint64(f.nonce))
	case "eth_blockNumber":
		return []byte(`"0x10"`), nil
	case "eth_getBalance":
		return []byte(`"0x1234"`), nil
	default:
		return nil, fmt.Errorf("unexpected method %s (params %d)", method, len(params))
	}
}

func newServer(t *testing.T, authToken string) (*Server, *fakeEL, *txpool.Pool) {
	var auth Authorizer
	if authToken != "" {
		auth = StaticAuthorizer{Token: authToken}
	}

	return newServerWith(t, auth)
}

func newServerWith(t *testing.T, auth Authorizer) (*Server, *fakeEL, *txpool.Pool) {
	t.Helper()

	el := &fakeEL{nonce: 5}
	cfg := config.DefaultConfig()
	cfg.TxPool.Enabled = true

	pool := txpool.NewPool(cfg, el, &stubChain{}, nil, logrus.New())
	require.NoError(t, pool.Start(context.Background()))

	t.Cleanup(func() { _ = pool.Stop() })

	return NewServer(pool, el, auth, "test", logrus.New()), el, pool
}

// denyAuthorizer stands in for an auth-provider check that rejects.
type denyAuthorizer struct{}

func (denyAuthorizer) Authorize(*http.Request) bool { return false }

func call(t *testing.T, srv http.Handler, body string, headers ...string) (int, string) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/rpc", bytes.NewBufferString(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	return rec.Code, rec.Body.String()
}

func signedTx(t *testing.T, nonce uint64) (*types.Transaction, string) {
	t.Helper()

	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	to := common.Address{0x1}
	tx, err := types.SignNewTx(key, types.LatestSignerForChainID(chainID), &types.DynamicFeeTx{
		ChainID: chainID, Nonce: nonce, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2), Gas: 21000, To: &to,
	})
	require.NoError(t, err)

	raw, err := tx.MarshalBinary()
	require.NoError(t, err)

	return tx, hexutil.Encode(raw)
}

func TestSendRawTransactionAndDuplicates(t *testing.T) {
	srv, _, pool := newServer(t, "")
	tx, raw := signedTx(t, 0)

	code, body := call(t, srv, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"eth_sendRawTransaction","params":[%q]}`, raw))
	require.Equal(t, http.StatusOK, code)
	require.JSONEq(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":%q}`, tx.Hash().Hex()), body)
	require.NotNil(t, pool.Get(tx.Hash()))

	code, body = call(t, srv, fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"eth_sendRawTransaction","params":[%q]}`, raw))
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, `"already known"`, "spamoor matches this substring to count the submission as success")
	require.Contains(t, body, `"code":-32000`)
}

func TestDisabledPoolAnswersDisabled(t *testing.T) {
	srv, _, pool := newServer(t, "")
	pool.SetEnabled(false)

	_, raw := signedTx(t, 0)
	_, body := call(t, srv, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"eth_sendRawTransaction","params":[%q]}`, raw))
	require.Contains(t, body, "txpool disabled")
}

func TestLocalMethods(t *testing.T) {
	srv, _, _ := newServer(t, "")

	_, body := call(t, srv, `{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}`)
	require.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":"0x1267"}`, body)

	_, body = call(t, srv, `{"jsonrpc":"2.0","id":"a","method":"net_version"}`)
	require.JSONEq(t, `{"jsonrpc":"2.0","id":"a","result":"4711"}`, body)

	_, body = call(t, srv, `{"jsonrpc":"2.0","id":3,"method":"web3_clientVersion","params":[]}`)
	require.JSONEq(t, `{"jsonrpc":"2.0","id":3,"result":"buildoor/test"}`, body)

	_, body = call(t, srv, `{"jsonrpc":"2.0","id":4,"method":"txpool_status","params":[]}`)
	require.JSONEq(t, `{"jsonrpc":"2.0","id":4,"result":{"pending":"0x0","queued":"0x0"}}`, body)
}

func TestPendingNonceIsPoolAware(t *testing.T) {
	srv, el, _ := newServer(t, "")
	tx, raw := signedTx(t, 5) // chain nonce (fake) is 5

	_, body := call(t, srv, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"eth_sendRawTransaction","params":[%q]}`, raw))
	require.Contains(t, body, tx.Hash().Hex())

	sender, err := types.Sender(types.LatestSignerForChainID(chainID), tx)
	require.NoError(t, err)

	_, body = call(t, srv, fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"eth_getTransactionCount","params":[%q,"pending"]}`, sender.Hex()))
	require.JSONEq(t, `{"jsonrpc":"2.0","id":2,"result":"0x6"}`, body, "latest nonce 5 + one queued tx")

	// Latest is proxied untouched.
	_, body = call(t, srv, fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"eth_getTransactionCount","params":[%q,"latest"]}`, sender.Hex()))
	require.JSONEq(t, `{"jsonrpc":"2.0","id":3,"result":"0x5"}`, body)
	require.Contains(t, el.calls, "eth_getTransactionCount")

	// Queued transactions are visible by hash.
	_, body = call(t, srv, fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"eth_getTransactionByHash","params":[%q]}`, tx.Hash().Hex()))
	require.Contains(t, body, `"blockHash":null`)
	require.Contains(t, body, tx.Hash().Hex())
}

func TestPassthroughAndDenylist(t *testing.T) {
	srv, el, _ := newServer(t, "")

	_, body := call(t, srv, `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`)
	require.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":"0x10"}`, body)
	require.Equal(t, []string{"eth_blockNumber"}, el.calls)

	_, body = call(t, srv, `{"jsonrpc":"2.0","id":2,"method":"eth_sendTransaction","params":[{}]}`)
	require.Contains(t, body, `"code":-32601`)

	_, body = call(t, srv, `{"jsonrpc":"2.0","id":3,"method":"debug_traceBlock","params":[]}`)
	require.Contains(t, body, `"code":-32601`)

	_, body = call(t, srv, `{"jsonrpc":"2.0","id":4,"method":"testing_buildBlockV1","params":[]}`)
	require.Contains(t, body, `"code":-32601`)
	require.Len(t, el.calls, 1, "refused methods never reach the EL")
}

func TestBatch(t *testing.T) {
	srv, _, _ := newServer(t, "")

	code, body := call(t, srv, `[{"jsonrpc":"2.0","id":1,"method":"eth_chainId"},{"jsonrpc":"2.0","id":2,"method":"eth_getBalance","params":["0x0000000000000000000000000000000000000001","latest"]},{"jsonrpc":"2.0","id":3,"method":"nope_x"}]`)
	require.Equal(t, http.StatusOK, code)

	var responses []map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &responses))
	require.Len(t, responses, 3)
	require.Equal(t, "0x1267", responses[0]["result"])
	require.Equal(t, "0x1234", responses[1]["result"])
	require.NotNil(t, responses[2]["error"])
}

func TestAuthModes(t *testing.T) {
	// static
	srv, _, _ := newServer(t, "secret")

	code, _ := call(t, srv, `{"jsonrpc":"2.0","id":1,"method":"eth_chainId"}`)
	require.Equal(t, http.StatusUnauthorized, code)

	code, _ = call(t, srv, `{"jsonrpc":"2.0","id":1,"method":"eth_chainId"}`, "Authorization", "Bearer wrong")
	require.Equal(t, http.StatusUnauthorized, code)

	code, body := call(t, srv, `{"jsonrpc":"2.0","id":1,"method":"eth_chainId"}`, "Authorization", "Bearer secret")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, "0x1267")

	// open
	srv, _, _ = newServerWith(t, nil)
	code, _ = call(t, srv, `{"jsonrpc":"2.0","id":1,"method":"eth_chainId"}`)
	require.Equal(t, http.StatusOK, code)

	// a rejecting authorizer (auth-provider mode without a valid JWT)
	srv, _, _ = newServerWith(t, denyAuthorizer{})
	code, _ = call(t, srv, `{"jsonrpc":"2.0","id":1,"method":"eth_chainId"}`, "Authorization", "Bearer secret")
	require.Equal(t, http.StatusUnauthorized, code)

	// an empty static token never matches
	srv, _, _ = newServerWith(t, StaticAuthorizer{})
	code, _ = call(t, srv, `{"jsonrpc":"2.0","id":1,"method":"eth_chainId"}`, "Authorization", "Bearer ")
	require.Equal(t, http.StatusUnauthorized, code)
}

func TestMalformedRequests(t *testing.T) {
	srv, _, _ := newServer(t, "")

	_, body := call(t, srv, `{not json`)
	require.Contains(t, body, `"code":-32700`)

	_, body = call(t, srv, `{"jsonrpc":"2.0","id":1,"params":[]}`)
	require.Contains(t, body, `"code":-32600`)

	_, body = call(t, srv, `{"jsonrpc":"2.0","id":1,"method":"eth_sendRawTransaction","params":["0xzz"]}`)
	require.Contains(t, body, `"code":-32602`)

	req := httptest.NewRequest(http.MethodGet, "/rpc", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
