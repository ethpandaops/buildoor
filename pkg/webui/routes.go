package webui

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/gorilla/mux"
	httpSwagger "github.com/swaggo/http-swagger"

	"github.com/ethpandaops/buildoor/pkg/webui/handlers/api"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers/docs"
	"github.com/ethpandaops/buildoor/version"
)

// APIBasePath is the base path of buildoor's own REST API. The Builder API
// (/eth/...), the JSON-RPC ingress (/rpc), /metrics and /debug/pprof keep
// their own paths.
const APIBasePath = "/api/v1"

// apiRoute is one REST API route: served under APIBasePath and, until 2.0,
// at its pre-v1 path as a deprecated alias.
type apiRoute struct {
	methods []string
	path    string // under APIBasePath
	handler http.HandlerFunc

	// legacyMethod and legacyPath name the pre-v1 route (empty = none).
	legacyMethod string
	legacyPath   string
}

func apiRoutes(h *api.APIHandler) []apiRoute {
	get := []string{http.MethodGet}
	post := []string{http.MethodPost}
	del := []string{http.MethodDelete}

	return []apiRoute{
		// Instance
		{methods: get, path: "/version", handler: h.GetVersion, legacyPath: "/api/version"},
		{methods: get, path: "/status", handler: h.GetStatus, legacyPath: "/api/status"},
		{methods: get, path: "/stats", handler: h.GetStats, legacyPath: "/api/stats"},
		{methods: get, path: "/events", handler: h.EventStream, legacyPath: "/api/events"},
		// Read by the overview UI of other buildoor versions (CORS).
		{methods: []string{http.MethodGet, http.MethodOptions}, path: "/overview", handler: h.GetOverview, legacyPath: "/api/buildoor/overview"},

		// Settings: GET the public config, PATCH any subset of settings.
		{methods: get, path: "/config", handler: h.GetConfig, legacyPath: "/api/config"},
		{methods: []string{http.MethodPatch}, path: "/config", handler: h.UpdateSettings, legacyMethod: http.MethodPost, legacyPath: "/api/config/settings"},

		// Per-slot action plans and outcomes
		{methods: get, path: "/action-plan", handler: h.GetActionPlan, legacyPath: "/api/buildoor/action-plan"},
		{methods: post, path: "/action-plan", handler: h.UpdateActionPlan, legacyPath: "/api/buildoor/action-plan"},
		{methods: get, path: "/action-plan/rules", handler: h.GetActionPlanRules, legacyPath: "/api/buildoor/action-plan/rules"},
		{methods: post, path: "/action-plan/rules", handler: h.UpdateActionPlanRules, legacyPath: "/api/buildoor/action-plan/rules"},
		{methods: post, path: "/action-plan/test-transform", handler: h.TestTransform, legacyPath: "/api/buildoor/action-plan/test-transform"},
		{methods: get, path: "/slot-results", handler: h.GetSlotResults, legacyPath: "/api/buildoor/slot-results"},
		{methods: get, path: "/slot-results/{slot}/payload", handler: h.GetSlotPayloadArtifact, legacyPath: "/api/buildoor/slot-results/{slot}/payload"},
		{methods: get, path: "/slot-results/{slot}/payloads/{blockHash}", handler: h.GetSlotPayloadArtifactByHash, legacyPath: "/api/buildoor/slot-results/{slot}/payloads/{blockHash}"},
		{methods: get, path: "/slot-results/{slot}/bids", handler: h.GetSlotBidArtifacts, legacyPath: "/api/buildoor/slot-results/{slot}/bids"},
		{methods: get, path: "/slot-results/{slot}/bids/{index}", handler: h.GetSlotBidArtifact, legacyPath: "/api/buildoor/slot-results/{slot}/bids/{index}"},
		{methods: get, path: "/slot-results/{slot}/envelope", handler: h.GetSlotEnvelopeArtifact, legacyPath: "/api/buildoor/slot-results/{slot}/envelope"},
		{methods: get, path: "/head-votes/{slot}", handler: h.GetHeadVoteDetail, legacyPath: "/api/buildoor/head-votes/{slot}"},
		{methods: get, path: "/bids-won", handler: h.GetBidsWon, legacyPath: "/api/buildoor/bids-won"},

		// Builder API, local build and the transaction pool
		{methods: get, path: "/builder-api/status", handler: h.GetBuilderAPIStatus, legacyPath: "/api/buildoor/builder-api-status"},
		{methods: get, path: "/local-build/status", handler: h.GetLocalBuildStatus, legacyPath: "/api/buildoor/local-build/status"},
		{methods: post, path: "/local-build/probe", handler: h.ProbeLocalBuild, legacyPath: "/api/buildoor/local-build/probe"},
		{methods: get, path: "/txpool", handler: h.GetTxPool, legacyPath: "/api/buildoor/txpool"},
		{methods: del, path: "/txpool", handler: h.ClearTxPool, legacyPath: "/api/buildoor/txpool"},
		{methods: get, path: "/next-block", handler: h.GetNextBlock, legacyPath: "/api/buildoor/txpool/preview"},
		{methods: get, path: "/payloads/{blockHash}/transactions", handler: h.GetPayloadTransactions},
		{methods: get, path: "/txpool/el-mempool", handler: h.GetELMempool, legacyPath: "/api/buildoor/txpool/el-mempool"},
		{methods: get, path: "/txpool/fees", handler: h.GetTxPoolFees, legacyPath: "/api/buildoor/txpool/fees"},
		{methods: del, path: "/txpool/{hash}", handler: h.DropTxPoolTx, legacyPath: "/api/buildoor/txpool/{hash}"},
		{methods: get, path: "/rpc-access", handler: h.GetRPCAccess, legacyPath: "/api/buildoor/rpc-access"},
		{methods: post, path: "/rpc-access/keys", handler: h.CreateRPCKey, legacyPath: "/api/buildoor/rpc-access/keys"},
		{methods: []string{http.MethodPatch}, path: "/rpc-access/keys/{id}", handler: h.UpdateRPCKey, legacyMethod: http.MethodPost, legacyPath: "/api/buildoor/rpc-access/keys/{id}"},
		{methods: del, path: "/rpc-access/keys/{id}", handler: h.DeleteRPCKey, legacyPath: "/api/buildoor/rpc-access/keys/{id}"},

		// Managed builder keys and their lifecycle (key 0 is the entry key)
		{methods: get, path: "/builder-keys", handler: h.GetBuilderKeys, legacyPath: "/api/buildoor/builder-keys"},
		{methods: post, path: "/builder-keys/target", handler: h.UpdateBuilderKeyTarget, legacyPath: "/api/buildoor/builder-keys/target"},
		{methods: post, path: "/builder-keys/{index:[0-9]+}/deposit", handler: h.DepositBuilderKey, legacyPath: "/api/buildoor/builder-keys/{index:[0-9]+}/deposit"},
		{methods: post, path: "/builder-keys/{index:[0-9]+}/topup", handler: h.TopupBuilderKey, legacyPath: "/api/buildoor/builder-keys/{index:[0-9]+}/topup"},
		{methods: post, path: "/builder-keys/{index:[0-9]+}/exit", handler: h.ExitBuilderKey, legacyPath: "/api/buildoor/builder-keys/{index:[0-9]+}/exit"},

		// Validator registrations and preferences
		{methods: get, path: "/registrations/validators", handler: h.GetValidators, legacyPath: "/api/buildoor/validators"},
		{methods: get, path: "/registrations/proposer-preferences", handler: h.GetProposerPreferences, legacyPath: "/api/buildoor/proposer-preferences"},
		{methods: get, path: "/registrations/builder-preferences", handler: h.GetBuilderPreferences, legacyPath: "/api/buildoor/builder-preferences"},

		{methods: get, path: "/audit-log", handler: h.GetAuditLog, legacyPath: "/api/buildoor/audit-log"},
	}
}

// registerAPI mounts the REST API, its Swagger UI (/api/docs/) and the
// deprecated pre-v1 aliases. Every other request under /api/ is answered
// with a JSON 404 or 405 here, never by the WebUI's catch-all, so a client
// with a wrong path or method gets an error instead of the SPA's HTML.
func registerAPI(router *mux.Router, routes []apiRoute) {
	apiRouter := router.PathPrefix("/api/").Subrouter()
	apiRouter.NotFoundHandler = apiFallback(apiRouter)
	apiRouter.MethodNotAllowedHandler = apiRouter.NotFoundHandler

	docs.SwaggerInfo.Version = version.GetBuildVersion()
	apiRouter.PathPrefix("/docs/").Handler(httpSwagger.Handler(func(c *httpSwagger.Config) {
		c.Layout = httpSwagger.StandaloneLayout
	}))

	for _, route := range routes {
		apiRouter.HandleFunc(strings.TrimPrefix(APIBasePath, "/api")+route.path, route.handler).
			Methods(route.methods...)
	}

	for _, route := range routes {
		if route.legacyPath == "" {
			continue
		}

		methods := route.methods
		if route.legacyMethod != "" {
			methods = []string{route.legacyMethod}
		}

		apiRouter.Handle(strings.TrimPrefix(route.legacyPath, "/api"), deprecatedRoute(APIBasePath+route.path, route.handler)).
			Methods(methods...)
	}
}

// routeVarPattern matches a mux path variable, with or without a pattern.
var routeVarPattern = regexp.MustCompile(`\{([^}:]+)(:[^}]*)?\}`)

// deprecatedRoute serves a pre-v1 alias: the same handler, with the
// Deprecation header (RFC 9745) and a Link to the successor path.
func deprecatedRoute(successor string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		target := routeVarPattern.ReplaceAllStringFunc(successor, func(match string) string {
			return vars[routeVarPattern.FindStringSubmatch(match)[1]]
		})

		w.Header().Set("Deprecation", "true")
		w.Header().Add("Link", "<"+target+`>; rel="successor-version"`)

		next.ServeHTTP(w, r)
	})
}

// apiFallbackMethods are the methods apiFallback probes for an Allow header.
var apiFallbackMethods = []string{
	http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
}

// apiFallback answers a request no API route serves: 405 with the methods
// the path does serve, else 404. The router cannot tell the two apart on
// its own: a subrouter's routes inherit the /api/ prefix matcher, and a
// later route matching that prefix clears the method mismatch an earlier
// one recorded.
func apiFallback(apiRouter *mux.Router) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed := make([]string, 0, len(apiFallbackMethods))

		for _, method := range apiFallbackMethods {
			if method == r.Method {
				continue
			}

			probe := r.Clone(r.Context())
			probe.Method = method

			var match mux.RouteMatch
			if apiRouter.Match(probe, &match) && match.MatchErr == nil {
				allowed = append(allowed, method)
			}
		}

		if len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")

			return
		}

		writeJSONError(w, http.StatusNotFound, "endpoint not found")
	})
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
