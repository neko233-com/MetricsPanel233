package main

import (
	"context"
	"encoding/json"
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

func TestAnnotationCLIJSONRetryPatchAndDelete(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "")
	s, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	srv := server.New(s, "", 30)
	defer srv.Plugins.Close()
	defer srv.Live.Close()
	h := httptest.NewServer(srv.Handler())
	defer h.Close()
	file := filepath.Join(t.TempDir(), "annotation.json")
	require.NoError(t, os.WriteFile(file, []byte(`{"time":1000,"text":"agent deploy","tags":["agent"],"idempotencyKey":"cli-deploy"}`), 0600))
	data, err := capture(t, "annotations", "save", "--file", file, "--server", h.URL)
	require.NoError(t, err)
	var created struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(data, &created))
	id := strconv.FormatInt(created.ID, 10)
	repeated, err := capture(t, "annotations", "save", "--file", file, "--server", h.URL)
	require.NoError(t, err)
	assert.JSONEq(t, string(data), string(repeated))
	data, err = capture(t, "annotations", "list", "--tags", `["agent"]`, "--start", "900", "--end", "1100", "--server", h.URL)
	require.NoError(t, err)
	assert.Contains(t, string(data), "agent deploy")
	require.NoError(t, os.WriteFile(file, []byte(`{"text":"edited"}`), 0600))
	_, err = capture(t, "annotations", "patch", "--id", id, "--file", file, "--server", h.URL)
	require.NoError(t, err)
	data, err = capture(t, "annotations", "get", "--id", id, "--server", h.URL)
	require.NoError(t, err)
	assert.Contains(t, string(data), "edited")
	_, err = capture(t, "annotations", "delete", "--id", id, "--server", h.URL)
	require.NoError(t, err)
	_, err = capture(t, "annotations", "get", "--id", id, "--server", h.URL)
	require.Error(t, err)
	view, err := s.SaveAlertRule(context.Background(), model.AlertRule{UID: "cli-alert", Title: "CLI alert", Expr: "vector(1)"})
	require.NoError(t, err)
	_, err = srv.Alerts.Evaluate(context.Background(), view.UID, time.Now())
	require.NoError(t, err)
	data, err = capture(t, "annotations", "list", "--type", "alert", "--alert-uid", view.UID, "--server", h.URL)
	require.NoError(t, err)
	var events []model.Annotation
	require.NoError(t, json.Unmarshal(data, &events))
	require.Len(t, events, 1)
	assert.Equal(t, "Alerting", events[0].NewState)
	data, err = capture(t, "annotations", "list", "--alert-id", strconv.FormatInt(events[0].AlertID, 10), "--server", h.URL)
	require.NoError(t, err)
	assert.Contains(t, string(data), view.UID)
	_, err = capture(t, "annotations", "tags", "--type", "alert", "--name", "alertname:", "--server", h.URL)
	require.NoError(t, err)
}
