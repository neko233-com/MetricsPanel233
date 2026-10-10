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

func TestAgentTemplatesRenderWithoutReplacingSourceDefinitions(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "")
	s, err := store.Open(filepath.Join(t.TempDir(), "templates.db"))
	require.NoError(t, err)
	app := server.New(s, "", 30)
	host := httptest.NewServer(app.Handler())
	defer func() { host.Close(); app.Plugins.Close(); app.Live.Close(); s.DB.Close() }()
	file := filepath.Join(t.TempDir(), "template.json")
	body := `{"uid":"agent-template","title":"Agent template","expr":"vector(233)","labels":{"severity":"{{ if gt $value 200.0 }}critical{{ else }}warning{{ end }}"},"annotations":{"summary":"{{ $labels.alertname }} has {{ $values.A.Value }} requests"}}`
	require.NoError(t, os.WriteFile(file, []byte(body), 0600))
	_, err = capture(t, "alerts", "save", "--file", file, "--server", host.URL)
	require.NoError(t, err)
	raw, err := capture(t, "alerts", "evaluate", "--id", "agent-template", "--server", host.URL)
	require.NoError(t, err)
	var view model.AlertRuleView
	require.NoError(t, json.Unmarshal(raw, &view))
	require.Len(t, view.Runtime.Instances, 1)
	assert.Equal(t, "critical", view.Runtime.Instances[0].Labels["severity"])
	assert.Equal(t, "Agent template has 233 requests", view.Runtime.Instances[0].Annotations["summary"])
	assert.Contains(t, view.Annotations["summary"], "{{ $labels")
	raw, err = capture(t, "alerts", "history", "--id", "agent-template", "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Agent template has 233 requests")
}

func TestBadTemplateRootURLFailsBeforeCreatingDatabase(t *testing.T) {
	for _, root := range []string{"javascript:alert(1)", "/relative", "https://user:password@example.test", "https://example.test/?secret=233"} {
		path := filepath.Join(t.TempDir(), "must-not-exist.db")
		err := serve([]string{"--root-url", root, "--db", path})
		require.ErrorContains(t, err, "root-url")
		_, err = os.Stat(path)
		require.True(t, os.IsNotExist(err))
	}
}
