package main

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func capture(t *testing.T, args ...string) (json.RawMessage, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = old; r.Close() }()
	err = run(args)
	w.Close()
	data, readErr := io.ReadAll(r)
	require.NoError(t, readErr)
	if err == nil {
		require.True(t, json.Valid(data), string(data))
	}
	return data, err
}
func TestCLIJSONContractsAndCommands(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	defer s.DB.Close()
	httpServer := httptest.NewServer(server.New(s, "", 30).Handler())
	defer httpServer.Close()
	for _, command := range []string{"health", "stats", "metrics"} {
		data, err := capture(t, command, "--server", httpServer.URL)
		require.NoError(t, err)
		require.True(t, json.Valid(data))
	}
	data, err := capture(t, "schema")
	require.NoError(t, err)
	assert.Contains(t, string(data), "ingest_example")
	data, err = capture(t, "version")
	require.NoError(t, err)
	assert.Contains(t, string(data), "0.1.0")
	file := filepath.Join(t.TempDir(), "samples.json")
	require.NoError(t, os.WriteFile(file, []byte(`{"samples":[{"name":"cli_orders","value":233}]}`), 0600))
	data, err = capture(t, "ingest", "--file", file, "--server", httpServer.URL)
	require.NoError(t, err)
	assert.Contains(t, string(data), "accepted")
	data, err = capture(t, "query", "--metric", "cli_orders", "--server", httpServer.URL)
	require.NoError(t, err)
	assert.Contains(t, string(data), "233")
	data, err = capture(t, "query", "--expr", "sum(cli_orders)", "--server", httpServer.URL)
	require.NoError(t, err)
	assert.Contains(t, string(data), "matrix")
	data, err = capture(t, "targets", "add", "--name", "mysql", "--url", "http://localhost:9104/metrics", "--interval", "5s", "--server", httpServer.URL)
	require.NoError(t, err)
	assert.Contains(t, string(data), "mysql")
	_, err = capture(t, "targets", "list", "--server", httpServer.URL)
	require.NoError(t, err)
	data, err = capture(t, "dashboards", "export", "--id", "system", "--server", httpServer.URL)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, data, 0600))
	_, err = capture(t, "dashboards", "save", "--file", file, "--server", httpServer.URL)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, []byte(`{"title":"CLI Grafana","panels":[{"title":"Orders","type":"timeseries","targets":[{"expr":"sum(cli_orders)"}]}]}`), 0600))
	data, err = capture(t, "dashboards", "save", "--file", file, "--server", httpServer.URL)
	require.NoError(t, err)
	var imported struct {
		Dashboard struct {
			ID string `json:"id"`
		} `json:"dashboard"`
	}
	require.NoError(t, json.Unmarshal(data, &imported))
	data, err = capture(t, "dashboards", "export", "--id", imported.Dashboard.ID, "--format", "grafana", "--server", httpServer.URL)
	require.NoError(t, err)
	assert.Contains(t, string(data), "CLI Grafana")
	for _, args := range [][]string{{"query"}, {"targets", "add", "--url", "file:///secret"}, {"targets", "delete"}, {"dashboards", "delete"}, {"unknown"}, {"query", "--bad-flag"}} {
		_, err = capture(t, args...)
		require.Error(t, err, args)
	}
}
func TestServeRejectsUnsafeAndInvalidSettingsBeforeBinding(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "")
	for _, args := range [][]string{{"--listen", "0.0.0.0:7333"}, {"--retention-days", "0"}, {"--listen", "bad-address"}, {"--storage", "wrong", "--db", filepath.Join(t.TempDir(), "db")}} {
		require.Error(t, serve(args))
	}
}
