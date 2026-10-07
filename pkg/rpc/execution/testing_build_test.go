package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	engineall "github.com/ethpandaops/go-eth-engine-client/spec/all"
	"github.com/ethpandaops/go-eth-engine-client/spec/paris"
	enginev "github.com/ethpandaops/go-eth-engine-client/spec/version"
	"github.com/stretchr/testify/require"
)

// testingPayloadJSON is a minimal Cancun+-shaped testing_buildBlockV1 result.
var testingPayloadJSON = `{
  "executionPayload": {
    "parentHash": "0xe27a3e81bd7cfe2aec2cc9e832c73a17c93e7efcf659cf4b39883b96c48708c2",
    "feeRecipient": "0x0000000000000000000000000000000000000000",
    "stateRoot": "0xca3149fa9e37db08d1cd49c9061db1002ef1cd58db2210f2115c8c989b2bdf45",
    "receiptsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
    "logsBloom": "0x` + logsBloomZero + `",
    "prevRandao": "0x0000000000000000000000000000000000000000000000000000000000000000",
    "blockNumber": "0x1",
    "gasLimit": "0x1c9c380",
    "gasUsed": "0x0",
    "timestamp": "0x1ce",
    "extraData": "0x",
    "baseFeePerGas": "0x7",
    "blockHash": "0x1234567890123456789012345678901234567890123456789012345678901234",
    "transactions": ["0x02f8"],
    "withdrawals": [],
    "blobGasUsed": "0x0",
    "excessBlobGas": "0x0"
  },
  "blockValue": "0x2a",
  "blobsBundle": {"commitments": [], "proofs": [], "blobs": []},
  "shouldOverrideBuilder": false,
  "executionRequests": []
}`

var logsBloomZero = strings.Repeat("00", 256)

type recordedCall struct {
	Method string
	Params []json.RawMessage
}

// newTestingServer serves testing_buildBlockV1 with the given result (or a
// JSON-RPC error when errCode != 0) and records every call.
func newTestingServer(t *testing.T, errCode int, errMsg string) (*httptest.Server, *[]recordedCall) {
	t.Helper()

	calls := &[]recordedCall{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}

		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		*calls = append(*calls, recordedCall{Method: req.Method, Params: req.Params})

		w.Header().Set("Content-Type", "application/json")

		if errCode != 0 {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":%q}}`, req.ID, errCode, errMsg)

			return
		}

		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, testingPayloadJSON)
	}))

	t.Cleanup(srv.Close)

	return srv, calls
}

func TestProbeTestingAPI(t *testing.T) {
	tests := []struct {
		name      string
		errCode   int
		errMsg    string
		available bool
	}{
		{name: "method not found", errCode: -32601, errMsg: "the method testing_buildBlockV1 does not exist/is not available", available: false},
		{name: "nethermind disabled namespace", errCode: -32600, errMsg: "The method 'testing_buildBlockV1' is found but the namespace 'Testing' is disabled for http://0.0.0.0:8545.", available: false},
		{name: "invalid params proves existence", errCode: -32602, errMsg: "invalid argument 0", available: true},
		{name: "nethermind missing argument proves existence", errCode: -32602, errMsg: "missing value for required argument 0", available: true},
		{name: "besu invalid params proves existence", errCode: -32602, errMsg: "Invalid params", available: true},
		{name: "internal error proves existence", errCode: -32000, errMsg: "parentHash is not current head", available: true},
		{name: "success proves existence", errCode: 0, available: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := newTestingServer(t, tc.errCode, tc.errMsg)
			client := newTestClient(t, srv.URL)

			status := client.ProbeTestingAPI(context.Background())
			require.Equal(t, tc.available, status.Available)
			require.Len(t, *calls, 1)
			require.Equal(t, "testing_buildBlockV1", (*calls)[0].Method)

			if !tc.available {
				require.Contains(t, status.Reason, "does not expose")
			}
		})
	}
}

func TestProbeTestingAPITransportError(t *testing.T) {
	client := newTestClient(t, "http://127.0.0.1:1")

	status := client.ProbeTestingAPI(context.Background())
	require.False(t, status.Available)
	require.Contains(t, status.Reason, "probe failed")
}

func TestBuildBlockV1EncodesParamsAndDecodesResponse(t *testing.T) {
	srv, calls := newTestingServer(t, 0, "")
	client := newTestClient(t, srv.URL)

	parent := common.HexToHash("0xe27a3e81bd7cfe2aec2cc9e832c73a17c93e7efcf659cf4b39883b96c48708c2")
	attrs := &engineall.PayloadAttributes{
		Version:               enginev.DataVersionAmsterdam,
		Timestamp:             0x1ce,
		SuggestedFeeRecipient: paris.Address{0x11},
		Withdrawals:           nil,
		SlotNumber:            42,
		TargetGasLimit:        30_000_000,
	}

	tests := []struct {
		name     string
		txs      [][]byte
		extra    []byte
		wantTxs  string
		wantExtr string
	}{
		{name: "null transactions = EL mempool", txs: nil, wantTxs: "null", wantExtr: "null"},
		{name: "empty list = empty block", txs: [][]byte{}, wantTxs: "[]", wantExtr: "null"},
		{name: "explicit list + extra data", txs: [][]byte{{0x02, 0xf8}, {0x01}}, extra: []byte("bo"),
			wantTxs: `["0x02f8","0x01"]`, wantExtr: `"0x626f"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			*calls = (*calls)[:0]

			resp, err := client.BuildBlockV1(context.Background(), enginev.DataVersionAmsterdam, parent, attrs, tc.txs, tc.extra)
			require.NoError(t, err)
			require.NotNil(t, resp.ExecutionPayload)
			require.Equal(t, enginev.DataVersionAmsterdam, resp.Version)
			require.Equal(t, uint64(1), resp.ExecutionPayload.BlockNumber)
			require.Len(t, resp.ExecutionPayload.Transactions, 1)
			require.Equal(t, uint64(42), resp.BlockValue.Uint64())

			require.Len(t, *calls, 1)
			call := (*calls)[0]
			require.Equal(t, "testing_buildBlockV1", call.Method)
			require.Len(t, call.Params, 4)
			require.JSONEq(t, fmt.Sprintf("%q", parent.Hex()), string(call.Params[0]))

			var attrsJSON map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(call.Params[1], &attrsJSON))
			require.Contains(t, attrsJSON, "slotNumber", "post-Amsterdam attributes must carry slotNumber")
			require.Contains(t, attrsJSON, "targetGasLimit")
			require.JSONEq(t, `"0x2a"`, string(attrsJSON["slotNumber"]))

			require.JSONEq(t, tc.wantTxs, string(call.Params[2]))
			require.JSONEq(t, tc.wantExtr, string(call.Params[3]))
		})
	}
}

func TestBuildBlockV1PropagatesELError(t *testing.T) {
	srv, _ := newTestingServer(t, -32000, "nonce too low")
	client := newTestClient(t, srv.URL)

	attrs := &engineall.PayloadAttributes{Version: enginev.DataVersionPrague}

	_, err := client.BuildBlockV1(context.Background(), enginev.DataVersionPrague, common.Hash{}, attrs, [][]byte{{1}}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nonce too low")
}

func TestBuildBlockV1RejectsUnsupportedVersion(t *testing.T) {
	srv, _ := newTestingServer(t, 0, "")
	client := newTestClient(t, srv.URL)

	_, err := client.BuildBlockV1(context.Background(), enginev.DataVersionParis, common.Hash{},
		&engineall.PayloadAttributes{Version: enginev.DataVersionParis}, nil, nil)
	require.Error(t, err)
}
