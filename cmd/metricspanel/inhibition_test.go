package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/require"
)

func TestAgentInhibitionLifecycleUsesRealActiveSources(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "")
	db, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	app := server.New(db, "", 30)
	host := httptest.NewServer(app.Handler())
	t.Cleanup(func() { host.Close(); app.Live.Close(); app.Plugins.Close(); db.DB.Close() })
	file := func(name, body string) string {
		path := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.WriteFile(path, []byte(body), 0600))
		return path
	}
	for _, alert := range []struct{ uid, severity string }{{"source", "critical"}, {"target", "warning"}} {
		body := `{"uid":"` + alert.uid + `","title":"` + alert.uid + `","expr":"vector(233)","labels":{"service":"api","severity":"` + alert.severity + `"}}`
		_, err = capture(t, "alerts", "save", "--file", file(alert.uid+".json", body), "--server", host.URL)
		require.NoError(t, err)
		_, err = capture(t, "alerts", "evaluate", "--id", alert.uid, "--server", host.URL)
		require.NoError(t, err)
	}
	definition := `{"name":"Critical suppresses warning","source_matchers":[{"name":"severity","operator":"=","value":"critical"}],"target_matchers":[{"name":"severity","operator":"=","value":"warning"}],"equal":["service"]}`
	ruleFile := file("inhibition.json", definition)
	raw, err := capture(t, "inhibition-rules", "save", "--file", ruleFile, "--server", host.URL)
	require.NoError(t, err)
	var saved model.InhibitionRule
	require.NoError(t, json.Unmarshal(raw, &saved))
	require.NotEmpty(t, saved.UID)
	require.Equal(t, 1, saved.Version)
	raw, err = capture(t, "inhibition-rules", "get", "--id", saved.UID, "--server", host.URL)
	require.NoError(t, err)
	require.Contains(t, string(raw), saved.Name)
	raw, err = capture(t, "inhibition-rules", "list", "--server", host.URL)
	require.NoError(t, err)
	require.Contains(t, string(raw), saved.UID)
	raw, err = capture(t, "inhibition-rules", "active", "--limit", "1", "--server", host.URL)
	require.NoError(t, err)
	var active model.InhibitionActiveView
	require.NoError(t, json.Unmarshal(raw, &active))
	require.Equal(t, 2, active.Total)
	require.Len(t, active.Instances, 1)
	require.True(t, active.Truncated)
	previewFile := file("preview.json", `{"source_matchers":[{"name":"severity","operator":"=","value":"critical"}],"target_matchers":[{"name":"severity","operator":"=","value":"warning"}],"equal":["service"],"labels":{"service":"api","severity":"warning"}}`)
	raw, err = capture(t, "inhibition-rules", "preview", "--file", previewFile, "--server", host.URL)
	require.NoError(t, err)
	var preview model.InhibitionPreview
	require.NoError(t, json.Unmarshal(raw, &preview))
	require.True(t, preview.Inhibited)
	require.Equal(t, 1, preview.SourceCount)
	require.Equal(t, "source", preview.Sources[0].RuleUID)
	_, err = capture(t, "inhibition-rules", "save", "--id", saved.UID, "--file", ruleFile, "--server", host.URL)
	require.ErrorContains(t, err, "HTTP 409")
	raw, err = capture(t, "inhibition-rules", "save", "--id", saved.UID, "--version", "1", "--file", ruleFile, "--server", host.URL)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &saved))
	require.Equal(t, 2, saved.Version)
	_, err = capture(t, "inhibition-rules", "delete", "--id", saved.UID, "--version", "1", "--server", host.URL)
	require.ErrorContains(t, err, "HTTP 409")
	_, err = capture(t, "inhibition-rules", "delete", "--id", saved.UID, "--server", host.URL)
	require.ErrorContains(t, err, "positive --version")
	_, err = capture(t, "inhibition-rules", "delete", "--id", saved.UID, "--version", strconv.Itoa(saved.Version), "--server", host.URL)
	require.NoError(t, err)
	_, err = capture(t, "inhibition-rules", "get", "--id", saved.UID, "--server", host.URL)
	require.ErrorContains(t, err, "HTTP 404")
	_, err = capture(t, "inhibition-rules", "active", "--limit", "201", "--server", host.URL)
	require.ErrorContains(t, err, "1 to 200")
	_, err = capture(t, "inhibition-rules", "save", "--id", "different", "--file", file("mismatch.json", `{"uid":"other","name":"mismatch"}`), "--server", host.URL)
	require.ErrorContains(t, err, "does not match --id")
	_, err = capture(t, "inhibition-rules", "save", "--id", "different", "--version", "2", "--file", file("version.json", `{"version":1}`), "--server", host.URL)
	require.ErrorContains(t, err, "does not match --version")
}
