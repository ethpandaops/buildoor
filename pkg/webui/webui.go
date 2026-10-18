package webui

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	apiv1 "github.com/ethpandaops/go-eth2-client/api/v1"
	"github.com/ethpandaops/go-eth2-client/spec/phase0"

	"github.com/ethpandaops/buildoor/pkg/action_plan"
	"github.com/ethpandaops/buildoor/pkg/builder_keys"
	"github.com/ethpandaops/buildoor/pkg/builderapi"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/config"
	"github.com/ethpandaops/buildoor/pkg/db"
	"github.com/ethpandaops/buildoor/pkg/lifecycle"
	"github.com/ethpandaops/buildoor/pkg/memstore"
	"github.com/ethpandaops/buildoor/pkg/p2p_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_bidder"
	"github.com/ethpandaops/buildoor/pkg/payload_builder"
	"github.com/ethpandaops/buildoor/pkg/rpc/execution"
	"github.com/ethpandaops/buildoor/pkg/slot_results"
	"github.com/ethpandaops/buildoor/pkg/tx_plan_verifier"
	"github.com/ethpandaops/buildoor/pkg/txpool"
	txpoolrpc "github.com/ethpandaops/buildoor/pkg/txpool/rpc"
	"github.com/ethpandaops/buildoor/pkg/validatorranges"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers/api"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers/auth"
	"github.com/ethpandaops/buildoor/pkg/webui/types"
	"github.com/ethpandaops/buildoor/version"
	"github.com/gorilla/mux"
	"github.com/klauspost/compress/gzhttp"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	httpSwagger "github.com/swaggo/http-swagger"
	"github.com/urfave/negroni"

	_ "net/http/pprof"

	_ "github.com/ethpandaops/buildoor/pkg/webui/handlers/docs"
)

var (
	//go:embed static/*
	staticEmbedFS embed.FS
)

// loginChecker accepts ingress requests carrying a valid WebUI login (the
// authenticatoor JWT the mutating API endpoints check). Without an auth
// provider there is no login: the API is open, but /rpc callers stay
// anonymous.
type loginChecker struct {
	handler *auth.AuthHandler
}

func (c loginChecker) ValidLogin(r *http.Request) bool {
	if c.handler.IsOpen() {
		return false
	}

	return c.handler.CheckAuthToken(r.Header.Get("Authorization")) != nil
}

func StartHttpServer(frontendConfig *types.FrontendConfig, settingsSvc *config.Service, stateDB *db.Database, builderSvc *payload_builder.Service, epbsSvc *p2p_bidder.Service, lifecycleMgr *lifecycle.Manager, keys *builder_keys.Registry, chainSvc chain.Service, validatorStore *memstore.Store[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration], builderAPISvc *builderapi.Server, propPrefSvc *payload_bidder.ProposerPreferencesService, valRanges *validatorranges.Resolver, revealSvc *payload_bidder.RevealService, inclusionTracker *payload_bidder.InclusionTracker, payments *payload_bidder.PaymentTracker, planSvc *action_plan.PlanService, resultTracker *slot_results.Tracker, txPool *txpool.Pool, elClient *execution.Client, rpcKeys *txpoolrpc.KeyStore, planVerifier *tx_plan_verifier.Verifier) *api.APIHandler {
	authHandler, err := auth.NewAuthHandler(context.Background(), frontendConfig.AuthProviderURL)
	if err != nil {
		logrus.WithError(err).Fatal("failed to initialize auth handler")
	}
	if authHandler.IsOpen() {
		logrus.Warn("--auth-provider-url is empty: API endpoints are unauthenticated. Configure an authenticatoor URL to require auth.")
	}

	// init router
	router := mux.NewRouter()

	// Builder API routes (served on same port as the webui)
	if builderAPISvc != nil {
		builderAPISvc.RegisterRoutes(router)
	}

	// API routes
	apiHandler := api.NewAPIHandler(authHandler, settingsSvc, stateDB, builderSvc, epbsSvc, lifecycleMgr, keys, chainSvc, validatorStore, builderAPISvc, propPrefSvc, valRanges, revealSvc, inclusionTracker, payments, planSvc, resultTracker)

	// Transaction pool JSON-RPC ingress (eth_sendRawTransaction + read
	// passthrough): callers are anonymous (if txpool.auth allows), an API key
	// (header or /rpc/<key>), the static --txpool-auth-token or a WebUI login;
	// reads go through the txpool.rpc_upstreams pool.
	if txPool != nil {
		cfg := settingsSvc.Load()

		var local txpoolrpc.ELProxy
		if elClient != nil {
			local = elClient
		}

		upstreams := txpoolrpc.NewUpstreamPool(cfg, local, logrus.StandardLogger())
		access := txpoolrpc.NewAccess(cfg, rpcKeys, loginChecker{handler: authHandler})

		ingress := txpoolrpc.NewServer(txPool, upstreams, access, version.GetBuildVersion(), logrus.StandardLogger())
		router.Handle(txpoolrpc.PathPrefix, ingress).Methods(http.MethodPost)
		router.Handle(txpoolrpc.PathPrefix+"/{key}", ingress).Methods(http.MethodPost)

		apiHandler.SetRPCIngress(ingress, upstreams, rpcKeys)
	}

	apiHandler.SetPlanVerifier(planVerifier)
	apiRouter := router.PathPrefix("/api").Subrouter()
	apiRouter.HandleFunc("/version", apiHandler.GetVersion).Methods("GET")
	apiRouter.HandleFunc("/status", apiHandler.GetStatus).Methods(http.MethodGet)
	apiRouter.HandleFunc("/stats", apiHandler.GetStats).Methods(http.MethodGet)

	// Event stream endpoint for real-time updates
	apiRouter.HandleFunc("/events", apiHandler.EventStream).Methods(http.MethodGet)

	// Configuration endpoints
	apiRouter.HandleFunc("/config", apiHandler.GetConfig).Methods(http.MethodGet)
	apiRouter.HandleFunc("/config/schedule", apiHandler.UpdateSchedule).Methods(http.MethodPost)
	apiRouter.HandleFunc("/config/epbs", apiHandler.UpdateEPBS).Methods(http.MethodPost)

	// Shared builder config endpoint (build start time, payload build delay)
	apiRouter.HandleFunc("/config/builder", apiHandler.UpdateBuilderConfig).Methods(http.MethodPost)

	// Service toggle endpoint (enable/disable ePBS and Builder API)
	apiRouter.HandleFunc("/services/toggle", apiHandler.ToggleServices).Methods(http.MethodPost)

	// Builder API config endpoint
	apiRouter.HandleFunc("/config/builder-api", apiHandler.UpdateBuilderAPIConfig).Methods(http.MethodPost)

	// Generic path-based settings endpoint (partial updates by canonical key)
	apiRouter.HandleFunc("/config/settings", apiHandler.UpdateSettings).Methods(http.MethodPost)

	// Per-slot action plan + results endpoints
	apiRouter.HandleFunc("/buildoor/action-plan", apiHandler.GetActionPlan).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/action-plan", apiHandler.UpdateActionPlan).Methods(http.MethodPost)
	apiRouter.HandleFunc("/buildoor/action-plan/rules", apiHandler.GetActionPlanRules).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/action-plan/rules", apiHandler.UpdateActionPlanRules).Methods(http.MethodPost)
	apiRouter.HandleFunc("/buildoor/action-plan/test-transform", apiHandler.TestTransform).Methods(http.MethodPost)
	apiRouter.HandleFunc("/buildoor/slot-results", apiHandler.GetSlotResults).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/slot-results/{slot}/payload", apiHandler.GetSlotPayloadArtifact).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/slot-results/{slot}/payload/{index:[0-9]+}", apiHandler.GetSlotPayloadArtifactByIndex).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/slot-results/{slot}/bids", apiHandler.GetSlotBidArtifacts).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/slot-results/{slot}/bids/{index}", apiHandler.GetSlotBidArtifact).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/slot-results/{slot}/envelope", apiHandler.GetSlotEnvelopeArtifact).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/head-votes/{slot}", apiHandler.GetHeadVoteDetail).Methods(http.MethodGet)

	// Local build extension (testing_buildBlockV1) + owned transaction pool
	apiRouter.HandleFunc("/buildoor/local-build/status", apiHandler.GetLocalBuildStatus).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/local-build/probe", apiHandler.ProbeLocalBuild).Methods(http.MethodPost)
	apiRouter.HandleFunc("/buildoor/txpool", apiHandler.GetTxPool).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/txpool", apiHandler.ClearTxPool).Methods(http.MethodDelete)
	apiRouter.HandleFunc("/buildoor/txpool/preview", apiHandler.GetTxPoolPreview).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/txpool/el-mempool", apiHandler.GetELMempool).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/txpool/fees", apiHandler.GetTxPoolFees).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/txpool/{hash}", apiHandler.DropTxPoolTx).Methods(http.MethodDelete)
	apiRouter.HandleFunc("/buildoor/rpc-access", apiHandler.GetRPCAccess).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/rpc-access/keys", apiHandler.CreateRPCKey).Methods(http.MethodPost)
	apiRouter.HandleFunc("/buildoor/rpc-access/keys/{id}", apiHandler.UpdateRPCKey).Methods(http.MethodPost)
	apiRouter.HandleFunc("/buildoor/rpc-access/keys/{id}", apiHandler.DeleteRPCKey).Methods(http.MethodDelete)

	// Buildoor endpoints
	apiRouter.HandleFunc("/buildoor/validators", apiHandler.GetValidators).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/bids-won", apiHandler.GetBidsWon).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/builder-api-status", apiHandler.GetBuilderAPIStatus).Methods(http.MethodGet)

	// Managed builder key set
	apiRouter.HandleFunc("/buildoor/builder-keys", apiHandler.GetBuilderKeys).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/builder-keys/target", apiHandler.UpdateBuilderKeyTarget).Methods(http.MethodPost)
	apiRouter.HandleFunc("/buildoor/builder-keys/{index:[0-9]+}/deposit", apiHandler.DepositBuilderKey).Methods(http.MethodPost)
	apiRouter.HandleFunc("/buildoor/builder-keys/{index:[0-9]+}/topup", apiHandler.TopupBuilderKey).Methods(http.MethodPost)
	apiRouter.HandleFunc("/buildoor/builder-keys/{index:[0-9]+}/exit", apiHandler.ExitBuilderKey).Methods(http.MethodPost)
	apiRouter.HandleFunc("/buildoor/overview", apiHandler.GetOverview).Methods(http.MethodGet, http.MethodOptions)
	apiRouter.HandleFunc("/buildoor/proposer-preferences", apiHandler.GetProposerPreferences).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/builder-preferences", apiHandler.GetBuilderPreferences).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/audit-log", apiHandler.GetAuditLog).Methods(http.MethodGet)

	// Lifecycle endpoints (if manager available)
	apiRouter.HandleFunc("/lifecycle/status", apiHandler.GetLifecycleStatus).Methods(http.MethodGet)
	apiRouter.HandleFunc("/lifecycle/deposit", apiHandler.PostDeposit).Methods(http.MethodPost)
	apiRouter.HandleFunc("/lifecycle/topup", apiHandler.PostTopup).Methods(http.MethodPost)
	apiRouter.HandleFunc("/lifecycle/exit", apiHandler.PostExit).Methods(http.MethodPost)
	apiRouter.HandleFunc("/config/lifecycle", apiHandler.UpdateLifecycleConfig).Methods(http.MethodPost)

	// metrics endpoint
	router.Handle("/metrics", promhttp.Handler()).Methods("GET")

	// swagger
	router.PathPrefix("/api/docs/").Handler(httpSwagger.Handler(func(c *httpSwagger.Config) {
		c.Layout = httpSwagger.StandaloneLayout
	}))

	// Optional raw HTML snippet injected into <head> of the served index.html
	// (e.g. the panda menu loader / analytics). The CLI flag wins; fall back
	// to the legacy BUILDOOR_INJECT_HEAD_HTML env var when the flag is empty.
	headInjectHTML := frontendConfig.InjectHeadHTML
	if headInjectHTML == "" {
		headInjectHTML = os.Getenv("BUILDOOR_INJECT_HEAD_HTML")
	}

	spaHandler, err := handlers.NewSPAHandler(
		logrus.WithField("module", "web-spa"),
		staticEmbedFS,
		handlers.RuntimeConfig{
			AuthProviderURL: frontendConfig.AuthProviderURL,
			OverviewURL:     frontendConfig.OverviewURL,
		},
		headInjectHTML,
	)
	if err != nil {
		logrus.Fatalf("error initializing spa handler: %v", err)
	}
	// pprof handler — net/http/pprof registers itself on http.DefaultServeMux
	// via the blank import above. Profiles need a login: the UI is public, and
	// a profile both exposes the process internals and costs CPU to collect.
	// Tools send the bearer token; a browser without the login cookie gets
	// the WebUI, whose gate page sets it (see requireLogin).
	router.PathPrefix("/debug/pprof/").Handler(debugHandler(authHandler, apiHandler, spaHandler, http.DefaultServeMux))

	router.PathPrefix("/").Handler(spaHandler)

	// Responses are gzipped for clients that accept it: the slot results and
	// the event stream are repetitive JSON (about 10:1). The event stream
	// stays live: every flush ends a gzip block, so each event reaches the
	// browser at once.
	compressed, err := gzhttp.NewWrapper(gzhttp.MinSize(1024))
	if err != nil {
		logrus.Fatalf("error initializing the compression middleware: %v", err)
	}

	n := negroni.New()
	n.Use(negroni.NewRecovery())
	n.UseHandler(compressed(router))

	if frontendConfig.Host == "" {
		frontendConfig.Host = "0.0.0.0"
	}
	if frontendConfig.Port == 0 {
		frontendConfig.Port = 8080
	}
	srv := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", frontendConfig.Host, frontendConfig.Port),
		WriteTimeout: 0,
		ReadTimeout:  0,
		IdleTimeout:  120 * time.Second,
		Handler:      n,
	}

	logrus.Printf("http server listening on %v", srv.Addr)
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			logrus.WithError(err).Fatal("Error serving frontend")
		}
	}()

	return apiHandler
}

// debugTokenCookie carries the login token for the /debug/pprof pages in a
// browser. The WebUI's gate page sets it from the session (path-scoped,
// SameSite=Strict, expiring with the token); see DebugLoginPage.tsx.
const debugTokenCookie = "buildoor_debug_token"

// debugHandler serves the pprof pages to requests that carry a valid login
// token, as a bearer header (tools: curl, go tool pprof) or the
// debugTokenCookie (a browser); always valid when no auth provider is
// configured. Every profile download (heap, goroutine dump, CPU profile,
// trace, the command line, ...) lands in the audit log; only the index page
// with its counts does not. A browser without a valid token gets the WebUI
// (gate), whose page for this path sets the cookie and reloads, or asks for
// a login; everything else is a 401.
func debugHandler(authHandler *auth.AuthHandler, auditor *api.APIHandler, gate, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" {
			if cookie, err := r.Cookie(debugTokenCookie); err == nil {
				header = cookie.Value
			}
		}

		if token := authHandler.CheckAuthToken(header); token != nil {
			profile := strings.Trim(strings.TrimPrefix(r.URL.Path, "/debug/pprof"), "/")
			if profile == "" {
				next.ServeHTTP(w, r)

				return
			}

			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)

			result := "ok"
			if recorder.status >= http.StatusBadRequest {
				result = fmt.Sprintf("error: HTTP %d", recorder.status)
			}

			auditor.Audit(r, token, "debug.pprof", profile, map[string]string{"query": r.URL.RawQuery}, result)

			return
		}

		if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
			w.Header().Set("Cache-Control", "no-store")
			gate.ServeHTTP(w, r)

			return
		}

		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "login required", http.StatusUnauthorized)
	})
}

// statusRecorder captures the status code a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush keeps streaming responses (CPU profile, trace) flushable.
func (s *statusRecorder) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
