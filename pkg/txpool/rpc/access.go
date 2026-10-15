package rpc

import (
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/ethpandaops/buildoor/pkg/config"
)

// Caller kinds of the ingress.
const (
	CallerAnonymous = "anonymous" // no credentials (per client IP or shared)
	CallerAPIKey    = "api_key"   // a key managed in the WebUI
	CallerStatic    = "static"    // --txpool-auth-token
	CallerLogin     = "login"     // a WebUI login (authenticatoor JWT)
)

const (
	// anonSharedID is the identity every anonymous caller shares in the
	// shared scope.
	anonSharedID = "shared"
	// maxAnonCallers bounds the per-IP limiters and stats; the least recently
	// seen IP is dropped beyond it.
	maxAnonCallers = 1024
	// maxMethodsPerCaller bounds the per-method stats of one caller; further
	// methods are counted as "other".
	maxMethodsPerCaller = 32
)

// Access errors, each answered with HTTP 401.
var (
	ErrInvalidKey  = errors.New("invalid API key")
	ErrKeyDisabled = errors.New("API key disabled")
	ErrKeyRequired = errors.New("an API key is required")
)

// Caller is a resolved ingress caller.
type Caller struct {
	Kind  string // CallerAnonymous, CallerAPIKey, CallerStatic or CallerLogin
	ID    string // client IP / "shared", key id, "static", "login"
	Label string // key label
	key   *APIKey
}

func (c Caller) statsKey() string { return c.Kind + ":" + c.ID }

// LoginChecker validates a WebUI login (the authenticatoor JWT); nil when no
// auth provider is configured.
type LoginChecker interface {
	ValidLogin(r *http.Request) bool
}

// Access authenticates, rate-limits and accounts ingress calls. Every setting
// is read live from the shared config.
type Access struct {
	cfg   *config.Config
	keys  *KeyStore
	login LoginChecker

	mu       sync.Mutex
	limiters map[string]*callerLimiter
	stats    map[string]*callerStats
	anonSeen []string // anonymous stats keys, least recently created first
}

type callerLimiter struct {
	limiter *rate.Limiter
	perSec  uint64
}

type callerStats struct {
	caller      Caller
	calls       uint64
	errors      uint64
	rateLimited uint64
	methods     map[string]uint64
	firstSeen   time.Time
	lastSeen    time.Time
}

// NewAccess creates the access layer; keys and login may be nil.
func NewAccess(cfg *config.Config, keys *KeyStore, login LoginChecker) *Access {
	return &Access{
		cfg:      cfg,
		keys:     keys,
		login:    login,
		limiters: make(map[string]*callerLimiter, 16),
		stats:    make(map[string]*callerStats, 16),
	}
}

// Resolve identifies the caller of a request. pathKey is the key from the
// /rpc/<key> URL form (empty otherwise); a key may also come as "X-API-Key"
// or "Authorization: Bearer". A presented key must be valid; without one the
// caller is a WebUI login (bearer JWT) or anonymous, which txpool.auth=api_key
// refuses.
func (a *Access) Resolve(r *http.Request, pathKey string) (Caller, error) {
	secret := pathKey
	if secret == "" {
		secret = strings.TrimSpace(r.Header.Get("X-API-Key"))
	}

	bearer := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		bearer = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}

	if secret == "" && bearer != "" && !a.isLogin(r) {
		secret = bearer
	}

	if secret != "" {
		if static := a.cfg.TxPool.AuthToken; static != "" &&
			subtle.ConstantTimeCompare([]byte(secret), []byte(static)) == 1 {
			return Caller{Kind: CallerStatic, ID: "static", Label: "--txpool-auth-token"}, nil
		}

		if a.keys != nil {
			if key := a.keys.Lookup(secret); key != nil {
				if !key.Enabled {
					return Caller{}, ErrKeyDisabled
				}

				return Caller{Kind: CallerAPIKey, ID: key.ID, Label: key.Label, key: key}, nil
			}
		}

		return Caller{}, ErrInvalidKey
	}

	if bearer != "" && a.isLogin(r) {
		return Caller{Kind: CallerLogin, ID: "login", Label: "WebUI login"}, nil
	}

	if a.cfg.TxPool.NormalizedAuth() == config.TxPoolAuthAPIKey {
		return Caller{}, ErrKeyRequired
	}

	if a.cfg.TxPool.RPCAnonScope == config.RPCAnonScopeShared {
		return Caller{Kind: CallerAnonymous, ID: anonSharedID}, nil
	}

	return Caller{Kind: CallerAnonymous, ID: ClientIP(r, a.cfg.TxPool.RPCProxyHops)}, nil
}

func (a *Access) isLogin(r *http.Request) bool {
	return a.login != nil && a.login.ValidLogin(r)
}

// ClientIP returns the caller's address: with hops trusted reverse proxies in
// front, the X-Forwarded-For entry that many hops from the right (each proxy
// appends the address it saw); with fewer entries than hops, or hops 0, the
// connection's address.
func ClientIP(r *http.Request, hops uint64) string {
	if hops > 0 {
		var entries []string

		for _, header := range r.Header.Values("X-Forwarded-For") {
			for _, entry := range strings.Split(header, ",") {
				if entry = strings.TrimSpace(entry); entry != "" {
					entries = append(entries, entry)
				}
			}
		}

		if uint64(len(entries)) >= hops {
			return entries[uint64(len(entries))-hops]
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

// rateFor returns the caller's limit in calls per second (0 = unlimited).
func (a *Access) rateFor(caller Caller) uint64 {
	switch caller.Kind {
	case CallerAnonymous:
		return a.cfg.TxPool.RPCAnonRate
	case CallerAPIKey:
		if caller.key != nil && caller.key.RateLimit != nil {
			return *caller.key.RateLimit
		}

		return a.cfg.TxPool.RPCKeyRate
	case CallerStatic:
		return a.cfg.TxPool.RPCKeyRate
	default: // a WebUI login is the operator
		return 0
	}
}

// Allow takes one call from the caller's budget. Limits apply live: a changed
// rate replaces the caller's bucket. The burst is one second's worth.
func (a *Access) Allow(caller Caller) bool {
	perSec := a.rateFor(caller)
	if perSec == 0 {
		return true
	}

	key := caller.statsKey()

	a.mu.Lock()

	entry := a.limiters[key]
	if entry == nil || entry.perSec != perSec {
		entry = &callerLimiter{limiter: rate.NewLimiter(rate.Limit(perSec), int(perSec)), perSec: perSec} //nolint:gosec // bounded by the setting
		a.limiters[key] = entry
	}

	a.mu.Unlock()

	return entry.limiter.Allow()
}

// Outcomes of an accounted call.
const (
	outcomeOK          = "ok"
	outcomeError       = "error"
	outcomeRateLimited = "rate_limited"
)

// Record accounts one call of the caller.
func (a *Access) Record(caller Caller, method, outcome string) {
	key := caller.statsKey()
	now := time.Now()

	a.mu.Lock()
	defer a.mu.Unlock()

	stats := a.stats[key]
	if stats == nil {
		stats = &callerStats{caller: caller, methods: make(map[string]uint64, 8), firstSeen: now}
		a.stats[key] = stats

		if caller.Kind == CallerAnonymous {
			a.trackAnonLocked(key)
		}
	}

	stats.caller = caller // the current key record (label, limit)
	stats.lastSeen = now
	stats.calls++

	switch outcome {
	case outcomeError:
		stats.errors++
	case outcomeRateLimited:
		stats.rateLimited++
	}

	if _, known := stats.methods[method]; !known && len(stats.methods) >= maxMethodsPerCaller {
		method = "other"
	}

	stats.methods[method]++
}

// trackAnonLocked bounds the anonymous callers; the oldest is dropped with
// its limiter. Caller holds a.mu.
func (a *Access) trackAnonLocked(key string) {
	a.anonSeen = append(a.anonSeen, key)

	for len(a.anonSeen) > maxAnonCallers {
		oldest := a.anonSeen[0]
		a.anonSeen = a.anonSeen[1:]

		delete(a.stats, oldest)
		delete(a.limiters, oldest)
	}
}

// CallerStats is one caller's accounting for the WebUI.
type CallerStats struct {
	Kind        string            `json:"kind"`
	ID          string            `json:"id"`
	Label       string            `json:"label,omitempty"`
	RateLimit   uint64            `json:"rate_limit"` // effective calls per second (0 = unlimited)
	Calls       uint64            `json:"calls"`
	Errors      uint64            `json:"errors"`
	RateLimited uint64            `json:"rate_limited"`
	Methods     map[string]uint64 `json:"methods"`
	FirstSeen   time.Time         `json:"first_seen"`
	LastSeen    time.Time         `json:"last_seen"`
}

// Stats returns every caller's accounting since the start, most recent first.
func (a *Access) Stats() []CallerStats {
	a.mu.Lock()

	out := make([]CallerStats, 0, len(a.stats))

	for _, stats := range a.stats {
		methods := make(map[string]uint64, len(stats.methods))
		for method, count := range stats.methods {
			methods[method] = count
		}

		out = append(out, CallerStats{
			Kind:        stats.caller.Kind,
			ID:          stats.caller.ID,
			Label:       stats.caller.Label,
			Calls:       stats.calls,
			Errors:      stats.errors,
			RateLimited: stats.rateLimited,
			Methods:     methods,
			FirstSeen:   stats.firstSeen,
			LastSeen:    stats.lastSeen,
			RateLimit:   a.rateFor(stats.caller),
		})
	}

	a.mu.Unlock()

	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })

	return out
}
