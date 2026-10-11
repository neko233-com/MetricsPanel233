package main

import (
	"context"
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

func TestDashboardHistoryCLIUsesRealServerAndExactJSON(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "history")
	s, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	srv := server.New(s, "history", 30)
	defer srv.Plugins.Close()
	h := httptest.NewServer(srv.Handler())
	defer h.Close()
	path := filepath.Join(t.TempDir(), "native.json")
	raw := []byte(`{"id":"cli-history","name":"Original","panels":[{"id":"p","title":"Metric","metric":"up","aggregation":"last","config":{"type":"stat","id":1,"opaque":9007199254740993}}]}`)
	require.NoError(t, os.WriteFile(path, raw, 0600))
	note := "Initial 保存\nfrom CLI"
	_, err = capture(t, "dashboards", "save", "--file", path, "--message", note, "--server", h.URL)
	require.NoError(t, err)
	first, err := s.Dashboard(context.Background(), "cli-history")
	require.NoError(t, err)
	first.Name = "Changed"
	first.Panels[0].Config = json.RawMessage(`{"type":"stat","id":1,"opaque":9007199254740994}`)
	raw, err = json.Marshal(first)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0600))
	_, err = capture(t, "dashboards", "save", "--file", path, "--revision", strconv.FormatInt(first.UpdatedAt, 10), "--message", "Correction", "--server", h.URL)
	require.NoError(t, err)
	list, err := capture(t, "dashboards", "history", "--id", first.ID, "--limit", "1", "--server", h.URL)
	require.NoError(t, err)
	var page struct {
		Versions      []model.DashboardVersion
		ContinueToken string
	}
	require.NoError(t, json.Unmarshal(list, &page))
	require.Len(t, page.Versions, 1)
	require.Equal(t, "Correction", page.Versions[0].Message)
	require.NotEmpty(t, page.ContinueToken)
	list, err = capture(t, "dashboards", "history", "--id", first.ID, "--continue-token", page.ContinueToken, "--server", h.URL)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(list, &page))
	require.Len(t, page.Versions, 1)
	require.Equal(t, note, page.Versions[0].Message)
	for _, action := range []string{"version", "version-export"} {
		result, err := capture(t, "dashboards", action, "--id", first.ID, "--dashboard-version", "1", "--server", h.URL)
		require.NoError(t, err)
		require.Contains(t, string(result), "9007199254740993")
	}
	diff, err := capture(t, "dashboards", "compare", "--id", first.ID, "--base-version", "1", "--new-version", "2", "--server", h.URL)
	require.NoError(t, err)
	require.Contains(t, string(diff), "9007199254740993")
	require.Contains(t, string(diff), "9007199254740994")
	_, err = capture(t, "dashboards", "restore", "--id", first.ID, "--dashboard-version", "1", "--revision", strconv.FormatInt(first.UpdatedAt, 10), "--server", h.URL)
	require.ErrorContains(t, err, "HTTP 409")
	current, err := s.Dashboard(context.Background(), first.ID)
	require.NoError(t, err)
	_, err = capture(t, "dashboards", "restore", "--id", first.ID, "--dashboard-version", "1", "--revision", strconv.FormatInt(current.UpdatedAt, 10), "--message", "Recovered configuration", "--server", h.URL)
	require.NoError(t, err)
	restored, err := s.Dashboard(context.Background(), first.ID)
	require.NoError(t, err)
	require.Equal(t, first.GrafanaID, restored.GrafanaID)
	require.Equal(t, int64(3), restored.GrafanaVersion)
	require.Equal(t, "Original", restored.Name)
	entry, err := s.DashboardVersion(context.Background(), first.ID, 3)
	require.NoError(t, err)
	require.Equal(t, int64(2), entry.ParentVersion)
	require.Equal(t, int64(1), entry.RestoredFrom)
	require.Equal(t, "Recovered configuration", entry.Message)
	_, err = capture(t, "dashboards", "restore", "--id", first.ID, "--dashboard-version", "1", "--server", h.URL)
	require.ErrorContains(t, err, "HTTP 400")
	for _, args := range [][]string{{"dashboards", "history", "--id", first.ID, "--offset", "-1"}, {"dashboards", "version", "--id", first.ID, "--dashboard-version", "0"}, {"dashboards", "compare", "--id", first.ID, "--base-version", "1"}, {"metrics", "--message", "ignored"}} {
		_, err = capture(t, args...)
		require.Error(t, err)
	}
}
