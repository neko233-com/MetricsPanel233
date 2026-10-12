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

func TestAgentLayoutAndNotificationWorkflows(t *testing.T) {
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
	d, err := db.Dashboard(context.Background(), "system")
	require.NoError(t, err)
	layout := file("layout.json", `{"panels":[{"id":"memory","x":0,"y":0,"w":24,"h":8},{"id":"goroutines","x":0,"y":8,"w":12,"h":8},{"id":"requests","x":12,"y":8,"w":12,"h":8}]}`)
	args := []string{"dashboards", "layout", "--id", "system", "--file", layout, "--revision", strconv.FormatInt(d.UpdatedAt, 10), "--message", "Agent layout", "--server", host.URL}
	raw, err := capture(t, args...)
	require.NoError(t, err)
	var saved model.Dashboard
	require.NoError(t, json.Unmarshal(raw, &saved))
	assert.Greater(t, saved.UpdatedAt, d.UpdatedAt)
	var panel struct {
		Grid struct {
			Width int `json:"w"`
		} `json:"gridPos"`
	}
	require.NoError(t, json.Unmarshal(saved.Panels[0].Config, &panel))
	assert.Equal(t, 24, panel.Grid.Width)
	_, err = capture(t, args...)
	require.ErrorContains(t, err, "HTTP 409")
	_, err = capture(t, "dashboards", "layout", "--id", "system", "--file", layout, "--server", host.URL)
	require.ErrorContains(t, err, "positive --revision")
	panelFile := file("panel.json", `{"action":"save","sourceId":"memory","panel":{"id":"memory","title":"Memory edited","visualization":"stat","metric":"metricspanel_memory_bytes","aggregation":"last","unit":"bytes"}}`)
	raw, err = capture(t, "dashboards", "panels", "--id", "system", "--file", panelFile, "--revision", strconv.FormatInt(saved.UpdatedAt, 10), "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Memory edited")
	require.NoError(t, json.Unmarshal(raw, &saved))
	annotationFile := file("annotation-queries.json", `{"queries":[{"query":{"name":"Operations","enable":true,"type":"dashboard"}}]}`)
	raw, err = capture(t, "dashboards", "annotation-queries", "--id", "system", "--file", annotationFile, "--revision", strconv.FormatInt(saved.UpdatedAt, 10), "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Operations")
	require.NoError(t, json.Unmarshal(raw, &saved))
	variableFile := file("variables.json", `{"variables":[{"variable":{"name":"region","type":"constant","query":"east","current":"east","options":[],"multi":false,"include_all":false}}]}`)
	raw, err = capture(t, "dashboards", "variables", "--id", "system", "--file", variableFile, "--revision", strconv.FormatInt(saved.UpdatedAt, 10), "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"name": "region"`)
	contact := file("contact.json", `{"uid":"agent-hook","name":"Agent hook","url":"http://127.0.0.1:9999/webhook","bearer_token":"credential-must-stay-private"}`)
	raw, err = capture(t, "contacts", "save", "--file", contact, "--server", host.URL)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "credential-must-stay-private")
	assert.Contains(t, string(raw), `"bearer_token": true`)
	var point model.ContactPoint
	require.NoError(t, json.Unmarshal(raw, &point))
	raw, err = capture(t, "contacts", "list", "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "agent-hook")
	assert.NotContains(t, string(raw), "credential-must-stay-private")
	raw, err = capture(t, "notifications", "policy", "--server", host.URL)
	require.NoError(t, err)
	var policy model.NotificationPolicy
	require.NoError(t, json.Unmarshal(raw, &policy))
	policy.Receiver = point.UID
	payload, err := json.Marshal(policy)
	require.NoError(t, err)
	policyFile := file("policy.json", string(payload))
	raw, err = capture(t, "notifications", "policy-save", "--file", policyFile, "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "agent-hook")
	raw, err = capture(t, "contacts", "test", "--id", point.UID, "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "pending")
	raw, err = capture(t, "notifications", "list", "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "agent-hook")
	_, err = capture(t, "notifications", "retry", "--id", "not-a-number", "--server", host.URL)
	require.ErrorContains(t, err, "positive delivery")
	_, err = capture(t, "contacts", "delete", "--id", point.UID, "--server", host.URL)
	require.ErrorContains(t, err, "HTTP 409")
	silence := model.Silence{Matchers: []model.SilenceMatcher{{Name: "service", Value: "api"}}, StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(time.Hour), CreatedBy: "Agent", Comment: "Maintenance"}
	silenceBody, err := json.Marshal(silence)
	require.NoError(t, err)
	silenceFile := file("silence.json", string(silenceBody))
	raw, err = capture(t, "silences", "save", "--file", silenceFile, "--server", host.URL)
	require.NoError(t, err)
	var identity struct {
		ID string `json:"silenceID"`
	}
	require.NoError(t, json.Unmarshal(raw, &identity))
	require.NotEmpty(t, identity.ID)
	raw, err = capture(t, "silences", "list", "--state", "active", "--filter", `service="api"`, "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), identity.ID)
	raw, err = capture(t, "silences", "get", "--id", identity.ID, "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Maintenance")
	preview := file("preview.json", `{"matchers":[{"name":"service","value":"api"}],"labels":{"service":"api"}}`)
	raw, err = capture(t, "silences", "preview", "--file", preview, "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"matches": true`)
	raw, err = capture(t, "silences", "expire", "--id", identity.ID, "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"expired": true`)
	raw, err = capture(t, "silences", "list", "--state", "expired", "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), identity.ID)
	_, err = capture(t, "silences", "list", "--state", "bogus", "--server", host.URL)
	require.ErrorContains(t, err, "active, pending or expired")
	timingFile := file("timing.json", `{"name":"office","time_intervals":[{"times":[{"start_time":"09:00","end_time":"17:00"}],"location":"Asia/Shanghai"}]}`)
	raw, err = capture(t, "mute-timings", "save", "--file", timingFile, "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "office")
	raw, err = capture(t, "mute-timings", "list", "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Asia/Shanghai")
	raw, err = capture(t, "mute-timings", "get", "--id", "office", "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "office")
	at := file("at.json", `{"at":"2026-10-12T10:00:00+08:00"}`)
	raw, err = capture(t, "mute-timings", "preview", "--id", "office", "--file", at, "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"matches": true`)
	raw, err = capture(t, "notifications", "groups", "--server", host.URL)
	require.NoError(t, err)
	raw, err = capture(t, "mute-timings", "delete", "--id", "office", "--server", host.URL)
	require.NoError(t, err)
}

func TestAgentNotificationTemplateLifecycle(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "")
	db, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	app := server.New(db, "", 30)
	host := httptest.NewServer(app.Handler())
	t.Cleanup(func() { host.Close(); app.Live.Close(); app.Plugins.Close(); db.DB.Close() })
	file := filepath.Join(t.TempDir(), "templates.json")
	body := `{"name":"agent-notifications","template":"{{ define \"agent.title\" }}{{ .Status }}: {{ .Receiver }}{{ end }}"}`
	require.NoError(t, os.WriteFile(file, []byte(body), 0600))
	raw, err := capture(t, "notification-templates", "save", "--file", file, "--server", host.URL)
	require.NoError(t, err)
	var saved model.NotificationTemplate
	require.NoError(t, json.Unmarshal(raw, &saved))
	assert.Greater(t, saved.Version, 0)
	raw, err = capture(t, "notification-templates", "get", "--id", saved.Name, "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "agent.title")
	raw, err = capture(t, "notification-templates", "list", "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), saved.Name)
	preview := filepath.Join(t.TempDir(), "preview.json")
	require.NoError(t, os.WriteFile(preview, []byte(`{"name":"agent-notifications","data":{"status":"firing","receiver":"Agent webhook"},"title":"{{ template \"agent.title\" . }}"}`), 0600))
	raw, err = capture(t, "notification-templates", "preview", "--file", preview, "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "firing: Agent webhook")
	require.NoError(t, os.WriteFile(preview, []byte(`{"data":{"status":"firing","receiver":"Agent via ID"},"title":"{{ template \"agent.title\" . }}"}`), 0600))
	raw, err = capture(t, "notification-templates", "preview", "--id", saved.Name, "--file", preview, "--server", host.URL)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "firing: Agent via ID")
	require.NoError(t, os.WriteFile(preview, []byte(`{"name":"another-group","data":{}}`), 0600))
	_, err = capture(t, "notification-templates", "preview", "--id", saved.Name, "--file", preview, "--server", host.URL)
	require.ErrorContains(t, err, "does not match --id")
	_, err = capture(t, "notification-templates", "save", "--id", saved.Name, "--file", file, "--server", host.URL)
	require.ErrorContains(t, err, "HTTP 409")
	_, err = capture(t, "notification-templates", "delete", "--id", saved.Name, "--server", host.URL)
	require.NoError(t, err)
	_, err = capture(t, "notification-templates", "get", "--id", saved.Name, "--server", host.URL)
	require.ErrorContains(t, err, "HTTP 404")
	_, err = capture(t, "notification-templates", "get", "--server", host.URL)
	require.ErrorContains(t, err, "template group name")
}
