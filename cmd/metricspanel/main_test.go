package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPatternCLIJSONAndIdempotentCapture(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "")
	s, err := store.Open(filepath.Join(t.TempDir(), "patterns.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	httpServer := httptest.NewServer(server.New(s, "", 30).Handler())
	defer httpServer.Close()
	start := time.Now().Add(-10 * time.Minute).UnixMilli()
	end := start + 320000
	samples := []model.Sample{}
	for i := 0; i < 64; i++ {
		for _, host := range []string{"go", "mysql"} {
			value := float64(i)
			if host == "mysql" {
				value = value*20 + 233
			}
			samples = append(samples, model.Sample{Name: "cli_vectors", Labels: map[string]string{"host": host}, Timestamp: start + int64(i)*5000, Value: value})
		}
	}
	require.NoError(t, s.Ingest(context.Background(), samples))
	args := []string{"patterns", "capture", "--metric", "cli_vectors", "--start", strconv.FormatInt(start, 10), "--end", strconv.FormatInt(end, 10), "--server", httpServer.URL}
	data, err := capture(t, args...)
	require.NoError(t, err)
	var response struct {
		Patterns []model.Pattern `json:"patterns"`
	}
	require.NoError(t, json.Unmarshal(data, &response))
	require.Len(t, response.Patterns, 2)
	id := response.Patterns[0].ID
	_, err = capture(t, args...)
	require.NoError(t, err)
	data, err = capture(t, "patterns", "list", "--server", httpServer.URL)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &response))
	require.Len(t, response.Patterns, 2)
	data, err = capture(t, "patterns", "get", "--id", id, "--server", httpServer.URL)
	require.NoError(t, err)
	var pattern model.Pattern
	require.NoError(t, json.Unmarshal(data, &pattern))
	require.Len(t, pattern.Values, 64)
	data, err = capture(t, "patterns", "search", "--id", id, "--exact", "--server", httpServer.URL)
	require.NoError(t, err)
	assert.Contains(t, string(data), "sqlite-exact")
	data, err = capture(t, "schema")
	require.NoError(t, err)
	assert.Contains(t, string(data), "pattern_analysis")
	_, err = capture(t, "patterns", "capture", "--metric", "cli_vectors", "--range", "1s", "--server", httpServer.URL)
	require.Error(t, err)
	_, err = capture(t, "patterns", "delete", "--id", id, "--server", httpServer.URL)
	require.NoError(t, err)
}

func TestPluginAndDatasourceCLIJSONLifecycle(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "")
	s, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	srv := server.New(s, "", 30)
	defer srv.Plugins.Close()
	h := httptest.NewServer(srv.Handler())
	defer h.Close()
	args := []string{"plugins", "install", "--file", "../../internal/plugins/testdata/grafana-clock-panel-3.2.4.zip", "--server", h.URL}
	first, err := capture(t, args...)
	require.NoError(t, err)
	second, err := capture(t, args...)
	require.NoError(t, err)
	assert.JSONEq(t, string(first), string(second))
	for _, action := range []string{"get", "disable", "enable", "delete"} {
		result, err := capture(t, "plugins", action, "--id", "grafana-clock-panel", "--server", h.URL)
		require.NoError(t, err)
		require.True(t, json.Valid(result))
	}
	file := filepath.Join(t.TempDir(), "datasource.json")
	require.NoError(t, os.WriteFile(file, []byte(`{"uid":"agent-prom","name":"Agent datasource","type":"prometheus","url":"http://localhost:9090"}`), 0600))
	result, err := capture(t, "datasources", "save", "--file", file, "--server", h.URL)
	require.NoError(t, err)
	assert.Contains(t, string(result), "agent-prom")
	for _, action := range []string{"get", "delete"} {
		_, err = capture(t, "datasources", action, "--id", "agent-prom", "--server", h.URL)
		require.NoError(t, err)
	}
	result, err = capture(t, "datasources", "health", "--id", "metricspanel", "--server", h.URL)
	require.NoError(t, err)
	assert.Contains(t, string(result), "OK")
	result, err = capture(t, "schema")
	require.NoError(t, err)
	assert.Contains(t, string(result), "plugins install --file")
	assert.Contains(t, string(result), "/api/ds/query")
}

func capture(t *testing.T, args ...string) (json.RawMessage, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = old; r.Close() }()
	type captured struct {
		data []byte
		err  error
	}
	readDone := make(chan captured, 1)
	go func() { data, err := io.ReadAll(r); readDone <- captured{data, err} }()
	err = run(args)
	w.Close()
	result := <-readDone
	data := result.data
	require.NoError(t, result.err)
	if err == nil {
		require.True(t, json.Valid(data), string(data))
	}
	return data, err
}
func TestAlertCLIJSONRuleLifecycle(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "")
	s, err := store.Open(filepath.Join(t.TempDir(), "alerts.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	h := httptest.NewServer(server.New(s, "", 30).Handler())
	defer h.Close()
	file := filepath.Join(t.TempDir(), "rule.json")
	require.NoError(t, os.WriteFile(file, []byte(`{"uid":"cli-alert","title":"Agent alert","expr":"vector(1)","condition":"nonzero"}`), 0600))
	data, err := capture(t, "alerts", "save", "--file", file, "--server", h.URL)
	require.NoError(t, err)
	assert.Contains(t, string(data), "cli-alert")
	for _, action := range []string{"get", "evaluate", "history"} {
		data, err = capture(t, "alerts", action, "--id", "cli-alert", "--server", h.URL)
		require.NoError(t, err)
		assert.Contains(t, string(data), "cli-alert")
	}
	data, err = capture(t, "alerts", "list", "--server", h.URL)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Firing")
	_, err = capture(t, "alerts", "delete", "--id", "cli-alert", "--server", h.URL)
	require.NoError(t, err)
	_, err = capture(t, "alerts", "get", "--id", "cli-alert", "--server", h.URL)
	require.Error(t, err)
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
