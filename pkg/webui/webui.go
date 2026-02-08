package webui

import (
	"embed"
	"fmt"
	"net/http"
	"time"

	"github.com/ethpandaops/buildoor/pkg/builder"
	"github.com/ethpandaops/buildoor/pkg/builderapi"
	"github.com/ethpandaops/buildoor/pkg/builderapi/validators"
	"github.com/ethpandaops/buildoor/pkg/chain"
	"github.com/ethpandaops/buildoor/pkg/epbs"
	"github.com/ethpandaops/buildoor/pkg/lifecycle"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers/api"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers/auth"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers/docs"
	"github.com/ethpandaops/buildoor/pkg/webui/server"
	"github.com/ethpandaops/buildoor/pkg/webui/types"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"github.com/urfave/negroni"

	_ "net/http/pprof"
)

var (
	//go:embed static/*
	staticEmbedFS embed.FS

	//go:embed templates/*
	templateEmbedFS embed.FS
)

func StartHttpServer(config *types.FrontendConfig, builderSvc *builder.Service, epbsSvc *epbs.Service, lifecycleMgr *lifecycle.Manager, chainSvc chain.Service, validatorStore *validators.Store, builderAPISvc *builderapi.Server) *api.APIHandler {
	// init router
	router := mux.NewRouter()

	frontend, err := server.NewFrontend(config, staticEmbedFS, templateEmbedFS)
	if err != nil {
		logrus.Fatalf("error initializing frontend: %v", err)
	}

	authHandler := auth.NewAuthHandler(config.AuthKey, config.UserHeader, config.TokenKey)
	authRouter := router.PathPrefix("/auth").Subrouter()
	authRouter.HandleFunc("/token", authHandler.GetToken).Methods(http.MethodGet)
	authRouter.HandleFunc("/login", authHandler.GetLogin).Methods(http.MethodGet)

	// register frontend routes
	frontendHandler := handlers.NewFrontendHandler()
	router.HandleFunc("/", frontendHandler.Index).Methods("GET")

	// API routes
	apiHandler := api.NewAPIHandler(authHandler, builderSvc, epbsSvc, lifecycleMgr, chainSvc, validatorStore, builderAPISvc)
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

	// Service toggle endpoint (enable/disable ePBS and legacy builder)
	apiRouter.HandleFunc("/services/toggle", apiHandler.ToggleServices).Methods(http.MethodPost)

	// Builder API config endpoint
	apiRouter.HandleFunc("/config/builder-api", apiHandler.UpdateBuilderAPIConfig).Methods(http.MethodPost)

	// Buildoor endpoints
	apiRouter.HandleFunc("/buildoor/validators", apiHandler.GetValidators).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/bids-won", apiHandler.GetBidsWon).Methods(http.MethodGet)
	apiRouter.HandleFunc("/buildoor/builder-api-status", apiHandler.GetBuilderAPIStatus).Methods(http.MethodGet)

	// Lifecycle endpoints (if manager available)
	apiRouter.HandleFunc("/lifecycle/status", apiHandler.GetLifecycleStatus).Methods(http.MethodGet)
	apiRouter.HandleFunc("/lifecycle/deposit", apiHandler.PostDeposit).Methods(http.MethodPost)
	apiRouter.HandleFunc("/lifecycle/topup", apiHandler.PostTopup).Methods(http.MethodPost)
	apiRouter.HandleFunc("/lifecycle/exit", apiHandler.PostExit).Methods(http.MethodPost)

	// metrics endpoint
	router.Handle("/metrics", promhttp.Handler()).Methods("GET")

	// swagger
	router.PathPrefix("/docs/").Handler(docs.GetSwaggerHandler(logrus.StandardLogger()))

	if config.Pprof {
		// add pprof handler
		router.PathPrefix("/debug/pprof/").Handler(http.DefaultServeMux)
	}

	router.PathPrefix("/").Handler(frontend)

	n := negroni.New()
	n.Use(negroni.NewRecovery())
	//n.Use(gzip.Gzip(gzip.DefaultCompression))
	n.UseHandler(router)

	if config.Host == "" {
		config.Host = "0.0.0.0"
	}
	if config.Port == 0 {
		config.Port = 8080
	}
	srv := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", config.Host, config.Port),
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
