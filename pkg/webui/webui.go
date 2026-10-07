package webui

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"os"
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

// authProviderAuthorizer accepts ingress requests carrying a JWT the
// authenticatoor validates — the same check the mutating API endpoints run.
// With no auth provider configured the API is open and so is the ingress.
type authProviderAuthorizer struct {
	handler *auth.AuthHandler
}

func (a authProviderAuthorizer) Authorize(r *http.Request) bool {
	if a.handler.IsOpen() {
		return true
	}

	return a.handler.CheckAuthToken(r.Header.Get("Authorization")) != nil
}

func StartHttpServer(frontendConfig *types.FrontendConfig, settingsSvc *config.Service, stateDB *db.Database, builderSvc *payload_builder.Service, epbsSvc *p2p_bidder.Service, lifecycleMgr *lifecycle.Manager, keys *builder_keys.Registry, chainSvc chain.Service, validatorStore *memstore.Store[phase0.BLSPubKey, *apiv1.SignedValidatorRegistration], builderAPISvc *builderapi.Server, propPrefSvc *payload_bidder.ProposerPreferencesService, valRanges *validatorranges.Resolver, revealSvc *payload_bidder.RevealService, inclusionTracker *payload_bidder.InclusionTracker, payments *payload_bidder.PaymentTracker, planSvc *action_plan.PlanService, resultTracker *slot_results.Tracker, txPool *txpool.Pool, elClient *execution.Client, planVerifier *tx_plan_verifier.Verifier) *api.APIHandler {
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

	// Transaction pool JSON-RPC ingress (eth_sendRawTransaction + read
	// passthrough). Its authentication is chosen by txpool.auth: open, the
	// authenticatoor JWT of the mutating API endpoints, or a static secret.
	if txPool != nil {
		cfg := settingsSvc.Load()

		var authorizer txpoolrpc.Authorizer

		switch cfg.TxPool.NormalizedAuth() {
		case config.TxPoolAuthToken:
			authorizer = authProviderAuthorizer{handler: authHandler}
		case config.TxPoolAuthStatic:
			authorizer = txpoolrpc.StaticAuthorizer{Token: cfg.TxPool.AuthToken}
		default:
			authorizer = txpoolrpc.OpenAuthorizer{}
		}

		ingress := txpoolrpc.NewServer(txPool, elClient, authorizer, version.GetBuildVersion(), logrus.StandardLogger())
		router.Handle("/rpc", ingress).Methods(http.MethodPost)
	}

	// API routes
	apiHandler := api.NewAPIHandler(authHandler, settingsSvc, stateDB, builderSvc, epbsSvc, lifecycleMgr, keys, chainSvc, validatorStore, builderAPISvc, propPrefSvc, valRanges, revealSvc, inclusionTracker, payments, planSvc, resultTracker)
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
	apiRouter.HandleFunc("/buildoor/txpool/{hash}", apiHandler.DropTxPoolTx).Methods(http.MethodDelete)

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

	// pprof handler — net/http/pprof registers itself on http.DefaultServeMux
	// via the blank import above.
	router.PathPrefix("/debug/pprof/").Handler(http.DefaultServeMux)

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
	router.PathPrefix("/").Handler(spaHandler)

	n := negroni.New()
	n.Use(negroni.NewRecovery())
	//n.Use(gzip.Gzip(gzip.DefaultCompression))
	n.UseHandler(router)

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
