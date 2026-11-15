package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/builder_keys"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/txpool"
	txpoolrpc "github.com/ethpandaops/buildoor/pkg/txpool/rpc"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers/auth"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers/auth/authtest"
)

// gatedEnv is an APIHandler behind a real token verifier (a local stand-in
// authenticatoor), with the services its gated endpoints change.
type gatedEnv struct {
	handler  *APIHandler
	provider *authtest.Provider
	cfg      *config.Config
	planSvc  *action_plan.PlanService
	stateDB  *db.Database
	rpcKeys  *txpoolrpc.KeyStore
}

// basicServices is the claim of a login with basic access and no feature.
const basicServices = "-all,+buildoor"

func newGatedEnv(t *testing.T, provider *authtest.Provider) *gatedEnv {
	t.Helper()

	log := logrus.New()
	log.SetOutput(io.Discard)

	keys := config.BuilderKeysConfig{TargetCount: 2, DiscoveryGap: 1, MaxIndex: 32}

	cfg := config.DefaultConfig()
	cfg.BuilderKeys = keys

	defaults := config.DefaultConfig()
	defaults.BuilderKeys = keys

	stateDB := db.NewDatabase(&db.Config{File: filepath.Join(t.TempDir(), "state.db")}, log)
	require.NoError(t, stateDB.Init())

	t.Cleanup(func() { _ = stateDB.Close() })

	settingsSvc, err := config.NewService(cfg, defaults, map[string]bool{}, stateDB, log)
	require.NoError(t, err)

	registry, err := builder_keys.NewRegistry(cfg, testEntryPrivkey, log)
	require.NoError(t, err)
	registry.Refresh()

	chainSvc := newPlanTestChain()
	planSvc := action_plan.NewPlanService(cfg, chainSvc, log)

	builderSvc, err := payload_builder.NewService(cfg, nil, chainSvc, planSvc, nil, common.Address{}, log)
	require.NoError(t, err)
	builderSvc.SetTxPool(txpool.NewPool(cfg, nil, chainSvc, nil, log))

	rpcKeys := txpoolrpc.NewKeyStore(memstore.New[string, *txpoolrpc.APIKey]())
	upstreams := txpoolrpc.NewUpstreamPool(cfg, nil, log)
	ingress := txpoolrpc.NewServer(builderSvc.TxPool(), upstreams, txpoolrpc.NewAccess(cfg, rpcKeys, nil), "test", log)

	authHandler, err := auth.NewAuthHandler(t.Context(), provider.URL(), "")
	require.NoError(t, err)
	require.False(t, authHandler.IsOpen())

	// Built without NewAPIHandler: no event stream (it needs a live chain).
	handler := &APIHandler{
		authHandler: authHandler,
		settingsSvc: settingsSvc,
		stateDB:     stateDB,
		builderSvc:  builderSvc,
		keys:        registry,
		chainSvc:    chainSvc,
		planSvc:     planSvc,
	}
	handler.SetRPCIngress(ingress, upstreams, rpcKeys)

	return &gatedEnv{
		handler:  handler,
		provider: provider,
		cfg:      cfg,
		planSvc:  planSvc,
		stateDB:  stateDB,
		rpcKeys:  rpcKeys,
	}
}

// call serves one request with a token carrying the given services claim
// ("none" = no Authorization header).
func (e *gatedEnv) call(
	handler http.HandlerFunc, method, body string, vars map[string]string, services string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/v1/under-test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	if services != "none" {
		req.Header.Set("Authorization", "Bearer "+e.provider.Token(services))
	}

	if vars != nil {
		req = mux.SetURLVars(req, vars)
	}

	rec := httptest.NewRecorder()
	handler(rec, req)

	return rec
}

func (e *gatedEnv) auditEntries(t *testing.T) []db.AuditLog {
	t.Helper()

	entries, _, err := e.stateDB.GetAuditLogs(0, 100)
	require.NoError(t, err)

	return entries
}

// gatedCase is one endpoint that needs a feature.
type gatedCase struct {
	name    string
	feature string
	method  string
	body    string
	vars    map[string]string
	handler func(e *gatedEnv) http.HandlerFunc
	// prepare sets up state the request acts on.
	prepare func(t *testing.T, e *gatedEnv)
	// unchanged asserts that a refused request had no effect.
	unchanged func(t *testing.T, e *gatedEnv)
	// success is the status of the request with the feature; done asserts
	// its effect.
	success int
	done    func(t *testing.T, e *gatedEnv)
	// audited is the action a successful request records ("" = none here).
	audited string
}

const gatedKeyID = "gated-key"

func gatedCases() []gatedCase {
	planCount := func(e *gatedEnv) int { return len(e.planSvc.GetRange(2000, 2000)) }

	createKey := func(t *testing.T, e *gatedEnv) {
		t.Helper()

		_, _, err := e.rpcKeys.Create(gatedKeyID, nil)
		require.NoError(t, err)
	}

	keyID := func(e *gatedEnv) string { return e.rpcKeys.List()[0].ID }

	return []gatedCase{
		{
			name: "POST /action-plan", feature: auth.FeaturePlan, method: http.MethodPost,
			body:      `{"updates":[{"slots":[2000],"bid":{"mode":"disabled"}}]}`,
			handler:   func(e *gatedEnv) http.HandlerFunc { return e.handler.UpdateActionPlan },
			unchanged: func(t *testing.T, e *gatedEnv) { assert.Zero(t, planCount(e), "plan saved") },
			success:   http.StatusOK,
			done:      func(t *testing.T, e *gatedEnv) { assert.Equal(t, 1, planCount(e)) },
			audited:   "action_plan.update",
		},
		{
			name: "POST /action-plan/rules", feature: auth.FeaturePlan, method: http.MethodPost,
			body:      `{"rules":[{"id":"r1","enabled":true,"slots_in_epoch":[31],"reveal":{"mode":"disabled"}}]}`,
			handler:   func(e *gatedEnv) http.HandlerFunc { return e.handler.UpdateActionPlanRules },
			unchanged: func(t *testing.T, e *gatedEnv) { assert.Empty(t, e.planSvc.Rules(), "rules saved") },
			success:   http.StatusOK,
			done:      func(t *testing.T, e *gatedEnv) { assert.Len(t, e.planSvc.Rules(), 1) },
			audited:   "action_plan.rules",
		},
		{
			name: "DELETE /txpool", feature: auth.FeatureTxPool, method: http.MethodDelete,
			handler: func(e *gatedEnv) http.HandlerFunc { return e.handler.ClearTxPool },
			success: http.StatusOK,
			audited: "txpool.clear",
		},
		{
			name: "DELETE /txpool/{hash}", feature: auth.FeatureTxPool, method: http.MethodDelete,
			vars:    map[string]string{"hash": common.Hash{1}.Hex()},
			handler: func(e *gatedEnv) http.HandlerFunc { return e.handler.DropTxPoolTx },
			success: http.StatusNotFound, // an empty pool: the transaction is not in it
			audited: "txpool.drop",
		},
		{
			name: "PATCH /config", feature: auth.FeatureConfig, method: http.MethodPatch,
			body:    `{"epbs.bid_subsidy": 12345}`,
			handler: func(e *gatedEnv) http.HandlerFunc { return e.handler.UpdateSettings },
			unchanged: func(t *testing.T, e *gatedEnv) {
				assert.NotEqual(t, int64(12345), e.cfg.EPBS.BidSubsidy, "setting stored")
			},
			success: http.StatusOK,
			done:    func(t *testing.T, e *gatedEnv) { assert.Equal(t, int64(12345), e.cfg.EPBS.BidSubsidy) },
			audited: "config.settings",
		},
		{
			name: "GET /rpc-access", feature: auth.FeatureRPC, method: http.MethodGet,
			handler: func(e *gatedEnv) http.HandlerFunc { return e.handler.GetRPCAccess },
			success: http.StatusOK,
		},
		{
			name: "POST /rpc-access/keys", feature: auth.FeatureRPC, method: http.MethodPost,
			body:      `{"label":"spammer"}`,
			handler:   func(e *gatedEnv) http.HandlerFunc { return e.handler.CreateRPCKey },
			unchanged: func(t *testing.T, e *gatedEnv) { assert.Empty(t, e.rpcKeys.List(), "key created") },
			success:   http.StatusOK,
			done:      func(t *testing.T, e *gatedEnv) { assert.Len(t, e.rpcKeys.List(), 1) },
			audited:   "rpc_key.create",
		},
		{
			name: "PATCH /rpc-access/keys/{id}", feature: auth.FeatureRPC, method: http.MethodPatch,
			body:    `{"enabled":false}`,
			prepare: createKey,
			handler: func(e *gatedEnv) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					e.handler.UpdateRPCKey(w, mux.SetURLVars(r, map[string]string{"id": keyID(e)}))
				}
			},
			unchanged: func(t *testing.T, e *gatedEnv) { assert.True(t, e.rpcKeys.List()[0].Enabled, "key disabled") },
			success:   http.StatusOK,
			done:      func(t *testing.T, e *gatedEnv) { assert.False(t, e.rpcKeys.List()[0].Enabled) },
			audited:   "rpc_key.update",
		},
		{
			name: "DELETE /rpc-access/keys/{id}", feature: auth.FeatureRPC, method: http.MethodDelete,
			prepare: createKey,
			handler: func(e *gatedEnv) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					e.handler.DeleteRPCKey(w, mux.SetURLVars(r, map[string]string{"id": keyID(e)}))
				}
			},
			unchanged: func(t *testing.T, e *gatedEnv) { assert.Len(t, e.rpcKeys.List(), 1, "key deleted") },
			success:   http.StatusOK,
			done:      func(t *testing.T, e *gatedEnv) { assert.Empty(t, e.rpcKeys.List()) },
			audited:   "rpc_key.delete",
		},
		{
			name: "POST /builder-keys/target", feature: auth.FeatureKeys, method: http.MethodPost,
			body:    `{"target":3}`,
			handler: func(e *gatedEnv) http.HandlerFunc { return e.handler.UpdateBuilderKeyTarget },
			unchanged: func(t *testing.T, e *gatedEnv) {
				assert.Equal(t, uint64(2), e.cfg.BuilderKeys.EffectiveTargetCount(), "target changed")
			},
			success: http.StatusOK,
			done: func(t *testing.T, e *gatedEnv) {
				assert.Equal(t, uint64(3), e.cfg.BuilderKeys.EffectiveTargetCount())
			},
			audited: "builder_keys.target",
		},
		// Without a lifecycle manager the key actions end at "not enabled":
		// what matters here is that the gate comes first.
		{
			name: "POST /builder-keys/{index}/deposit", feature: auth.FeatureKeys, method: http.MethodPost,
			vars:    map[string]string{"index": "1"},
			handler: func(e *gatedEnv) http.HandlerFunc { return e.handler.DepositBuilderKey },
			success: http.StatusNotFound,
		},
		{
			name: "POST /builder-keys/{index}/topup", feature: auth.FeatureKeys, method: http.MethodPost,
			vars:    map[string]string{"index": "1"},
			handler: func(e *gatedEnv) http.HandlerFunc { return e.handler.TopupBuilderKey },
			success: http.StatusNotFound,
		},
		{
			name: "POST /builder-keys/{index}/exit", feature: auth.FeatureKeys, method: http.MethodPost,
			vars:    map[string]string{"index": "1"},
			handler: func(e *gatedEnv) http.HandlerFunc { return e.handler.ExitBuilderKey },
			success: http.StatusNotFound,
		},
	}
}

// Every gated endpoint: no token is 401, a login without the feature is 403
// and changes nothing (no audit entry either), the feature opens the
// existing path.
func TestGatedEndpointsNeedTheirFeature(t *testing.T) {
	provider := authtest.NewProvider(t)

	for _, tt := range gatedCases() {
		t.Run(tt.name, func(t *testing.T) {
			env := newGatedEnv(t, provider)

			if tt.prepare != nil {
				tt.prepare(t, env)
			}

			rec := env.call(tt.handler(env), tt.method, tt.body, tt.vars, "none")
			require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
			require.JSONEq(t, `{"error":"unauthorized"}`, rec.Body.String())

			// Basic access only, then every feature but the needed one.
			for _, services := range []string{basicServices, "+buildoor:all:-" + tt.feature} {
				rec = env.call(tt.handler(env), tt.method, tt.body, tt.vars, services)
				require.Equal(t, http.StatusForbidden, rec.Code, "%s: %s", services, rec.Body.String())
				require.JSONEq(t,
					`{"error":"forbidden: this login lacks the feature buildoor:`+tt.feature+`"}`, rec.Body.String())
			}

			if tt.unchanged != nil {
				tt.unchanged(t, env)
			}

			require.Empty(t, env.auditEntries(t), "a refused request must not be audited")

			rec = env.call(tt.handler(env), tt.method, tt.body, tt.vars, "-all,+buildoor:"+tt.feature)
			require.Equal(t, tt.success, rec.Code, rec.Body.String())

			if tt.done != nil {
				tt.done(t, env)
			}

			if tt.audited != "" {
				entries := env.auditEntries(t)
				require.Len(t, entries, 1)
				assert.Equal(t, tt.audited, entries[0].Action)
				assert.Equal(t, authtest.Subject, entries[0].Actor)
			}
		})
	}
}

// The builder key settings are reachable through PATCH /config as well: they
// need the keys feature there too.
func TestBuilderKeySettingsNeedTheKeysFeature(t *testing.T) {
	env := newGatedEnv(t, authtest.NewProvider(t))

	const body = `{"builder_keys.target_count": 5}`

	rec := env.call(env.handler.UpdateSettings, http.MethodPatch, body, nil, "+buildoor:config")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"error":"forbidden: this login lacks the feature buildoor:keys"}`, rec.Body.String())

	rec = env.call(env.handler.UpdateSettings, http.MethodPatch, body, nil, "+buildoor:keys")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"error":"forbidden: this login lacks the feature buildoor:config"}`, rec.Body.String())

	assert.Equal(t, uint64(2), env.cfg.BuilderKeys.EffectiveTargetCount())
	require.Empty(t, env.auditEntries(t))

	rec = env.call(env.handler.UpdateSettings, http.MethodPatch, body, nil, "+buildoor:config:keys")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, uint64(5), env.cfg.BuilderKeys.EffectiveTargetCount())
}

// The endpoints that need a login and no feature.
func TestBasicEndpointsNeedALoginOnly(t *testing.T) {
	env := newGatedEnv(t, authtest.NewProvider(t))

	tests := []struct {
		name    string
		method  string
		body    string
		handler http.HandlerFunc
	}{
		{name: "POST /local-build/probe", method: http.MethodPost, handler: env.handler.ProbeLocalBuild},
		{
			name: "POST /action-plan/test-transform", method: http.MethodPost,
			body: `{"target":"bid","expression":"."}`, handler: env.handler.TestTransform,
		},
		{name: "GET /txpool", method: http.MethodGet, handler: env.handler.GetTxPool},
		{name: "GET /audit-log", method: http.MethodGet, handler: env.handler.GetAuditLog},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := env.call(tt.handler, tt.method, tt.body, nil, "none")
			require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())

			// A login for another tool only does not pass the service gate.
			rec = env.call(tt.handler, tt.method, tt.body, nil, "-all,+spamoor:all")
			require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())

			for _, services := range []string{basicServices, ""} {
				rec = env.call(tt.handler, tt.method, tt.body, nil, services)
				require.Equal(t, http.StatusOK, rec.Code, "%q: %s", services, rec.Body.String())
			}
		})
	}
}
