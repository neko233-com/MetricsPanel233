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
