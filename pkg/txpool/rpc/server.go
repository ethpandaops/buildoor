// Package rpc is the transaction pool's JSON-RPC ingress: an eth_* endpoint
// transaction generators (spamoor) point at. Submissions land in the owned
// pool; read-only calls are proxied to the EL so the endpoint works as a
// generator's only RPC host.
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	gethrpc "github.com/ethereum/go-ethereum/rpc"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/txpool"
)

// Limits of the ingress.
const (
	maxBodyBytes  = 32 << 20 // blob transactions with sidecars are large
	maxBatchCalls = 100
	callTimeout   = 30 * time.Second
)

// JSON-RPC 2.0 error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeServerError    = -32000
)

// ELProxy is the EL surface the passthrough needs (satisfied by
// *execution.Client).
type ELProxy interface {
	RawCall(ctx context.Context, method string, params []any) ([]byte, error)
}

// Authorizer decides whether an ingress request may proceed. The three modes
// are open (every caller), the authenticatoor JWT the mutating API endpoints
// use, and a static shared secret.
type Authorizer interface {
	// Authorize reports whether the request's Authorization header is
	// acceptable.
	Authorize(r *http.Request) bool
}

// OpenAuthorizer accepts every request.
type OpenAuthorizer struct{}

// Authorize implements Authorizer.
func (OpenAuthorizer) Authorize(*http.Request) bool { return true }

// StaticAuthorizer requires "Authorization: Bearer <token>" with the
// configured shared secret.
type StaticAuthorizer struct {
	Token string
}

// Authorize implements Authorizer.
func (a StaticAuthorizer) Authorize(r *http.Request) bool {
	return a.Token != "" && r.Header.Get("Authorization") == "Bearer "+a.Token
}

// Server is the ingress HTTP handler.
type Server struct {
	log     logrus.FieldLogger
	pool    *txpool.Pool
	el      ELProxy
	auth    Authorizer
	version string
}

var _ http.Handler = (*Server)(nil)

// NewServer creates the ingress for the given pool. el may be nil, in which
// case read methods answer "method not found". A nil auth means open.
func NewServer(pool *txpool.Pool, el ELProxy, auth Authorizer, version string, log logrus.FieldLogger) *Server {
	if auth == nil {
		auth = OpenAuthorizer{}
	}

	return &Server{
		log:     log.WithField("component", "txpool-rpc"),
		pool:    pool,
		el:      el,
		auth:    auth,
		version: version,
	}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ServeHTTP handles single and batched JSON-RPC 2.0 requests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "POST only", http.StatusMethodNotAllowed)

		return
	}

	if !s.auth.Authorize(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)

		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeResponses(w, []response{errorResponse(nil, codeParseError, "cannot read body: "+err.Error())}, false)

		return
	}

	body = []byte(strings.TrimSpace(string(body)))
	if len(body) == 0 {
		writeResponses(w, []response{errorResponse(nil, codeInvalidRequest, "empty request")}, false)

		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), callTimeout)
	defer cancel()

	if body[0] == '[' {
		var batch []request
		if err := json.Unmarshal(body, &batch); err != nil {
			writeResponses(w, []response{errorResponse(nil, codeParseError, err.Error())}, false)

			return
		}

		if len(batch) == 0 || len(batch) > maxBatchCalls {
			writeResponses(w, []response{errorResponse(nil, codeInvalidRequest, "invalid batch size")}, false)

			return
		}

		responses := make([]response, 0, len(batch))
		for i := range batch {
			responses = append(responses, s.dispatch(ctx, &batch[i]))
		}

		writeResponses(w, responses, true)

		return
	}

	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		writeResponses(w, []response{errorResponse(nil, codeParseError, err.Error())}, false)

		return
	}

	writeResponses(w, []response{s.dispatch(ctx, &req)}, false)
}

// dispatch answers one call locally or through the EL passthrough.
func (s *Server) dispatch(ctx context.Context, req *request) response {
	if req.Method == "" {
		return errorResponse(req.ID, codeInvalidRequest, "missing method")
	}

	params, err := decodeParams(req.Params)
	if err != nil {
		return errorResponse(req.ID, codeInvalidParams, err.Error())
	}

	switch req.Method {
	case "eth_sendRawTransaction":
		return s.sendRawTransaction(req.ID, params)
	case "eth_chainId":
		return s.chainID(req.ID)
	case "net_version":
		return s.netVersion(req.ID)
	case "web3_clientVersion":
		return resultResponse(req.ID, "buildoor/"+s.version)
	case "eth_getTransactionCount":
		return s.transactionCount(ctx, req.ID, params)
	case "eth_getTransactionByHash":
		return s.transactionByHash(ctx, req.ID, params)
	case "txpool_status":
		return s.txpoolStatus(req.ID)
	case "txpool_content":
		return s.txpoolContent(req.ID)
	}

	if !passthroughAllowed(req.Method) {
		return errorResponse(req.ID, codeMethodNotFound, fmt.Sprintf("the method %s is not available", req.Method))
	}

	return s.proxy(ctx, req.ID, req.Method, params)
}

// passthroughAllowed reports whether a method is forwarded to the EL: the
// read-only eth_/net_/web3_ surface. Everything that mutates node state or
// belongs to privileged namespaces is refused.
func passthroughAllowed(method string) bool {
	switch {
	case strings.HasPrefix(method, "net_"), strings.HasPrefix(method, "web3_"):
		return true
	case !strings.HasPrefix(method, "eth_"):
		return false
	}

	switch method {
	case "eth_sendTransaction", "eth_sendRawTransaction", "eth_sign", "eth_signTransaction",
		"eth_submitWork", "eth_submitHashrate", "eth_subscribe", "eth_unsubscribe",
		"eth_newFilter", "eth_newBlockFilter", "eth_newPendingTransactionFilter",
		"eth_uninstallFilter", "eth_getFilterChanges", "eth_getFilterLogs",
		"eth_sendRawTransactionSync":
		return false
	default:
		return true
	}
}

func (s *Server) sendRawTransaction(id json.RawMessage, params []json.RawMessage) response {
	if len(params) < 1 {
		return errorResponse(id, codeInvalidParams, "missing raw transaction")
	}

	var raw hexutil.Bytes
	if err := json.Unmarshal(params[0], &raw); err != nil {
		return errorResponse(id, codeInvalidParams, "invalid raw transaction: "+err.Error())
	}

	hash, err := s.pool.Add(raw)
	if err != nil {
		return errorResponse(id, codeServerError, err.Error())
	}

	return resultResponse(id, hash)
}

func (s *Server) chainID(id json.RawMessage) response {
	chainID := s.pool.ChainID()
	if chainID == nil {
		return errorResponse(id, codeServerError, txpool.ErrNotStarted.Error())
	}

	return resultResponse(id, hexutil.EncodeBig(chainID))
}

func (s *Server) netVersion(id json.RawMessage) response {
	chainID := s.pool.ChainID()
	if chainID == nil {
		return errorResponse(id, codeServerError, txpool.ErrNotStarted.Error())
	}

	return resultResponse(id, chainID.String())
}

// transactionCount answers the pending nonce pool-aware (the EL's latest nonce
// advanced over our queued run); other block tags are proxied verbatim.
func (s *Server) transactionCount(ctx context.Context, id json.RawMessage, params []json.RawMessage) response {
	if len(params) < 2 {
		return s.proxy(ctx, id, "eth_getTransactionCount", params)
	}

	var tag string
	if err := json.Unmarshal(params[1], &tag); err != nil || tag != "pending" {
		return s.proxy(ctx, id, "eth_getTransactionCount", params)
	}

	var addr common.Address
	if err := json.Unmarshal(params[0], &addr); err != nil {
		return errorResponse(id, codeInvalidParams, "invalid address: "+err.Error())
	}

	if s.el == nil {
		return errorResponse(id, codeMethodNotFound, "eth_getTransactionCount is not available")
	}

	raw, err := s.el.RawCall(ctx, "eth_getTransactionCount", []any{addr, "latest"})
	if err != nil {
		return proxyError(id, err)
	}

	var latest hexutil.Uint64
	if err := json.Unmarshal(raw, &latest); err != nil {
		return errorResponse(id, codeServerError, "invalid nonce from EL: "+err.Error())
	}

	return resultResponse(id, hexutil.Uint64(s.pool.PendingNonce(addr, uint64(latest))))
}

// transactionByHash answers from the pool for queued transactions, else
// proxies.
func (s *Server) transactionByHash(ctx context.Context, id json.RawMessage, params []json.RawMessage) response {
	if len(params) >= 1 {
		var hash common.Hash
		if err := json.Unmarshal(params[0], &hash); err == nil {
			if tx := s.pool.Get(hash); tx != nil {
				return resultResponse(id, pendingTxJSON(tx))
			}
		}
	}

	return s.proxy(ctx, id, "eth_getTransactionByHash", params)
}

func (s *Server) txpoolStatus(id json.RawMessage) response {
	stats := s.pool.Stats()

	return resultResponse(id, map[string]hexutil.Uint64{
		"pending": hexutil.Uint64(stats.Pending), //nolint:gosec // counts are small
		"queued":  0,
	})
}

func (s *Server) txpoolContent(id json.RawMessage) response {
	txs, _ := s.pool.List(txpool.ListOptions{Sort: "sender"})
	pending := make(map[string]map[string]any, 64)

	for _, tx := range txs {
		sender := tx.Sender.Hex()
		if pending[sender] == nil {
			pending[sender] = make(map[string]any, 8)
		}

		pending[sender][fmt.Sprintf("%d", tx.Tx.Nonce())] = pendingTxJSON(tx)
	}

	return resultResponse(id, map[string]any{
		"pending": pending,
		"queued":  map[string]any{},
	})
}

// pendingTxJSON renders a queued transaction in the eth_getTransactionByHash
// shape (block fields null: not included).
func pendingTxJSON(tx *txpool.PooledTx) map[string]any {
	t := tx.Tx

	out := map[string]any{
		"hash":                 tx.Hash,
		"from":                 tx.Sender,
		"nonce":                hexutil.Uint64(t.Nonce()),
		"gas":                  hexutil.Uint64(t.Gas()),
		"gasPrice":             (*hexutil.Big)(t.GasPrice()),
		"maxFeePerGas":         (*hexutil.Big)(t.GasFeeCap()),
		"maxPriorityFeePerGas": (*hexutil.Big)(t.GasTipCap()),
		"value":                (*hexutil.Big)(t.Value()),
		"input":                hexutil.Bytes(t.Data()),
		"type":                 hexutil.Uint64(t.Type()),
		"chainId":              (*hexutil.Big)(t.ChainId()),
		"to":                   t.To(),
		"blockHash":            nil,
		"blockNumber":          nil,
		"transactionIndex":     nil,
	}

	v, r, sig := t.RawSignatureValues()
	out["v"] = (*hexutil.Big)(v)
	out["r"] = (*hexutil.Big)(r)
	out["s"] = (*hexutil.Big)(sig)

	if t.Type() >= 1 {
		out["accessList"] = t.AccessList()
	}

	if blobHashes := t.BlobHashes(); len(blobHashes) > 0 {
		out["blobVersionedHashes"] = blobHashes
		out["maxFeePerBlobGas"] = (*hexutil.Big)(t.BlobGasFeeCap())
	}

	return out
}

// proxy forwards a call to the EL verbatim.
func (s *Server) proxy(ctx context.Context, id json.RawMessage, method string, params []json.RawMessage) response {
	if s.el == nil {
		return errorResponse(id, codeMethodNotFound, fmt.Sprintf("the method %s is not available", method))
	}

	args := make([]any, len(params))
	for i, p := range params {
		args[i] = p
	}

	raw, err := s.el.RawCall(ctx, method, args)
	if err != nil {
		return proxyError(id, err)
	}

	return response{JSONRPC: "2.0", ID: id, Result: raw}
}

// proxyError maps an EL error back onto the wire, keeping its JSON-RPC code
// and message when it is one.
func proxyError(id json.RawMessage, err error) response {
	var rpcErr gethrpc.Error
	if errors.As(err, &rpcErr) {
		return errorResponse(id, rpcErr.ErrorCode(), rpcErr.Error())
	}

	return errorResponse(id, codeServerError, err.Error())
}

// decodeParams splits the params array; null/absent params are allowed.
func decodeParams(raw json.RawMessage) ([]json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}

	var params []json.RawMessage
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("params must be an array: %w", err)
	}

	return params, nil
}

func resultResponse(id json.RawMessage, result any) response {
	encoded, err := json.Marshal(result)
	if err != nil {
		return errorResponse(id, codeServerError, "encode result: "+err.Error())
	}

	return response{JSONRPC: "2.0", ID: normalizeID(id), Result: encoded}
}

func errorResponse(id json.RawMessage, code int, message string) response {
	return response{JSONRPC: "2.0", ID: normalizeID(id), Error: &rpcError{Code: code, Message: message}}
}

// normalizeID keeps the caller's id and substitutes null for a missing one so
// the response always carries the member.
func normalizeID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}

	return id
}

func writeResponses(w http.ResponseWriter, responses []response, batch bool) {
	w.Header().Set("Content-Type", "application/json")

	for i := range responses {
		responses[i].ID = normalizeID(responses[i].ID)
	}

	var payload any = responses[0]
	if batch {
		payload = responses
	}

	// An encode error means the client went away; nothing more to do.
	_ = json.NewEncoder(w).Encode(payload)
}
