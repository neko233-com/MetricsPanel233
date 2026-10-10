package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentImportsEvaluatesAndUpdatesPersistentGrafanaGraph(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "")
	s, err := store.Open(filepath.Join(t.TempDir(), "graph.db"))
	require.NoError(t, err)
	app := server.New(s, "", 30)
	host := httptest.NewServer(app.Handler())
	defer func() { host.Close(); app.Plugins.Close(); app.Live.Close(); s.DB.Close() }()
	file := filepath.Join(t.TempDir(), "graph.json")
	body := `{"uid":"agent-graph","title":"Agent graph","condition":"C","data":[{"refId":"A","datasourceUid":"__expr__","model":{"type":"math","expression":"233"}},{"refId":"B","datasourceUid":"__expr__","model":{"type":"math","expression":"2"}},{"refId":"C","datasourceUid":"__expr__","model":{"type":"math","expression":"$A*$B>400"}}]}`
	require.NoError(t, os.WriteFile(file, []byte(body), 0600))
	_, err = capture(t, "alerts", "import-grafana", "--file", file, "--server", host.URL)
	require.NoError(t, err)
	raw, err := capture(t, "alerts", "evaluate", "--id", "agent-graph", "--server", host.URL)
	require.NoError(t, err)
	var view model.AlertRuleView
	require.NoError(t, json.Unmarshal(raw, &view))
	assert.Equal(t, "grafana", view.Execution)
	assert.Empty(t, view.Expr)
	require.Len(t, view.Runtime.Instances, 1)
	assert.Equal(t, "Firing", view.Runtime.Instances[0].State)
	view.Title = "Updated by agent"
	raw, err = json.Marshal(view)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, raw, 0600))
	raw, err = capture(t, "alerts", "save", "--id", view.UID, "--file", file, "--server", host.URL)
	require.NoError(t, err)
	var updated model.AlertRuleView
	require.NoError(t, json.Unmarshal(raw, &updated))
	assert.Equal(t, "grafana", updated.Execution)
	assert.NotEmpty(t, updated.Grafana)
	_, err = capture(t, "alerts", "save", "--id", view.UID, "--file", file, "--server", host.URL)
	require.Error(t, err)
	raw, err = capture(t, "alerts", "get", "--id", view.UID, "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Updated by agent")
	_, err = capture(t, "alerts", "delete", "--id", view.UID, "--server", host.URL)
	require.NoError(t, err)
}
