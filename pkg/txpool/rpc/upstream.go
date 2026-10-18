package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	gethrpc "github.com/ethereum/go-ethereum/rpc"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/buildoor/pkg/config"
)

// upstreamDialTimeout bounds connecting to a websocket upstream (http
// upstreams connect lazily per call).
const upstreamDialTimeout = 5 * time.Second

// ErrNoUpstream is returned when no configured upstream can serve a call.
var ErrNoUpstream = errors.New("no RPC upstream available")

// UpstreamPool routes the ingress' EL reads through the endpoints configured in
// txpool.rpc_upstreams. Groups are tried by ascending priority; within a group
// the endpoints take turns (round robin), and a transport failure moves on to
// the next endpoint. A JSON-RPC error is an answer and returned as is. The
// list is read live, so UI changes apply to the next call.
type UpstreamPool struct {
	cfg   *config.Config // shared live config; the upstream list is read per call
	local ELProxy        // the EL behind --el-rpc; nil when not configured
	log   logrus.FieldLogger

	mu      sync.Mutex
	spec    string
	groups  []*upstreamGroup
	remotes map[string]*remoteUpstream
}

var _ ELProxy = (*UpstreamPool)(nil)

type upstreamGroup struct {
	priority uint64
	members  []*upstream
	next     atomic.Uint64
}

type upstream struct {
	target   string
	priority uint64
	proxy    ELProxy // nil = unusable (local without --el-rpc, failed dial)
	err      string  // why proxy is nil
	calls    atomic.Uint64
	failures atomic.Uint64
}

// remoteUpstream adapts a JSON-RPC client to ELProxy.
type remoteUpstream struct {
	client *gethrpc.Client
}

func (r *remoteUpstream) RawCall(ctx context.Context, method string, params []any) ([]byte, error) {
	var result json.RawMessage
	if err := r.client.CallContext(ctx, &result, method, params...); err != nil {
		return nil, err
	}

	return result, nil
}

// NewUpstreamPool creates the pool; local may be nil.
func NewUpstreamPool(cfg *config.Config, local ELProxy, log logrus.FieldLogger) *UpstreamPool {
	return &UpstreamPool{
		cfg:     cfg,
		local:   local,
		log:     log.WithField("component", "txpool-rpc-upstreams"),
		remotes: make(map[string]*remoteUpstream, 4),
	}
}

// RawCall implements ELProxy.
func (p *UpstreamPool) RawCall(ctx context.Context, method string, params []any) ([]byte, error) {
	var lastErr error

	for _, group := range p.current() {
		start := group.next.Add(1) - 1
		count := uint64(len(group.members))

		for i := range count {
			member := group.members[(start+i)%count]
			if member.proxy == nil {
				continue
			}

			member.calls.Add(1)

			raw, err := member.proxy.RawCall(ctx, method, params)
			if err == nil || isRPCAnswer(err) {
				return raw, err
			}

			member.failures.Add(1)
			lastErr = fmt.Errorf("%s: %w", member.target, err)

			if ctx.Err() != nil {
				return nil, lastErr
			}
		}
	}

	if lastErr == nil {
		lastErr = ErrNoUpstream
	}

	return nil, lastErr
}

// isRPCAnswer reports whether err is a JSON-RPC error response (the endpoint
// answered) rather than a transport failure.
func isRPCAnswer(err error) bool {
	var rpcErr gethrpc.Error

	return errors.As(err, &rpcErr)
}

// UpstreamStats is one endpoint's view for the WebUI.
type UpstreamStats struct {
	Target    string `json:"target"`
	Priority  uint64 `json:"priority"`
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
	Calls     uint64 `json:"calls"`
	Failures  uint64 `json:"failures"`
}

// Stats returns the configured endpoints in routing order.
func (p *UpstreamPool) Stats() []UpstreamStats {
	out := make([]UpstreamStats, 0, 4)

	for _, group := range p.current() {
		for _, member := range group.members {
			out = append(out, UpstreamStats{
				Target:    member.target,
				Priority:  member.priority,
				Available: member.proxy != nil,
				Error:     member.err,
				Calls:     member.calls.Load(),
				Failures:  member.failures.Load(),
			})
		}
	}

	return out
}

// current returns the routing groups, rebuilt when the configured list
// changed. Remote clients are reused across rebuilds and closed once unused.
func (p *UpstreamPool) current() []*upstreamGroup {
	spec := p.cfg.TxPool.RPCUpstreams

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.groups != nil && spec == p.spec {
		return p.groups
	}

	upstreams, err := config.ParseRPCUpstreams(spec)
	if err != nil {
		// Validated on write; keep routing through the previous list.
		p.log.WithError(err).Warn("Invalid RPC upstream list, keeping the previous one")

		if p.groups == nil {
			upstreams = []config.RPCUpstream{{Target: config.RPCUpstreamLocal}}
		} else {
			return p.groups
		}
	}

	byPriority := make(map[uint64]*upstreamGroup, len(upstreams))
	used := make(map[string]bool, len(upstreams))

	for _, entry := range upstreams {
		member := &upstream{target: entry.Target, priority: entry.Priority}

		if entry.Target == config.RPCUpstreamLocal {
			if p.local != nil {
				member.proxy = p.local
			} else {
				member.err = "no --el-rpc configured"
			}
		} else {
			remote, dialErr := p.remote(entry.Target)
			if dialErr != nil {
				member.err = dialErr.Error()
			} else {
				member.proxy = remote
			}

			used[entry.Target] = true
		}

		group := byPriority[entry.Priority]
		if group == nil {
			group = &upstreamGroup{priority: entry.Priority}
			byPriority[entry.Priority] = group
		}

		group.members = append(group.members, member)
	}

	for target, remote := range p.remotes {
		if !used[target] {
			remote.client.Close()
			delete(p.remotes, target)
		}
	}

	groups := make([]*upstreamGroup, 0, len(byPriority))
	for _, group := range byPriority {
		groups = append(groups, group)
	}

	sort.Slice(groups, func(i, j int) bool { return groups[i].priority < groups[j].priority })

	p.spec = spec
	p.groups = groups

	return groups
}

// remote returns the (cached) client for an upstream URL. Caller holds p.mu.
func (p *UpstreamPool) remote(target string) (*remoteUpstream, error) {
	if remote, ok := p.remotes[target]; ok {
		return remote, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), upstreamDialTimeout)
	defer cancel()

	client, err := gethrpc.DialContext(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	remote := &remoteUpstream{client: client}
	p.remotes[target] = remote

	return remote, nil
}

// Close releases the remote clients.
func (p *UpstreamPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for target, remote := range p.remotes {
		remote.client.Close()
		delete(p.remotes, target)
	}

	p.groups = nil
}
