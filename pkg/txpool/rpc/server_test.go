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
	"strings"
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
	"github.com/ethpandaops/buildoor/pkg/memstore"
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
	t.Helper()

	srv, el, pool, _ := newServerCfg(t, nil, func(cfg *config.Config) {
		if authToken != "" {
			cfg.TxPool.Auth = config.TxPoolAuthAPIKey
			cfg.TxPool.AuthToken = authToken
		}
	})

	return srv, el, pool
}

// newServerCfg builds an ingress over a fresh pool; keys and login may be nil.
func newServerCfg(t *testing.T, login LoginChecker, mutate func(*config.Config)) (*Server, *fakeEL, *txpool.Pool, *KeyStore) {
	t.Helper()

	el := &fakeEL{nonce: 5}
	cfg := config.DefaultConfig()
	cfg.TxPool.Enabled = true
	cfg.TxPool.RPCAnonRate = 0

	if mutate != nil {
		mutate(cfg)
	}

	pool := txpool.NewPool(cfg, el, &stubChain{}, nil, logrus.New())
	require.NoError(t, pool.Start(context.Background()))

	t.Cleanup(func() { _ = pool.Stop() })

	keys := NewKeyStore(memstore.New[string, *APIKey]())
	access := NewAccess(cfg, keys, login)

	return NewServer(pool, el, access, "test", logrus.New()), el, pool, keys
}

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

// TestDisabledPoolRefusesPassthrough keeps the EL unreachable through the
// ingress while the pool is disabled, for single calls and batches alike.
func TestDisabledPoolRefusesPassthrough(t *testing.T) {
	srv, el, pool := newServer(t, "")
	pool.SetEnabled(false)

	_, body := call(t, srv, `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`)
	require.JSONEq(t, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"txpool disabled"}}`, body)

	_, body = call(t, srv, `[{"jsonrpc":"2.0","id":1,"method":"eth_call","params":[{},"latest"]},`+
		`{"jsonrpc":"2.0","id":2,"method":"eth_chainId","params":[]}]`)
	require.Equal(t, 2, strings.Count(body, "txpool disabled"))
	require.Empty(t, el.calls, "a disabled pool never reaches the EL")

	pool.SetEnabled(true)

	_, body = call(t, srv, `{"jsonrpc":"2.0","id":3,"method":"eth_blockNumber","params":[]}`)
	require.JSONEq(t, `{"jsonrpc":"2.0","id":3,"result":"0x10"}`, body)
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

	// The pool is private: an anonymous caller never reads it back.
	_, body = call(t, srv, fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"eth_getTransactionByHash","params":[%q]}`, tx.Hash().Hex()))
	require.NotContains(t, body, `"blockHash":null`)
}

// TestPoolVisibility scopes reads of the private pool: an API key sees the
// transactions it submitted, operators (static token, WebUI login) all of
// them, anonymous callers none.
func TestPoolVisibility(t *testing.T) {
	srv, _, _, keys := newServerCfg(t, fakeLogin{}, func(cfg *config.Config) {
		cfg.TxPool.AuthToken = "operator"
	})

	_, secretA, err := keys.Create("a", nil)
	require.NoError(t, err)

	_, secretB, err := keys.Create("b", nil)
	require.NoError(t, err)

	txA, rawA := signedTx(t, 5)
	txB, rawB := signedTx(t, 5)

	_, body := callPath(t, srv, "/rpc/"+secretA, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"eth_sendRawTransaction","params":[%q]}`, rawA))
	require.Contains(t, body, txA.Hash().Hex())

	_, body = callPath(t, srv, "/rpc/"+secretB, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"eth_sendRawTransaction","params":[%q]}`, rawB))
	require.Contains(t, body, txB.Hash().Hex())

	content := `{"jsonrpc":"2.0","id":2,"method":"txpool_content","params":[]}`
	byHash := func(hash common.Hash) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"eth_getTransactionByHash","params":[%q]}`, hash.Hex())
	}

	_, body = callPath(t, srv, "/rpc/"+secretA, content)
	require.Contains(t, body, txA.Hash().Hex())
	require.NotContains(t, body, txB.Hash().Hex(), "a key never sees another key's transactions")

	_, body = callPath(t, srv, "/rpc/"+secretA, byHash(txB.Hash()))
	require.NotContains(t, body, `"blockHash":null`)

	_, body = callPath(t, srv, "/rpc/"+secretA, byHash(txA.Hash()))
	require.Contains(t, body, `"blockHash":null`)

	for _, headers := range [][]string{{"X-API-Key", "operator"}, {"Authorization", "Bearer jwt"}} {
		_, body = callPath(t, srv, "/rpc", content, headers...)
		require.Contains(t, body, txA.Hash().Hex(), headers[0])
		require.Contains(t, body, txB.Hash().Hex(), headers[0])
	}

	_, body = callPath(t, srv, "/rpc", content)
	require.NotContains(t, body, txA.Hash().Hex(), "anonymous callers see nothing")
	require.NotContains(t, body, txB.Hash().Hex())
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
	chainID := `{"jsonrpc":"2.0","id":1,"method":"eth_chainId"}`

	// api_key mode with the static token
	srv, _, _ := newServer(t, "secret")

	code, body := call(t, srv, chainID)
	require.Equal(t, http.StatusUnauthorized, code)
	require.Contains(t, body, ErrKeyRequired.Error())

	code, _ = call(t, srv, chainID, "Authorization", "Bearer wrong")
	require.Equal(t, http.StatusUnauthorized, code)

	code, body = call(t, srv, chainID, "Authorization", "Bearer secret")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, "0x1267")

	code, _ = call(t, srv, chainID, "X-API-Key", "secret")
	require.Equal(t, http.StatusOK, code)

	// open mode admits anonymous callers
	srv, _, _ = newServer(t, "")
	code, _ = call(t, srv, chainID)
	require.Equal(t, http.StatusOK, code)
}

// callPath posts a request to an explicit ingress path (the /rpc/<key> form).
func callPath(t *testing.T, srv http.Handler, path, body string, headers ...string) (int, string) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.RemoteAddr = "192.0.2.1:1234"

	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	return rec.Code, rec.Body.String()
}

// fakeLogin accepts "Bearer jwt" as a WebUI login.
type fakeLogin struct{}

func (fakeLogin) ValidLogin(r *http.Request) bool {
	return r.Header.Get("Authorization") == "Bearer jwt"
}

func TestAPIKeys(t *testing.T) {
	srv, _, _, keys := newServerCfg(t, fakeLogin{}, func(cfg *config.Config) {
		cfg.TxPool.Auth = config.TxPoolAuthAPIKey
	})

	chainID := `{"jsonrpc":"2.0","id":1,"method":"eth_chainId"}`

	key, secret, err := keys.Create("spamoor", nil)
	require.NoError(t, err)
	require.NotContains(t, key.Hash, secret, "only the hash is stored")

	for _, tc := range []struct {
		name    string
		path    string
		headers []string
	}{
		{name: "path", path: "/rpc/" + secret},
		{name: "x-api-key", path: "/rpc", headers: []string{"X-API-Key", secret}},
		{name: "bearer", path: "/rpc", headers: []string{"Authorization", "Bearer " + secret}},
	} {
		code, body := callPath(t, srv, tc.path, chainID, tc.headers...)
		require.Equal(t, http.StatusOK, code, tc.name)
		require.Contains(t, body, "0x1267", tc.name)
	}

	code, _ := callPath(t, srv, "/rpc/bdr_unknown", chainID)
	require.Equal(t, http.StatusUnauthorized, code)

	// A WebUI login is authenticated too.
	code, _ = callPath(t, srv, "/rpc", chainID, "Authorization", "Bearer jwt")
	require.Equal(t, http.StatusOK, code)

	disabled := false
	_, err = keys.Update(key.ID, KeyUpdate{Enabled: &disabled})
	require.NoError(t, err)

	code, body := callPath(t, srv, "/rpc/"+secret, chainID)
	require.Equal(t, http.StatusUnauthorized, code)
	require.Contains(t, body, ErrKeyDisabled.Error())

	require.NoError(t, keys.Delete(key.ID))
	code, _ = callPath(t, srv, "/rpc/"+secret, chainID)
	require.Equal(t, http.StatusUnauthorized, code)

	stats := srv.Access().Stats()
	kinds := make(map[string]uint64, len(stats))

	for _, caller := range stats {
		kinds[caller.Kind] += caller.Calls
	}

	require.Equal(t, uint64(3), kinds[CallerAPIKey])
	require.Equal(t, uint64(1), kinds[CallerLogin])
}

func TestRateLimits(t *testing.T) {
	chainID := `{"jsonrpc":"2.0","id":1,"method":"eth_chainId"}`

	t.Run("anonymous per client IP behind one proxy", func(t *testing.T) {
		srv, _, _, _ := newServerCfg(t, nil, func(cfg *config.Config) {
			cfg.TxPool.RPCAnonRate = 1
			cfg.TxPool.RPCProxyHops = 1
		})

		_, body := callPath(t, srv, "/rpc", chainID, "X-Forwarded-For", "spoofed, 198.51.100.7")
		require.Contains(t, body, "0x1267")

		_, body = callPath(t, srv, "/rpc", chainID, "X-Forwarded-For", "198.51.100.7")
		require.Contains(t, body, `"code":-32005`, "the same client is over its limit")

		_, body = callPath(t, srv, "/rpc", chainID, "X-Forwarded-For", "198.51.100.8")
		require.Contains(t, body, "0x1267", "another client has its own budget")

		stats := srv.Access().Stats()
		require.Len(t, stats, 2)

		for _, caller := range stats {
			if caller.ID == "198.51.100.7" {
				require.Equal(t, uint64(2), caller.Calls)
				require.Equal(t, uint64(1), caller.RateLimited)
				require.Equal(t, uint64(2), caller.Methods["eth_chainId"])
			}
		}
	})

	t.Run("anonymous shared bucket", func(t *testing.T) {
		srv, _, _, _ := newServerCfg(t, nil, func(cfg *config.Config) {
			cfg.TxPool.RPCAnonRate = 1
			cfg.TxPool.RPCAnonScope = config.RPCAnonScopeShared
		})

		_, body := callPath(t, srv, "/rpc", chainID)
		require.Contains(t, body, "0x1267")

		req := httptest.NewRequest(http.MethodPost, "/rpc", bytes.NewBufferString(chainID))
		req.RemoteAddr = "203.0.113.9:1"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		require.Contains(t, rec.Body.String(), `"code":-32005`, "every anonymous caller shares the bucket")
	})

	t.Run("per key limit overrides the default", func(t *testing.T) {
		srv, _, _, keys := newServerCfg(t, nil, func(cfg *config.Config) {
			cfg.TxPool.RPCKeyRate = 1
		})

		_, limited, err := keys.Create("limited", nil)
		require.NoError(t, err)

		unlimitedRate := uint64(0)
		_, unlimited, err := keys.Create("unlimited", &unlimitedRate)
		require.NoError(t, err)

		batch := `[` + chainID + `,` + chainID + `]`

		_, body := callPath(t, srv, "/rpc/"+limited, batch)
		require.Equal(t, 1, strings.Count(body, `"code":-32005`), "each batched call counts")

		_, body = callPath(t, srv, "/rpc/"+unlimited, batch)
		require.NotContains(t, body, `"code":-32005`)
	})
}

func TestClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/rpc", nil)
	req.RemoteAddr = "10.0.0.1:5555"
	req.Header.Add("X-Forwarded-For", "1.1.1.1, 2.2.2.2")
	req.Header.Add("X-Forwarded-For", "3.3.3.3")

	require.Equal(t, "10.0.0.1", ClientIP(req, 0), "no trusted proxy: the connection")
	require.Equal(t, "3.3.3.3", ClientIP(req, 1))
	require.Equal(t, "2.2.2.2", ClientIP(req, 2))
	require.Equal(t, "10.0.0.1", ClientIP(req, 4), "a shorter chain than the trusted hops is not trusted")
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

// TestPassthroughAllowlist pins the forwarded read surface: the standard
// public reads pass, every privileged or node-local method is refused —
// including ones the allowlist never heard of.
func TestPassthroughAllowlist(t *testing.T) {
	allowed := []string{
		"eth_blockNumber", "eth_call", "eth_estimateGas", "eth_getBalance", "eth_getLogs",
		"eth_getBlockByNumber", "eth_getTransactionReceipt", "eth_feeHistory", "net_listening",
	}
	for _, method := range allowed {
		require.True(t, passthroughAllowed(method), method)
	}

	refused := []string{
		// privileged namespaces
		"admin_peers", "debug_traceTransaction", "trace_block", "testing_buildBlockV1",
		"engine_newPayloadV4", "personal_listAccounts", "miner_setEtherbase", "txpool_inspect",
		// signing / sending / bundles
		"eth_sendTransaction", "eth_sign", "eth_signTypedData_v4", "eth_sendBundle", "eth_callBundle",
		// node-local answers
		"eth_accounts", "eth_coinbase", "eth_pendingTransactions", "eth_fillTransaction", "net_peerCount",
		// stateful
		"eth_newFilter", "eth_getFilterChanges", "eth_subscribe",
		// unknown
		"eth_somethingNew", "",
	}
	for _, method := range refused {
		require.False(t, passthroughAllowed(method), method)
	}
}
