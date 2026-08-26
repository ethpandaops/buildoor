package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// newNonceServer serves eth_getTransactionCount, answering with the nonce mapped
// to the requested block tag or a JSON-RPC error for tags that are absent, the
// way nimbus-eth1 rejects "pending". It records the tags it was asked for.
func newNonceServer(t *testing.T, nonces map[string]uint64) (*httptest.Server, *[]string) {
	t.Helper()

	tags := &[]string{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}

		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "eth_getTransactionCount", req.Method)
		require.Len(t, req.Params, 2)

		var tag string

		require.NoError(t, json.Unmarshal(req.Params[1], &tag))

		*tags = append(*tags, tag)

		w.Header().Set("Content-Type", "application/json")

		nonce, ok := nonces[tag]
		if !ok {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"Unsupported block tag %s"}}`, req.ID, tag)

			return
		}

		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":"0x%x"}`, req.ID, nonce)
	}))

	t.Cleanup(srv.Close)

	return srv, tags
}

func newTestClient(t *testing.T, url string) *Client {
	t.Helper()

	client, err := NewClient(context.Background(), url, logrus.New())
	require.NoError(t, err)

	t.Cleanup(client.Close)

	return client
}

func TestGetNoncePrefersPending(t *testing.T) {
	srv, tags := newNonceServer(t, map[string]uint64{"pending": 9, "latest": 7})
	client := newTestClient(t, srv.URL)

	nonce, err := client.GetNonce(context.Background(), common.Address{})
	require.NoError(t, err)
	require.Equal(t, uint64(9), nonce)
	require.Equal(t, []string{"pending"}, *tags)
}

func TestGetNonceFallsBackToLatestWhenPendingUnsupported(t *testing.T) {
	srv, tags := newNonceServer(t, map[string]uint64{"latest": 7})
	client := newTestClient(t, srv.URL)

	nonce, err := client.GetNonce(context.Background(), common.Address{})
	require.NoError(t, err)
	require.Equal(t, uint64(7), nonce)
	require.Equal(t, []string{"pending", "latest"}, *tags)
}

func TestGetNonceReportsBothErrors(t *testing.T) {
	srv, _ := newNonceServer(t, map[string]uint64{})
	client := newTestClient(t, srv.URL)

	_, err := client.GetNonce(context.Background(), common.Address{})
	require.ErrorContains(t, err, "Unsupported block tag pending")
	require.ErrorContains(t, err, "Unsupported block tag latest")
}
