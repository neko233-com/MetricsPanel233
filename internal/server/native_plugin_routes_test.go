package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/plugins"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nativeRouteClock = `{"id":31,"slug":"grafana-clock-panel","name":"Clock","typeCode":"panel","version":"3.2.4","readme":"<h2>Clock</h2>","json":{"dependencies":{"plugins":[]}},"future":{"fingerprint":18446744073709551615},"packages":{"any":{}}}`
const nativeRouteVersion = `{"id":10701,"pluginId":31,"pluginSlug":"grafana-clock-panel","version":"3.2.4","createdAt":"2026-08-25T13:03:45Z","isCompatible":true,"grafanaDependency":">=11.0.0-0","status":"active"}`

func nativePluginTestServer(t *testing.T) *Server {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	app := New(database, "native-plugin-token", 30)
	t.Cleanup(func() { app.Live.Close(); app.Plugins.Close(); database.DB.Close() })
	return app
}

func nativePluginCall(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestNativePluginCatalogRoutesRetainJSONAndInstallSignedExactOrLatest(t *testing.T) {
	app := nativePluginTestServer(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/plugins":
			assert.Equal(t, "true", r.URL.Query().Get("includeDeprecated"))
			fmt.Fprint(w, `{"items":[`+nativeRouteClock+`]}`)
		case "/api/plugins/grafana-clock-panel":
			fmt.Fprint(w, nativeRouteClock)
		case "/api/plugins/grafana-clock-panel/versions":
			fmt.Fprint(w, `{"items":[`+nativeRouteVersion+`]}`)
		case "/api/plugins/grafana-clock-panel/versions/3.2.4":
			fmt.Fprint(w, nativeRouteVersion)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	base, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	catalog := plugins.NewCatalogClient(catalogRouteTransport(func(r *http.Request) (*http.Response, error) {
		assert.Equal(t, "grafana.com", r.URL.Host)
		assert.Empty(t, r.Header.Get("Authorization"))
		forward := r.Clone(r.Context())
		forward.URL.Scheme, forward.URL.Host = base.Scheme, base.Host
		return http.DefaultTransport.RoundTrip(forward)
	}))
	raw, err := os.ReadFile("../plugins/testdata/grafana-clock-panel-3.2.4.zip")
	require.NoError(t, err)
	var installedVersions []string
	install := func(ctx context.Context, id, version string) (model.Plugin, error) {
		assert.Equal(t, "grafana-clock-panel", id)
		assert.Equal(t, "3.2.4", version)
		installedVersions = append(installedVersions, version)
		return app.Plugins.Install(ctx, raw, "")
	}
	api := http.NewServeMux()
	app.nativePluginRoutes(api, catalog, install)
	handler := app.protect(api)
	for _, path := range []string{
		"/api/gnet/plugins?includeDeprecated=true", "/api/gnet/plugins/grafana-clock-panel",
		"/api/gnet/plugins/grafana-clock-panel/versions", "/api/gnet/plugins/grafana-clock-panel/versions/3.2.4",
	} {
		assert.Equal(t, 401, nativePluginCall(handler, "GET", path, "", "").Code)
		w := nativePluginCall(handler, "GET", path, "", app.Token)
		require.Equal(t, 200, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), `"id":`)
		if strings.Contains(path, "/versions") {
			assert.Contains(t, w.Body.String(), `"pluginSlug"`, path)
		}
		if path == "/api/gnet/plugins?includeDeprecated=true" || path == "/api/gnet/plugins/grafana-clock-panel" {
			assert.Contains(t, w.Body.String(), `"slug":"grafana-clock-panel"`)
			assert.Contains(t, w.Body.String(), `18446744073709551615`)
		}
	}
	// Exact and omitted versions both reach the existing real PGP verifier.
	w := nativePluginCall(handler, "POST", "/api/plugins/grafana-clock-panel/install", `{"version":"3.2.4"}`, app.Token)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.JSONEq(t, `""`, w.Body.String())
	first, err := app.Store.Plugin(context.Background(), "grafana-clock-panel")
	require.NoError(t, err)
	_, err = app.Plugins.SetEnabled(context.Background(), first.ID, false)
	require.NoError(t, err)
	w = nativePluginCall(handler, "POST", "/api/plugins/grafana-clock-panel/install", `{}`, app.Token)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, []string{"3.2.4", "3.2.4"}, installedVersions)
	again, err := app.Store.Plugin(context.Background(), first.ID)
	require.NoError(t, err)
	assert.Equal(t, first.InstalledAt, again.InstalledAt)
	assert.False(t, again.Enabled)
	entries, err := os.ReadDir(app.Plugins.Root)
	require.NoError(t, err)
	require.Len(t, entries, 1, "native reinstall must not leave staging directories")
	w = nativePluginCall(handler, "GET", "/api/plugins/grafana-clock-panel/markdown/README", "", app.Token)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "Clock")
	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	w = nativePluginCall(handler, "GET", "/api/plugins/errors", "", app.Token)
	require.Equal(t, 200, w.Code)
	assert.JSONEq(t, `[]`, w.Body.String())
	w = nativePluginCall(handler, "POST", "/api/plugins/grafana-clock-panel/uninstall", "", app.Token)
	require.Equal(t, 200, w.Code, w.Body.String())
	entries, err = os.ReadDir(app.Plugins.Root)
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.Equal(t, 404, nativePluginCall(handler, "POST", "/api/plugins/grafana-clock-panel/uninstall", "", app.Token).Code)
}

func TestNativePluginRoutesCoexistWithCompleteHandlerAndRejectInvalidBeforeNetwork(t *testing.T) {
	app := nativePluginTestServer(t)
	var handler http.Handler
	require.NotPanics(t, func() { handler = app.Handler() })
	for _, path := range []string{
		"/api/gnet/plugins", "/api/gnet/plugins/clock", "/api/gnet/plugins/clock/versions", "/api/gnet/plugins/clock/versions/1.0.0",
		"/api/plugins/errors", "/api/plugins/clock/markdown/README", "/api/plugins/clock/app-settings", "/api/plugins/clock/settings",
	} {
		assert.Equal(t, 401, nativePluginCall(handler, "GET", path, "", "").Code)
	}
	for _, path := range []string{
		"/api/gnet/plugins?includeDeprecated=bad", "/api/gnet/plugins?includeDeprecated=true&includeDeprecated=false",
		"/api/gnet/plugins?url=https://example.com", "/api/gnet/plugins/invalid!", "/api/gnet/plugins/invalid!/versions",
		"/api/gnet/plugins/clock/versions/latest", "/api/plugins/clock/markdown/module.js",
	} {
		w := nativePluginCall(handler, "GET", path, "", app.Token)
		assert.Equal(t, 400, w.Code, path+" "+w.Body.String())
	}
	assert.Equal(t, 400, nativePluginCall(handler, "POST", "/api/plugins/clock/install", `{"version":"latest"}`, app.Token).Code)
	assert.Equal(t, 400, nativePluginCall(handler, "POST", "/api/plugins/clock/install", `{"version":"1.0.0","url":"https://example.com"}`, app.Token).Code)
	assert.Equal(t, 401, nativePluginCall(handler, "POST", "/api/plugins/clock/install", `{}`, "").Code)
	assert.Equal(t, 401, nativePluginCall(handler, "POST", "/api/plugins/clock/uninstall", "", "").Code)
}

func nativeBundledPackage(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	archive := zip.NewWriter(&out)
	for path, content := range map[string]string{
		"plugin.json":        `{"id":"native-test-app","name":"Application","type":"app","info":{"version":"1.0.0"}}`,
		"module.js":          "System.register([],function(){return{execute:function(){}}})",
		"source/plugin.json": `{"id":"native-test-source","name":"Datasource","type":"datasource","info":{"version":"1.0.0"}}`,
		"source/module.js":   "System.register([],function(){return{execute:function(){}}})",
		"panel/plugin.json":  `{"id":"native-test-panel","name":"Panel","type":"panel","info":{"version":"1.0.0"}}`,
		"panel/module.js":    "System.register([],function(){return{execute:function(){}}})",
		"README.md":          "# Native application\n",
		"source/README.md":   "# Native datasource\n",
		"panel/README.md":    "# Native panel\n",
	} {
		entry, err := archive.Create(path)
		require.NoError(t, err)
		_, err = entry.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, archive.Close())
	return out.Bytes()
}

func TestNativeInstalledListFiltersSignatureAndActualErrors(t *testing.T) {
	app := nativePluginTestServer(t)
	app.Plugins.AllowUnsigned["native-test-app"] = true
	_, err := app.Plugins.Install(context.Background(), nativeBundledPackage(t), "")
	require.NoError(t, err)
	raw, err := os.ReadFile("../plugins/testdata/grafana-clock-panel-3.2.4.zip")
	require.NoError(t, err)
	_, err = app.Plugins.Install(context.Background(), raw, "")
	require.NoError(t, err)
	_, err = app.Plugins.SetEnabled(context.Background(), "native-test-panel", false)
	require.NoError(t, err)
	handler := app.Handler()
	for _, test := range []struct {
		query string
		count int
	}{
		{"", 6}, {"?type=panel", 2}, {"?type=datasource", 3}, {"?enabled=1", 5},
		{"?core=1", 2}, {"?core=0", 4}, {"?embedded=0", 4}, {"?embedded=include-datasource", 5}, {"?type=panel&enabled=1", 1},
	} {
		w := nativePluginCall(handler, "GET", "/api/plugins"+test.query, "", app.Token)
		require.Equal(t, 200, w.Code, w.Body.String())
		var result []map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		assert.Len(t, result, test.count, test.query)
	}
	w := nativePluginCall(handler, "GET", "/api/plugins/grafana-clock-panel/settings", "", app.Token)
	require.Equal(t, 200, w.Code, w.Body.String())
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &metadata))
	assert.Equal(t, "valid", metadata["signature"])
	assert.Equal(t, "grafana", metadata["signatureType"])
	assert.Equal(t, "grafana", metadata["signatureOrg"])
	assert.Equal(t, false, metadata["hasUpdate"])
	w = nativePluginCall(handler, "GET", "/api/plugins/native-test-panel/settings", "", app.Token)
	require.Equal(t, 200, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &metadata))
	assert.Equal(t, "missing", metadata["signature"])
	assert.Equal(t, false, metadata["enabled"])
	// An actual tampered registered package produces an actual native error.
	require.NoError(t, os.WriteFile(filepath.Join(app.Plugins.Root, "grafana-clock-panel", "module.js"), []byte("tampered"), 0600))
	w = nativePluginCall(handler, "GET", "/api/plugins/errors", "", app.Token)
	require.Equal(t, 200, w.Code)
	assert.JSONEq(t, `[{"errorCode":"signatureModified","pluginId":"grafana-clock-panel","pluginType":"panel"}]`, w.Body.String())
	w = nativePluginCall(handler, "GET", "/api/plugins/grafana-clock-panel/settings", "", app.Token)
	require.Equal(t, 200, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &metadata))
	assert.Equal(t, "modified", metadata["signature"])
	assert.Equal(t, 500, nativePluginCall(handler, "GET", "/api/plugins/grafana-clock-panel/markdown/README", "", app.Token).Code)
}
