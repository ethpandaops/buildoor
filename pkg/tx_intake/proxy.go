package tx_intake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/sirupsen/logrus"
)

// maxBodyBytes bounds an intake request body. Blob transactions with full
// sidecars are large and submitters may batch them.
const maxBodyBytes = 128 << 20

// Proxy is the JSON-RPC 2.0 intake endpoint. It answers eth_sendRawTransaction
// from the queue and eth_getTransactionCount("pending") from the queue on top
// of the EL's latest nonce; every other request is forwarded to the EL RPC
// verbatim. Batches are supported and answered in request order.
type Proxy struct {
	queue    *Queue
	upstream string
	client   *http.Client
	log      logrus.FieldLogger
}

// NewProxy creates an intake proxy in front of the given EL JSON-RPC URL.
func NewProxy(queue *Queue, upstreamURL string, log logrus.FieldLogger) *Proxy {
	return &Proxy{
		queue:    queue,
		upstream: upstreamURL,
		client:   &http.Client{Timeout: 30 * time.Second},
		log:      log.WithField("component", "tx-intake"),
	}
}

type rpcRequest struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      json.RawMessage   `json:"id"`
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func errorResponse(id json.RawMessage, code int, msg string) json.RawMessage {
	out, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
	return out
}

func resultResponse(id json.RawMessage, result any) json.RawMessage {
	raw, err := json.Marshal(result)
	if err != nil {
		return errorResponse(id, -32603, err.Error())
	}

	out, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Result: raw})

	return out
}

// ServeHTTP handles one JSON-RPC request or batch.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	body = bytes.TrimSpace(body)
	batch := len(body) > 0 && body[0] == '['

	var reqs []json.RawMessage
	if batch {
		err = json.Unmarshal(body, &reqs)
	} else {
		reqs = []json.RawMessage{body}
	}

	w.Header().Set("Content-Type", "application/json")

	if err != nil || len(reqs) == 0 {
		_, _ = w.Write(errorResponse(nil, -32700, "parse error"))
		return
	}

	responses := p.handle(r.Context(), reqs)

	var out []byte
	if batch {
		out, _ = json.Marshal(responses)
	} else {
		out = responses[0]
	}

	_, _ = w.Write(out)
}

// handle answers intercepted requests locally and forwards the rest to the
// upstream as one batch, keeping the caller's order.
func (p *Proxy) handle(ctx context.Context, reqs []json.RawMessage) []json.RawMessage {
	responses := make([]json.RawMessage, len(reqs))
	forwardIdx := make([]int, 0, len(reqs))

	for i, raw := range reqs {
		var req rpcRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			responses[i] = errorResponse(nil, -32600, "invalid request")
			continue
		}

		switch {
		case req.Method == "eth_sendRawTransaction":
			responses[i] = p.sendRawTransaction(&req)
		case req.Method == "eth_getTransactionCount" && isPendingTag(req.Params):
			responses[i] = p.pendingNonce(ctx, &req)
		default:
			forwardIdx = append(forwardIdx, i)
		}
	}

	if len(forwardIdx) == 0 {
		return responses
	}

	toForward := make([]json.RawMessage, len(forwardIdx))
	for k, i := range forwardIdx {
		toForward[k] = reqs[i]
	}

	forwarded, err := p.forwardBatch(ctx, toForward)
	if err != nil {
		p.log.WithError(err).Warn("Upstream forward failed")

		for _, i := range forwardIdx {
			var req rpcRequest
			_ = json.Unmarshal(reqs[i], &req)
			responses[i] = errorResponse(req.ID, -32603, "upstream: "+err.Error())
		}

		return responses
	}

	for k, i := range forwardIdx {
		responses[i] = forwarded[k]
	}

	return responses
}

func isPendingTag(params []json.RawMessage) bool {
	if len(params) < 2 {
		return false
	}

	var tag string
	if err := json.Unmarshal(params[1], &tag); err != nil {
		return false
	}

	return tag == "pending"
}

func (p *Proxy) sendRawTransaction(req *rpcRequest) json.RawMessage {
	if len(req.Params) != 1 {
		return errorResponse(req.ID, -32602, "eth_sendRawTransaction takes one param")
	}

	var raw hexutil.Bytes
	if err := json.Unmarshal(req.Params[0], &raw); err != nil {
		return errorResponse(req.ID, -32602, "invalid raw transaction: "+err.Error())
	}

	hash, err := p.queue.Add(raw)
	if err != nil {
		if errors.Is(err, ErrQueueFull) {
			return errorResponse(req.ID, -32005, err.Error())
		}

		return errorResponse(req.ID, -32000, err.Error())
	}

	return resultResponse(req.ID, hash)
}

func (p *Proxy) pendingNonce(ctx context.Context, req *rpcRequest) json.RawMessage {
	var addr common.Address
	if err := json.Unmarshal(req.Params[0], &addr); err != nil {
		return errorResponse(req.ID, -32602, "invalid address: "+err.Error())
	}

	chainNonce, err := p.LatestNonce(ctx, addr)
	if err != nil {
		return errorResponse(req.ID, -32603, "upstream: "+err.Error())
	}

	return resultResponse(req.ID, hexutil.Uint64(p.queue.PendingNonce(addr, chainNonce)))
}

// LatestNonce asks the upstream for the sender's nonce at the latest block.
func (p *Proxy) LatestNonce(ctx context.Context, addr common.Address) (uint64, error) {
	var nonce hexutil.Uint64
	if err := p.callUpstream(ctx, "eth_getTransactionCount", []any{addr, "latest"}, &nonce); err != nil {
		return 0, err
	}

	return uint64(nonce), nil
}

func (p *Proxy) callUpstream(ctx context.Context, method string, params []any, out any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}

	respBody, err := p.post(ctx, body)
	if err != nil {
		return err
	}

	var resp rpcResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return fmt.Errorf("decoding upstream response: %w", err)
	}

	if resp.Error != nil {
		return fmt.Errorf("%s: %s", method, resp.Error.Message)
	}

	return json.Unmarshal(resp.Result, out)
}

// forwardBatch sends the requests to the upstream as one batch and returns
// the responses in request order, matched by id.
func (p *Proxy) forwardBatch(ctx context.Context, reqs []json.RawMessage) ([]json.RawMessage, error) {
	var (
		body []byte
		err  error
	)

	if len(reqs) == 1 {
		body = reqs[0]
	} else {
		body, err = json.Marshal(reqs)
		if err != nil {
			return nil, err
		}
	}

	respBody, err := p.post(ctx, body)
	if err != nil {
		return nil, err
	}

	respBody = bytes.TrimSpace(respBody)
	if len(reqs) == 1 {
		return []json.RawMessage{respBody}, nil
	}

	var responses []json.RawMessage
	if err := json.Unmarshal(respBody, &responses); err != nil {
		return nil, fmt.Errorf("decoding upstream batch: %w", err)
	}

	byID := make(map[string]json.RawMessage, len(responses))
	for _, raw := range responses {
		var resp rpcResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			continue
		}

		byID[compactID(resp.ID)] = raw
	}

	out := make([]json.RawMessage, len(reqs))
	for i, raw := range reqs {
		var req rpcRequest
		_ = json.Unmarshal(raw, &req)

		resp, ok := byID[compactID(req.ID)]
		if !ok {
			resp = errorResponse(req.ID, -32603, "upstream returned no response for this id")
		}

		out[i] = resp
	}

	return out, nil
}

func compactID(id json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, id); err != nil {
		return string(id)
	}

	return buf.String()
}

func (p *Proxy) post(ctx context.Context, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.upstream, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	out, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, bytes.TrimSpace(out))
	}

	return out, nil
}
