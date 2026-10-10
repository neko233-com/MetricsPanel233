package main

import (
	"context"
	"encoding/json"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentPreviewGraphsRulesErrorsAuthenticationAndNoPersistence(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "")
	s, err := store.Open(filepath.Join(t.TempDir(), "preview.db"))
	require.NoError(t, err)
	app := server.New(s, "agent-preview-token-233", 30)
	host := httptest.NewServer(app.Handler())
	t.Cleanup(func() { host.Close(); app.Plugins.Close(); app.Live.Close(); s.DB.Close() })
	file := filepath.Join(t.TempDir(), "graph.json")
	graph := `{"condition":"C","data":[{"refId":"A","datasourceUid":"metricspanel","relativeTimeRange":{"from":120,"to":5},"model":{"expr":"vector(233)","instant":true}},{"refId":"C","datasourceUid":"__expr__","model":{"type":"math","expression":"$A * 2 > 400"}}]}`
	for _, body := range []string{graph, `{"uid":"unsaved","grafana":` + graph + `,"runtime":{"state":"Firing"}}`} {
		require.NoError(t, os.WriteFile(file, []byte(body), 0600))
		_, err = capture(t, "alerts", "preview", "--file", file, "--server", host.URL)
		require.Error(t, err)
		raw, err := capture(t, "alerts", "preview", "--file", file, "--at", "1600000", "--token", "agent-preview-token-233", "--server", host.URL)
		require.NoError(t, err)
		var result struct {
			At     int64 `json:"at"`
			Values []struct {
				Value     float64 `json:"value"`
				Satisfied bool    `json:"satisfied"`
			} `json:"condition_values"`
		}
		require.NoError(t, json.Unmarshal(raw, &result))
		assert.Equal(t, int64(1600000), result.At)
		require.Len(t, result.Values, 1)
		assert.Equal(t, 1.0, result.Values[0].Value)
		assert.True(t, result.Values[0].Satisfied)
	}
	failed := `{"condition":"C","data":[{"refId":"A","datasourceUid":"metricspanel","relativeTimeRange":{"from":60,"to":0},"model":{"expr":"vector(233)","instant":false,"intervalMs":1000}},{"refId":"C","datasourceUid":"__expr__","model":{"type":"math","expression":"$A * 2"}}]}`
	require.NoError(t, os.WriteFile(file, []byte(failed), 0600))
	raw, err := capture(t, "alerts", "preview", "--file", file, "--token", "agent-preview-token-233", "--server", host.URL)
	require.Error(t, err, string(raw))
	assert.Contains(t, string(raw), `"condition_error"`)
	assert.Contains(t, string(raw), `"results"`)
	assert.True(t, json.Valid(raw))
	for _, args := range [][]string{{"alerts", "preview", "--at", "-1"}, {"alerts", "preview", "--at", "253402300800000"}, {"alerts", "list", "--at", "233"}} {
		_, err = capture(t, args...)
		require.Error(t, err)
	}
	for _, invalid := range []string{`null`, `[]`, `{`, `{"grafana":null}`} {
		require.NoError(t, os.WriteFile(file, []byte(invalid), 0600))
		_, err = capture(t, "alerts", "preview", "--file", file, "--token", "agent-preview-token-233", "--server", host.URL)
		require.Error(t, err)
	}
	rules, err := s.AlertRules(context.Background())
	require.NoError(t, err)
	assert.Empty(t, rules)
	history, err := s.AlertHistory(context.Background(), "", 100)
	require.NoError(t, err)
	assert.Empty(t, history)
	metrics, err := s.Metrics(context.Background())
	require.NoError(t, err)
	assert.Empty(t, metrics)
	schemaRaw, err := capture(t, "schema")
	require.NoError(t, err)
	assert.Contains(t, string(schemaRaw), "/api/v1/alerts/preview")
	assert.Contains(t, string(schemaRaw), "alerts preview")
}
