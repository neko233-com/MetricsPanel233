package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/alerting"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentRuleGroupSaveGetIntervalUpdateRoundTripAndDelete(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "")
	s, err := store.Open(filepath.Join(t.TempDir(), "groups.db"))
	require.NoError(t, err)
	app := server.New(s, "", 30)
	host := httptest.NewServer(app.Handler())
	defer func() { host.Close(); app.Plugins.Close(); app.Live.Close(); s.DB.Close() }()
	file := filepath.Join(t.TempDir(), "group.json")
	body := `{"interval":30,"rules":[{"uid":"agent-group","title":"Agent group","condition":"C","data":[{"refId":"C","datasourceUid":"__expr__","model":{"type":"math","expression":"233"}}]}]}`
	require.NoError(t, os.WriteFile(file, []byte(body), 0600))
	args := []string{"--folder-uid", "general", "--group", "agent group / 中文", "--server", host.URL}
	raw, err := capture(t, append([]string{"alerts", "group-save", "--file", file, "--disable-provenance"}, args...)...)
	require.NoError(t, err)
	var group alerting.GrafanaGroup
	require.NoError(t, json.Unmarshal(raw, &group))
	assert.Equal(t, "agent group / 中文", group.Title)
	require.Len(t, group.Rules, 1)
	assert.Empty(t, group.Rules[0].Provenance)
	raw, err = capture(t, append([]string{"alerts", "group-get"}, args...)...)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, raw, 0600))
	_, err = capture(t, append([]string{"alerts", "group-save", "--file", file, "--disable-provenance"}, args...)...)
	require.NoError(t, err)
	view, err := s.AlertRule(t.Context(), "agent-group")
	require.NoError(t, err)
	assert.Equal(t, 1, view.Version, "resubmitted GET must be idempotent")
	require.NoError(t, os.WriteFile(file, []byte(`{"interval":60}`), 0600))
	_, err = capture(t, append([]string{"alerts", "group-save", "--file", file, "--disable-provenance"}, args...)...)
	require.NoError(t, err)
	view, err = s.AlertRule(t.Context(), "agent-group")
	require.NoError(t, err)
	assert.Equal(t, 60, view.IntervalSeconds)
	_, err = capture(t, append([]string{"alerts", "group-delete"}, args...)...)
	require.NoError(t, err)
	_, err = capture(t, append([]string{"alerts", "group-get"}, args...)...)
	require.Error(t, err)
	_, err = capture(t, "alerts", "group-save", "--file", file, "--server", host.URL)
	require.ErrorContains(t, err, "--folder-uid")
}
