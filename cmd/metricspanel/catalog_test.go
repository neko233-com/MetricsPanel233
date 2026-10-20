package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogCLIQueriesAndBindings(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "catalog-token")
	var method, path, query string
	var payload map[string]any
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer catalog-token", r.Header.Get("Authorization"))
		method, path, query = r.Method, r.URL.Path, r.URL.RawQuery
		if r.Body != nil && r.Method == "POST" {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"opaque":"remote result"}`))
	}))
	defer host.Close()
	commands := []struct {
		args                []string
		method, path, query string
	}{
		{[]string{"plugins", "search", "--query", "clock & time", "--kind", "panel"}, "GET", "/api/v1/plugins/catalog", "q=clock+%26+time&type=panel"},
		{[]string{"plugins", "search"}, "GET", "/api/v1/plugins/catalog", "q=&type="},
		{[]string{"plugins", "info", "--id", "grafana-clock-panel"}, "GET", "/api/v1/plugin-catalog/grafana-clock-panel", ""},
		{[]string{"plugins", "versions", "--id", "grafana-clock-panel"}, "GET", "/api/v1/plugin-catalog/grafana-clock-panel/versions", ""},
		{[]string{"dashboards", "community", "--url", "https://grafana.com/api/dashboards/1860/revisions/42/download"}, "GET", "/api/v1/dashboards/community/1860", "revision=42"},
		{[]string{"dashboards", "import-community", "--id", "1860", "--community-revision", "42", "--name", "Company nodes", "--uid", "company-nodes", "--inputs", `{"ds_prometheus":"remote-prom"}`}, "POST", "/api/v1/import/grafana-community", ""},
	}
	for _, command := range commands {
		raw, err := capture(t, append(command.args, "--server", host.URL)...)
		require.NoError(t, err)
		assert.JSONEq(t, `{"items":[],"opaque":"remote result"}`, string(raw))
		assert.Equal(t, command.method, method)
		assert.Equal(t, command.path, path)
		assert.Equal(t, command.query, query)
	}
	assert.Equal(t, float64(1860), payload["id"])
	assert.Equal(t, float64(42), payload["revision"])
	assert.Equal(t, "company-nodes", payload["uid"])
	assert.Equal(t, map[string]any{"ds_prometheus": "remote-prom"}, payload["inputs"])
	file := filepath.Join(t.TempDir(), "dashboard.json")
	require.NoError(t, os.WriteFile(file, []byte(`{"title":"Bound","panels":[],"opaque":{"future":233}}`), 0600))
	for _, action := range []string{"preview-grafana", "import-grafana"} {
		_, err := capture(t, "dashboards", action, "--file", file, "--inputs", `{"DS_PROM":"metricspanel"}`, "--server", host.URL)
		require.NoError(t, err)
		assert.Equal(t, "POST", method)
		assert.Equal(t, float64(233), payload["dashboard"].(map[string]any)["opaque"].(map[string]any)["future"])
	}
}

func TestCatalogCLIRejectsInvalidArgumentsBeforeHTTP(t *testing.T) {
	for _, args := range [][]string{
		{"plugins", "search", "--kind", "mysql"}, {"plugins", "info"}, {"plugins", "versions", "--id", "../clock"},
		{"dashboards", "community", "--url", "https://localhost/dashboard/1860"},
		{"dashboards", "community", "--id", "1860", "--url", "https://grafana.com/grafana/dashboards/1860/"},
		{"dashboards", "import-community", "--id", "1860", "--community-revision", "-1"},
		{"dashboards", "import-community", "--id", "1860", "--inputs", `{"DS_PROM":233}`},
	} {
		_, err := capture(t, append(args, "--server", "http://127.0.0.1:1")...)
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "connect:")
	}
	raw, err := capture(t, "schema")
	require.NoError(t, err)
	assert.Contains(t, string(raw), "plugin_catalog")
	assert.Contains(t, string(raw), "community_dashboards")
	assert.Contains(t, string(raw), "plugins versions --id ID")
	assert.Contains(t, string(raw), "dashboards preview-grafana --file")
	assert.Contains(t, string(raw), "/api/v1/write")
	assert.Contains(t, string(raw), "/prometheus/api/v1/write")
	assert.Contains(t, string(raw), "ingest_modes")
}
