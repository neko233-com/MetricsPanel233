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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentDashboardRevisionRejectsStaleSaves(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "")
	s, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	app := server.New(s, "", 30)
	host := httptest.NewServer(app.Handler())
	t.Cleanup(func() { host.Close(); app.Plugins.Close(); app.Live.Close(); s.DB.Close() })
	d, err := s.SaveDashboard(context.Background(), model.Dashboard{ID: "agent", Name: "Before", Panels: []model.Panel{}})
	require.NoError(t, err)
	d.Name = "After"
	file := filepath.Join(t.TempDir(), "draft.json")
	body, err := json.Marshal(d)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, body, 0600))
	args := []string{"dashboards", "save", "--file", file, "--revision", strconv.FormatInt(d.UpdatedAt, 10), "--server", host.URL}
	raw, err := capture(t, args...)
	require.NoError(t, err)
	var saved model.Dashboard
	require.NoError(t, json.Unmarshal(raw, &saved))
	assert.Equal(t, "After", saved.Name)
	assert.Greater(t, saved.UpdatedAt, d.UpdatedAt)
	baseline, err := s.Dashboards(context.Background())
	require.NoError(t, err)
	_, err = capture(t, args...)
	require.ErrorContains(t, err, "HTTP 409")
	_, err = capture(t, "dashboards", "list", "--revision", "1", "--server", host.URL)
	require.ErrorContains(t, err, "only supported")
	_, err = capture(t, "dashboards", "save", "--file", file, "--revision", "-1", "--server", host.URL)
	require.Error(t, err)
	list, err := s.Dashboards(context.Background())
	require.NoError(t, err)
	assert.Equal(t, baseline, list)
}
