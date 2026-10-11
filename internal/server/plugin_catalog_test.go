package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/plugins"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type catalogRouteTransport func(*http.Request) (*http.Response, error)

func (f catalogRouteTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func catalogRouteTestHandler(t *testing.T, upstream http.HandlerFunc) http.Handler {
	t.Helper()
	remote := httptest.NewServer(upstream)
	t.Cleanup(remote.Close)
	base, err := url.Parse(remote.URL)
	require.NoError(t, err)
	client := plugins.NewCatalogClient(catalogRouteTransport(func(r *http.Request) (*http.Response, error) {
		assert.Equal(t, "grafana.com", r.URL.Host)
		assert.Empty(t, r.Header.Get("Authorization"), "workspace token must not reach the catalog")
		forward := r.Clone(r.Context())
		forward.URL.Scheme, forward.URL.Host = base.Scheme, base.Host
		return http.DefaultTransport.RoundTrip(forward)
	}))
	api := http.NewServeMux()
	server := &Server{Token: "catalog-workspace-token"}
	server.pluginCatalogRoutes(api, client)
	return server.protect(api)
}

func catalogRouteCall(handler http.Handler, path, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestPluginCatalogRoutesAreAuthenticatedAndNormalized(t *testing.T) {
	var requests atomic.Int32
	plugin := `{"slug":"clock-panel","name":"Clock","typeCode":"panel","version":"2.0.0","orgName":"Grafana","packages":{"any":{}}}`
	handler := catalogRouteTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/plugins":
			fmt.Fprint(w, `{"items":[`+plugin+`]}`)
		case "/api/plugins/clock-panel":
			fmt.Fprint(w, plugin)
		case "/api/plugins/clock-panel/versions":
			fmt.Fprint(w, `{"items":[{"pluginSlug":"clock-panel","version":"2.0.0","packages":{"any":{}}}]}`)
		default:
			http.NotFound(w, r)
		}
	})
	for _, path := range []string{"/api/v1/plugins/catalog", "/api/v1/plugin-catalog/clock-panel", "/api/v1/plugin-catalog/clock-panel/versions"} {
		assert.Equal(t, http.StatusUnauthorized, catalogRouteCall(handler, path, "").Code)
		w := catalogRouteCall(handler, path, "catalog-workspace-token")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
		assert.NotContains(t, w.Body.String(), "catalog-workspace-token")
	}
	assert.Equal(t, int32(3), requests.Load(), "versions reuse the cached detail")
	w := catalogRouteCall(handler, "/api/v1/plugins/catalog?q=grafana&type=panel", "catalog-workspace-token")
	require.Equal(t, http.StatusOK, w.Code)
	var result plugins.CatalogList
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Len(t, result.Items, 1)
	assert.Equal(t, "clock-panel", result.Items[0].ID)
	assert.Equal(t, "Grafana", result.Items[0].Author)
	assert.Equal(t, int32(3), requests.Load())
	w = catalogRouteCall(handler, "/api/v1/plugins/catalog?type=app", "catalog-workspace-token")
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"items":[]}`, w.Body.String())
}

func TestPluginCatalogRoutesValidateBeforeFetching(t *testing.T) {
	var requests atomic.Int32
	handler := catalogRouteTestHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected", 500)
	})
	for _, path := range []string{
		"/api/v1/plugins/catalog?type=sql",
		"/api/v1/plugins/catalog?q=" + strings.Repeat("a", 257),
		"/api/v1/plugins/catalog?q=bad%00query",
		"/api/v1/plugins/catalog?q=bad%zz",
		"/api/v1/plugins/catalog?q=a&q=b",
		"/api/v1/plugins/catalog?type=app&type=panel",
		"/api/v1/plugins/catalog?url=https%3A%2F%2Fevil.example",
		"/api/v1/plugin-catalog/invalid!",
		"/api/v1/plugin-catalog/invalid!/versions",
	} {
		w := catalogRouteCall(handler, path, "catalog-workspace-token")
		assert.Equal(t, http.StatusBadRequest, w.Code, path+" "+w.Body.String())
		assert.Contains(t, w.Body.String(), `"error"`)
	}
	assert.Zero(t, requests.Load())
}

func TestPluginCatalogRoutesReportUpstreamErrors(t *testing.T) {
	for _, test := range []struct{ upstream, expected int }{
		{404, 404}, {503, 502}, {429, 502},
	} {
		handler := catalogRouteTestHandler(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(test.upstream)
			fmt.Fprint(w, "remote service failed")
		})
		w := catalogRouteCall(handler, "/api/v1/plugin-catalog/missing-panel", "catalog-workspace-token")
		assert.Equal(t, test.expected, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), `"error"`)
		assert.NotContains(t, w.Body.String(), "remote service failed")
	}
	handler := catalogRouteTestHandler(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	r := httptest.NewRequest(http.MethodGet, "/api/v1/plugins/catalog", nil)
	r.Header.Set("Authorization", "Bearer catalog-workspace-token")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Millisecond)
	defer cancel()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r.WithContext(ctx))
	assert.Equal(t, http.StatusGatewayTimeout, w.Code, w.Body.String())
}

func TestPluginCatalogRoutesCoexistWithCompleteServerHandler(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	server := New(database, "catalog-workspace-token", 30)
	t.Cleanup(func() { server.Live.Close(); server.Plugins.Close(); database.DB.Close() })
	var handler http.Handler
	require.NotPanics(t, func() { handler = server.Handler() }, "catalog discovery must coexist with installed plugin app-settings routes")
	for _, path := range []string{
		"/api/v1/plugins/catalog",
		"/api/v1/plugin-catalog/clock-panel",
		"/api/v1/plugin-catalog/clock-panel/versions",
		"/api/v1/plugins/clock-panel/app-settings",
	} {
		assert.Equal(t, http.StatusUnauthorized, catalogRouteCall(handler, path, "").Code, path)
	}
	assert.Equal(t, http.StatusBadRequest, catalogRouteCall(handler, "/api/v1/plugins/catalog?type=invalid", "catalog-workspace-token").Code)
	assert.Equal(t, http.StatusBadRequest, catalogRouteCall(handler, "/api/v1/plugin-catalog/invalid!", "catalog-workspace-token").Code)
}
