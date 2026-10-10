package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentRecoveryEvaluationSurvivesReopenedDatabase(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "")
	dir := t.TempDir()
	db := filepath.Join(dir, "metrics.db")
	s, err := store.Open(db)
	require.NoError(t, err)
	app := server.New(s, "", 30)
	n := float64(101)
	query := func(context.Context, string, []backend.DataQuery) (backend.Responses, string, error) {
		return backend.Responses{"A": {Frames: data.Frames{data.NewFrame("", data.NewField("A", data.Labels{"host": "agent"}, []float64{n}))}}}, "backend", nil
	}
	app.Alerts.GraphSource = query
	host := httptest.NewServer(app.Handler())
	t.Cleanup(func() { host.Close(); app.Plugins.Close(); app.Live.Close(); s.DB.Close() })
	file := filepath.Join(dir, "rule.json")
	body := `{"uid":"agent-recovery","title":"Agent recovery","execution":"grafana","grafana":{"condition":"C","data":[{"refId":"A","datasourceUid":"metricspanel","model":{"expr":"vector(101)","instant":true}},{"refId":"C","datasourceUid":"__expr__","model":{"type":"threshold","expression":"A","conditions":[{"evaluator":{"type":"gt","params":[100]},"unloadEvaluator":{"type":"lt","params":[80]}}]}}]}}`
	require.NoError(t, os.WriteFile(file, []byte(body), 0600))
	_, err = capture(t, "alerts", "save", "--file", file, "--server", host.URL)
	require.NoError(t, err)
	raw, err := capture(t, "alerts", "evaluate", "--id", "agent-recovery", "--server", host.URL)
	require.NoError(t, err)
	var first model.AlertRuleView
	require.NoError(t, json.Unmarshal(raw, &first))
	require.Len(t, first.Runtime.Instances, 1)
	assert.Equal(t, "Firing", first.Runtime.Instances[0].State)
	assert.NotEmpty(t, first.Runtime.Instances[0].ResultFingerprint)
	host.Close()
	app.Plugins.Close()
	app.Live.Close()
	require.NoError(t, s.DB.Close())
	s, err = store.Open(db)
	require.NoError(t, err)
	app = server.New(s, "", 30)
	app.Alerts.GraphSource = query
	host = httptest.NewServer(app.Handler())
	n = 90
	// The public evaluate endpoint uses millisecond timestamps and rejects duplicates.
	time.Sleep(time.Until(time.UnixMilli(first.Runtime.LastEvaluation + 2)))
	raw, err = capture(t, "alerts", "evaluate", "--id", "agent-recovery", "--server", host.URL)
	require.NoError(t, err)
	var next model.AlertRuleView
	require.NoError(t, json.Unmarshal(raw, &next))
	require.Len(t, next.Runtime.Instances, 1)
	assert.Equal(t, "Firing", next.Runtime.Instances[0].State)
	assert.Equal(t, first.Runtime.Instances[0].ResultFingerprint, next.Runtime.Instances[0].ResultFingerprint)
	assert.Contains(t, string(next.Grafana), `"unloadEvaluator"`)
}
