package webui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/buildoor/pkg/webui/handlers/api"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers/auth"
	"github.com/ethpandaops/buildoor/pkg/webui/handlers/auth/authtest"
)

// Every route and every alias must be unique per method, or one of them
// silently shadows the other.
func TestAPIRoutesAreUnique(t *testing.T) {
	seen := make(map[string]bool, 128)

	add := func(method, path string) {
		key := method + " " + path
		require.False(t, seen[key], "route %s registered twice", key)
		seen[key] = true
	}

	for _, route := range apiRoutes(&api.APIHandler{}) {
		require.True(t, strings.HasPrefix(route.path, "/"), "path %q", route.path)
		require.NotNil(t, route.handler, "path %q", route.path)

		for _, method := range route.methods {
			add(method, APIBasePath+route.path)
		}

		if route.legacyPath == "" {
			continue
		}

		require.True(t, strings.HasPrefix(route.legacyPath, "/api/"), "legacy path %q", route.legacyPath)

		if route.legacyMethod != "" {
			add(route.legacyMethod, route.legacyPath)
		} else {
			for _, method := range route.methods {
				add(method, route.legacyPath)
			}
		}
	}
}

// The real table, end to end: a v1 route and its pre-v1 alias.
func TestAPIRoutesServeVersion(t *testing.T) {
	router := mux.NewRouter()
	registerAPI(router, apiRoutes(&api.APIHandler{}))

	for _, path := range []string{"/api/v1/version", "/api/version"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code, path)
		require.Contains(t, rec.Body.String(), `"version"`, path)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))
	require.Equal(t, `</api/v1/version>; rel="successor-version"`, rec.Header().Get("Link"))

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/config", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, "GET, PATCH", rec.Header().Get("Allow"))
}

func TestRegisterAPI(t *testing.T) {
	echo := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, "%s %s", name, mux.Vars(r)["id"])
		}
	}

	router := mux.NewRouter()
	registerAPI(router, []apiRoute{
		{methods: []string{http.MethodGet}, path: "/things/{id:[0-9]+}", handler: echo("get"), legacyPath: "/api/old/things/{id:[0-9]+}"},
		{methods: []string{http.MethodPatch}, path: "/things/{id:[0-9]+}", handler: echo("patch"), legacyMethod: http.MethodPost, legacyPath: "/api/old/things/{id:[0-9]+}"},
		{methods: []string{http.MethodGet}, path: "/new-only", handler: echo("new")},
	})
	router.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "spa")
	})

	serve := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, path, nil))

		return rec
	}

	// v1 routes, no deprecation.
	rec := serve(http.MethodGet, "/api/v1/things/7")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "get 7", rec.Body.String())
	require.Empty(t, rec.Header().Get("Deprecation"))

	rec = serve(http.MethodPatch, "/api/v1/things/7")
	require.Equal(t, "patch 7", rec.Body.String())

	// Aliases serve the same handler, marked deprecated with their successor.
	rec = serve(http.MethodGet, "/api/old/things/7")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "get 7", rec.Body.String())
	require.Equal(t, "true", rec.Header().Get("Deprecation"))
	require.Equal(t, `</api/v1/things/7>; rel="successor-version"`, rec.Header().Get("Link"))

	rec = serve(http.MethodPost, "/api/old/things/7")
	require.Equal(t, "patch 7", rec.Body.String())
	require.Equal(t, `</api/v1/things/7>; rel="successor-version"`, rec.Header().Get("Link"))

	// A wrong method or path under /api/ is a JSON error, never the SPA.
	for _, tt := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPost, "/api/v1/things/7", http.StatusMethodNotAllowed},
		{http.MethodPatch, "/api/old/things/7", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/v1/things/x", http.StatusNotFound},
		{http.MethodGet, "/api/v1/nope", http.StatusNotFound},
		{http.MethodGet, "/api/old/new-only", http.StatusNotFound},
		{http.MethodGet, "/api/", http.StatusNotFound},
	} {
		rec = serve(tt.method, tt.path)
		require.Equal(t, tt.status, rec.Code, "%s %s", tt.method, tt.path)
		require.Equal(t, "application/json", rec.Header().Get("Content-Type"), "%s %s", tt.method, tt.path)

		var body map[string]string
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "%s %s", tt.method, tt.path)
		require.NotEmpty(t, body["error"], "%s %s", tt.method, tt.path)
	}

	require.Equal(t, "GET, PATCH", serve(http.MethodPost, "/api/v1/things/7").Header().Get("Allow"))
	require.Equal(t, "GET, POST", serve(http.MethodPatch, "/api/old/things/7").Header().Get("Allow"))

	// The docs live under /api/; WebUI views next to it stay the SPA's.
	rec = serve(http.MethodGet, "/api/docs/doc.json")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"basePath": "/api/v1"`)

	for _, path := range []string{"/api-docs", "/apifoo", "/dashboard"} {
		require.Equal(t, "spa", serve(http.MethodGet, path).Body.String(), path)
	}
}

// The overview UI polls instances of other versions: it reads the v1 path and
// falls back to the pre-v1 one of instances that do not have it, whether they
// answer a 404 or their WebUI's HTML.
func TestOverviewProxyFallsBackToPreV1Instances(t *testing.T) {
	current := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != APIBasePath+"/overview" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":"v1"}`)
	}))
	defer current.Close()

	preV1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/buildoor/overview" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"version":"pre-v1"}`)

			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html>spa</html>")
	}))
	defer preV1.Close()

	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	hosts := []OverviewHostEntry{{ID: 0, URL: current.URL}, {ID: 1, URL: preV1.URL}, {ID: 2, URL: down.URL}}

	router := mux.NewRouter()
	router.HandleFunc("/api/v1/hosts/{idx}/overview", overviewProxyHandler(hosts, logrus.New()))

	serve := func(idx string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/hosts/"+idx+"/overview", nil))

		return rec
	}

	rec := serve("0")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"version":"v1"}`, rec.Body.String())

	rec = serve("1")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"version":"pre-v1"}`, rec.Body.String())

	require.Equal(t, http.StatusBadGateway, serve("2").Code)
	require.Equal(t, http.StatusBadRequest, serve("3").Code)
}

// Every mutating route answers 401 to a request without a token when an auth
// provider is configured, on its v1 path and on its pre-v1 alias: its handler
// starts with the auth check (AuthHandler.Require, which also names the
// feature the handler needs). A new mutating route that is meant to be public
// fails here and has to be argued for.
func TestMutatingAPIRoutesNeedALogin(t *testing.T) {
	provider := authtest.NewProvider(t)

	authHandler, err := auth.NewAuthHandler(t.Context(), provider.URL(), "")
	require.NoError(t, err)
	require.False(t, authHandler.IsOpen())

	// A handler without any service: a request that got past the auth check
	// would run into one (and panic) instead of answering 401.
	handler := api.NewAPIHandler(authHandler, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil)
	routes := apiRoutes(handler)

	router := mux.NewRouter()
	registerAPI(router, routes)

	mutating := map[string]bool{
		http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
	}

	checked := 0

	check := func(method, path string) {
		if !mutating[method] {
			return
		}

		checked++

		// Path variables are filled with a value every pattern accepts.
		target := routeVarPattern.ReplaceAllString(path, "1")

		for _, header := range []string{"", "Bearer not-a-token"} {
			req := httptest.NewRequest(method, target, strings.NewReader("{}"))
			if header != "" {
				req.Header.Set("Authorization", header)
			}

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s (Authorization %q)", method, path, header)
			require.JSONEq(t, `{"error":"unauthorized"}`, rec.Body.String(), "%s %s", method, path)
		}
	}

	for _, route := range routes {
		for _, method := range route.methods {
			check(method, APIBasePath+route.path)

			if route.legacyPath == "" {
				continue
			}

			if route.legacyMethod != "" {
				method = route.legacyMethod
			}

			check(method, route.legacyPath)
		}
	}

	require.NotZero(t, checked, "no mutating route found")
}
