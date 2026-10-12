package server_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func templateJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}

func TestNotificationTemplateAPIAuthCRUDAndReadOnlyPreview(t *testing.T) {
	s, h := setup(t, "template-api-token")
	token := "template-api-token"
	base := "/api/v1/alerting/notification-templates"
	value := model.NotificationTemplate{Name: "operations", Template: `{{define "ops.title"}}{{.Receiver | toUpper}} {{len .Alerts.Firing}}{{end}}{{define "ops.message"}}{{range .Alerts}}{{.RuleUID}}={{index .Values "A"}}{{end}}{{end}}`}
	body := templateJSON(t, value)
	assert.Equal(t, 401, call(h, "GET", base, "", "", "").Code)
	assert.Equal(t, 401, call(h, "POST", base, body, "", "").Code)
	assert.Equal(t, 403, call(h, "POST", base, body, token, "https://foreign.invalid").Code)
	w := call(h, "POST", base, body, token, "")
	require.Equal(t, 201, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &value))
	assert.Equal(t, 1, value.Version)
	assert.Equal(t, []string{"ops.message", "ops.title"}, value.Definitions)
	assert.Equal(t, 409, call(h, "POST", base, body, token, "").Code)
	assert.Equal(t, 200, call(h, "GET", base+"/operations", "", token, "").Code)
	assert.Equal(t, 404, call(h, "GET", base+"/missing", "", token, "").Code)
	preview := map[string]any{"name": "operations", "template": `{{define "ops.title"}}DRAFT {{.Receiver}}{{end}}`, "data": map[string]any{"receiver": "Operations", "status": "firing", "alerts": []any{}}}
	w = call(h, "POST", base+"/preview", templateJSON(t, preview), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"text":"DRAFT Operations"`)
	assert.Contains(t, w.Body.String(), `"errors":[]`)
	saved, err := s.NotificationTemplate(context.Background(), value.Name)
	require.NoError(t, err)
	assert.Equal(t, value, saved, "draft preview does not mutate saved source or version")
	history, err := s.Notifications(context.Background(), "", 100)
	require.NoError(t, err)
	assert.Empty(t, history, "preview does not enqueue a delivery")
	w = call(h, "POST", base+"/preview", `{"title":"{{.BearerToken}}"}`, token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"results":[]`)
	assert.Contains(t, w.Body.String(), `"errors":[`)
	w = call(h, "POST", base+"/preview", `{"title":"{{range 1000000000000}}{{end}}"}`, token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "execution limit exceeded")
	assert.Equal(t, 400, call(h, "POST", base+"/preview", `{"title":"ok","data":{"bearer_token":"must-not-be-accepted"}}`, token, "").Code)
	value.Template = `{{define "ops.title"}}Updated{{end}}`
	w = call(h, "PUT", base+"/operations", templateJSON(t, value), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, 409, call(h, "PUT", base+"/operations", templateJSON(t, value), token, "").Code)
	assert.Equal(t, 409, call(h, "DELETE", base+"/operations?version=1", "", token, "").Code)
	assert.Equal(t, 200, call(h, "DELETE", base+"/operations?version=2", "", token, "").Code)
	assert.Equal(t, 404, call(h, "DELETE", base+"/operations", "", token, "").Code)
}

func TestNotificationTemplateAPIDependenciesAndProvisioningRoundTrip(t *testing.T) {
	s, h := setup(t, "")
	base := "/api/v1/provisioning/templates"
	source := `{{define "shared.title"}}{{.Receiver}}{{end}}`
	w := call(h, "PUT", base+"/shared", templateJSON(t, map[string]any{"template": source}), "", "")
	require.Equal(t, 202, w.Code, w.Body.String())
	var exported map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &exported))
	assert.Equal(t, source, exported["template"])
	assert.Equal(t, "1", exported["version"])
	assert.Equal(t, "api", exported["provenance"])
	assert.Equal(t, "shared", exported["name"])
	w = call(h, "PUT", base+"/dependent", templateJSON(t, map[string]any{"template": `{{define "dependent.title"}}{{template "shared.title" .}}{{end}}`}), "", "")
	require.Equal(t, 202, w.Code, w.Body.String())
	assert.Equal(t, 409, call(h, "DELETE", base+"/shared", "", "", "").Code)
	point := map[string]any{"uid": "ops", "name": "Operations", "type": "webhook", "settings": map[string]any{"url": "http://localhost/webhook", "title": `{{template "dependent.title" .}}`, "message": "{{len .Alerts}} alerts", "authorization_credentials": "template-contact-secret"}}
	w = call(h, "POST", "/api/v1/provisioning/contact-points", templateJSON(t, point), "", "")
	require.Equal(t, 202, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "template-contact-secret")
	assert.Contains(t, w.Body.String(), "[REDACTED]")
	assert.Contains(t, w.Body.String(), `"message":"{{len .Alerts}} alerts"`)
	assert.Equal(t, 409, call(h, "DELETE", base+"/dependent", "", "", "").Code)
	point["name"] = "Renamed"
	point["settings"] = map[string]any{"url": "http://localhost/webhook"}
	w = call(h, "PUT", "/api/v1/provisioning/contact-points/ops", templateJSON(t, point), "", "")
	require.Equal(t, 202, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"message":"{{len .Alerts}} alerts"`, "omitted fields survive Grafana round trips")
	stored, err := s.ContactPoint(context.Background(), "ops")
	require.NoError(t, err)
	assert.Equal(t, `{{template "dependent.title" .}}`, stored.TitleTemplate)
	point["settings"] = map[string]any{"url": "http://localhost/webhook", "title": "", "message": ""}
	w = call(h, "PUT", "/api/v1/provisioning/contact-points/ops", templateJSON(t, point), "", "")
	require.Equal(t, 202, w.Code, w.Body.String())
	assert.Equal(t, 202, call(h, "DELETE", base+"/dependent", "", "", "").Code)
	assert.Equal(t, 409, call(h, "PUT", base+"/shared", `{"template":"Updated","version":"0"}`, "", "").Code)
	w = call(h, "PUT", base+"/shared", `{"template":"Updated {{.Receiver}}","version":"1"}`, "", "")
	require.Equal(t, 202, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"version":"2"`)
	w = call(h, "GET", base, "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"template":"Updated {{.Receiver}}"`)
	w = call(h, "POST", "/api/v1/alerting/notification-templates/preview", `{"name":"shared"}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"name":"shared","text":"Updated Preview"`)
	assert.Equal(t, 202, call(h, "DELETE", base+"/shared?version=2", "", "", "").Code)
	for _, source := range []string{`{{env "HOME"}}`, `{{query "up"}}`, `{{coll.Dict "a" 1}}`, `{{define "loop"}}{{template "loop" .}}{{end}}`, `{{template "missing" .}}`} {
		w = call(h, "PUT", base+"/unsupported", templateJSON(t, map[string]any{"template": source}), "", "")
		assert.Equal(t, 400, w.Code, w.Body.String())
	}
}

func TestNotificationTemplatePreviewUnnamedDraftDoesNotOverrideStoredPreview(t *testing.T) {
	_, h := setup(t, "")
	for _, name := range []string{"preview", "preview_"} {
		w := call(h, "POST", "/api/v1/alerting/notification-templates", templateJSON(t, map[string]any{"name": name, "template": "Stored"}), "", "")
		require.Equal(t, 201, w.Code, w.Body.String())
	}
	w := call(h, "POST", "/api/v1/alerting/notification-templates/preview", `{"template":"Unsaved"}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"text":"Unsaved"`)
	assert.Contains(t, w.Body.String(), `"errors":[]`)
}
