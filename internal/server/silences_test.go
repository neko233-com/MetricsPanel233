package server_test

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSilenceAPIGrafanaCompatibilityValidationAndFiltering(t *testing.T) {
	_, h := setup(t, "silence-token")
	token := "silence-token"
	at := time.Now()
	value := model.Silence{Matchers: []model.SilenceMatcher{{Name: "service", Value: "database", IsRegex: false}}, StartsAt: at.Add(-time.Minute), EndsAt: at.Add(time.Hour), CreatedBy: "operator", Comment: "Database maintenance"}
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	assert.Equal(t, 401, call(h, "POST", "/api/alertmanager/grafana/api/v2/silences", string(raw), "", "").Code)
	assert.Equal(t, 403, call(h, "POST", "/api/alertmanager/grafana/api/v2/silences", string(raw), token, "https://unrelated.invalid").Code)
	w := call(h, "POST", "/api/alertmanager/grafana/api/v2/silences", string(raw), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var saved struct {
		ID string `json:"silenceID"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &saved))
	require.NotEmpty(t, saved.ID)
	w = call(h, "GET", "/api/alertmanager/grafana/api/v2/silence/"+saved.ID, "", token, "")
	require.Equal(t, 200, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &value))
	assert.Equal(t, "active", value.Status.State)
	require.NotNil(t, value.Matchers[0].IsEqual)
	assert.True(t, *value.Matchers[0].IsEqual)
	assert.NotZero(t, value.UpdatedAt)
	assert.Contains(t, w.Body.String(), `"annotations":{}`)
	w = call(h, "GET", "/api/alertmanager/grafana/api/v2/silences?filter="+url.QueryEscape(`service="database"`), "", token, "")
	require.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), saved.ID)
	w = call(h, "GET", "/api/alertmanager/grafana/api/v2/silences?filter="+url.QueryEscape(`service="other"`), "", token, "")
	require.Equal(t, 200, w.Code)
	assert.JSONEq(t, `[]`, w.Body.String())
	w = call(h, "GET", "/api/v1/alerting/silences?active=false", "", token, "")
	require.Equal(t, 200, w.Code)
	assert.JSONEq(t, `[]`, w.Body.String())
	assert.Equal(t, 400, call(h, "GET", "/api/v1/alerting/silences?active=broken", "", token, "").Code)
	assert.Equal(t, 400, call(h, "GET", "/api/v1/alerting/silences?filter=broken", "", token, "").Code)
	value.Comment = "Updated maintenance"
	value.StartsAt = at.Add(time.Hour)
	value.EndsAt = at.Add(2 * time.Hour)
	raw, err = json.Marshal(value)
	require.NoError(t, err)
	w = call(h, "POST", "/api/v1/alerting/silences", string(raw), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = call(h, "GET", "/api/v1/alerting/silences/"+saved.ID, "", token, "")
	require.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), `"state":"pending"`)
	assert.Contains(t, w.Body.String(), "Updated maintenance")
	w = call(h, "DELETE", "/api/alertmanager/grafana/api/v2/silence/"+saved.ID, "", token, "")
	require.Equal(t, 200, w.Code)
	w = call(h, "GET", "/api/v1/alerting/silences/"+saved.ID, "", token, "")
	require.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), `"state":"expired"`)
	assert.Equal(t, 404, call(h, "DELETE", "/api/v1/alerting/silences/missing", "", token, "").Code)
	assert.Equal(t, 400, call(h, "POST", "/api/v1/alerting/silences", `{"matchers":[{"name":"alertname","value":".*","isRegex":true}],"startsAt":"2026-10-12T00:00:00Z","endsAt":"2026-10-12T01:00:00Z","createdBy":"operator","comment":"Overly broad"}`, token, "").Code)
	assert.Equal(t, 400, call(h, "POST", "/api/v1/alerting/silences", `{"matchers":[{"name":"alertname","value":"[","isRegex":true}],"startsAt":"2026-10-12T00:00:00Z","endsAt":"2026-10-12T01:00:00Z","createdBy":"operator","comment":"Invalid regex"}`, token, "").Code)
	w = call(h, "POST", "/api/v1/alerting/silences/preview", `{"matchers":[{"name":"service","value":"db|cache","isRegex":true}],"labels":{"service":"db"}}`, token, "")
	require.Equal(t, 200, w.Code)
	assert.JSONEq(t, `{"matches":true}`, w.Body.String())
	w = call(h, "POST", "/api/v1/alerting/silences/preview", `{"matchers":[{"name":"service","value":"db|cache","isRegex":true}],"labels":{"service":"redis"}}`, token, "")
	require.Equal(t, 200, w.Code)
	assert.JSONEq(t, `{"matches":false}`, w.Body.String())
}
