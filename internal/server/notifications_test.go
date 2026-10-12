package server_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotificationAPIAuthVersionsAndNoCredentialLeaks(t *testing.T) {
	_, h := setup(t, "notification-token")
	token := "notification-token"
	body := `{"uid":"ops","name":"Operations","type":"webhook","url":"http://localhost/webhook","bearer_token":"webhook-secret-233","headers":{"X-Private":"header-secret-233"}}`
	assert.Equal(t, 401, call(h, "POST", "/api/v1/alerting/contact-points", body, "", "").Code)
	assert.Equal(t, 403, call(h, "POST", "/api/v1/alerting/contact-points", body, token, "https://unrelated.invalid").Code)
	w := call(h, "POST", "/api/v1/alerting/contact-points", body, token, "")
	require.Equal(t, 201, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "webhook-secret-233")
	assert.NotContains(t, w.Body.String(), "header-secret-233")
	var point model.ContactPoint
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &point))
	assert.True(t, point.SecureFields["bearer_token"])
	assert.True(t, point.SecureFields["header:X-Private"])
	assert.Equal(t, 10, point.TimeoutSeconds)
	assert.Equal(t, 409, call(h, "POST", "/api/v1/alerting/contact-points", body, token, "").Code)
	assert.Equal(t, 400, call(h, "POST", "/api/v1/alerting/contact-points", `{"name":"Bad","type":"email","url":"http://localhost"}`, token, "").Code)
	assert.Equal(t, 400, call(h, "POST", "/api/v1/alerting/contact-points", `{"name":"Bad","url":"http://username:password@localhost"}`, token, "").Code)
	assert.Equal(t, 400, call(h, "POST", "/api/v1/alerting/contact-points", `{"name":"Bad","url":"http://localhost","headers":{"Content-Type":"text/plain"}}`, token, "").Code)
	point.Name = "Operations renamed"
	raw, err := json.Marshal(point)
	require.NoError(t, err)
	w = call(h, "PUT", "/api/v1/alerting/contact-points/ops", string(raw), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"bearer_token":true`)
	assert.Equal(t, 409, call(h, "PUT", "/api/v1/alerting/contact-points/ops", string(raw), token, "").Code)
	w = call(h, "POST", "/api/v1/alerting/contact-points/ops/test", "", token, "")
	require.Equal(t, 202, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"state":"pending"`)
	assert.Contains(t, w.Body.String(), `"status":"test"`)
	w = call(h, "PUT", "/api/v1/alerting/notification-policy", `{"receiver":"ops","routes":[{"receiver":"ops","matchers":[{"name":"severity","operator":"=~","value":"critical|warning"}]}]}`, token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, 409, call(h, "PUT", "/api/v1/alerting/notification-policy", `{"receiver":"ops"}`, token, "").Code)
	assert.Equal(t, 409, call(h, "DELETE", "/api/v1/alerting/contact-points/ops", "", token, "").Code)
	w = call(h, "GET", "/api/v1/alerting/notifications?limit=100&contact_point_uid=ops", "", token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "webhook-secret-233")
	assert.NotContains(t, w.Body.String(), "header-secret-233")
	assert.Equal(t, 400, call(h, "GET", "/api/v1/alerting/notifications?limit=1001", "", token, "").Code)
	assert.Equal(t, 404, call(h, "POST", "/api/v1/alerting/contact-points/missing/test", "", token, "").Code)
	assert.Equal(t, 400, call(h, "POST", "/api/v1/alerting/notifications/1/retry", "", token, "").Code)
}

func TestGrafanaNotificationProvisioningAndRuleReceiver(t *testing.T) {
	_, h := setup(t, "")
	body := `{"uid":"grafana-contact","name":"Operations","type":"webhook","settings":{"url":"http://localhost/webhook","httpMethod":"POST","authorization_scheme":"Bearer","authorization_credentials":"provisioned-secret-233"},"disableResolveMessage":false}`
	w := call(h, "POST", "/api/v1/provisioning/contact-points", body, "", "")
	require.Equal(t, 202, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "[REDACTED]")
	assert.NotContains(t, w.Body.String(), "provisioned-secret-233")
	w = call(h, "GET", "/api/v1/provisioning/contact-points?name=Operations", "", "", "")
	require.Equal(t, 200, w.Code)
	var points []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &points))
	require.Len(t, points, 1)
	points[0]["name"] = "Operations renamed"
	raw, err := json.Marshal(points[0])
	require.NoError(t, err)
	w = call(h, "PUT", "/api/v1/provisioning/contact-points/grafana-contact", string(raw), "", "")
	require.Equal(t, 202, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "[REDACTED]")
	assert.Equal(t, 400, call(h, "GET", "/api/v1/provisioning/contact-points?decrypt=true", "", "", "").Code)
	assert.Equal(t, 400, call(h, "POST", "/api/v1/provisioning/contact-points", `{"name":"Unsupported","type":"email","settings":{"addresses":"test@example.com"}}`, "", "").Code)
	assert.Equal(t, 400, call(h, "POST", "/api/v1/provisioning/contact-points", `{"name":"Unsupported","type":"webhook","settings":{"url":"http://localhost","maxAlerts":"4"}}`, "", "").Code)
	w = call(h, "PUT", "/api/v1/provisioning/policies", `{"receiver":"Operations renamed","routes":[{"receiver":"Operations renamed","object_matchers":[["team","=~","ops|infra"]],"continue":false}]}`, "", "")
	require.Equal(t, 202, w.Code, w.Body.String())
	w = call(h, "GET", "/api/v1/alerting/notification-policy", "", "", "")
	require.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), `"receiver":"grafana-contact"`)
	w = call(h, "GET", "/api/v1/provisioning/policies", "", "", "")
	require.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), `"receiver":"Operations renamed"`)
	assert.Contains(t, w.Body.String(), `"object_matchers":[["team","=~","ops|infra"]]`)
	assert.Equal(t, 202, call(h, "PUT", "/api/v1/provisioning/policies", `{"receiver":"Operations renamed","group_wait":"30s"}`, "", "").Code)
	assert.Equal(t, 202, call(h, "PUT", "/api/v1/provisioning/policies", `{"receiver":"Operations renamed","routes":[{"receiver":"Operations renamed","routes":[{"receiver":"Operations renamed"}]}]}`, "", "").Code)
	assert.Equal(t, 400, call(h, "PUT", "/api/v1/provisioning/policies", `{"receiver":"Operations renamed","matchers":["team=ops"]}`, "", "").Code)
	assert.Equal(t, 202, call(h, "DELETE", "/api/v1/provisioning/policies", "", "", "").Code)
	grafana := `{"uid":"direct","title":"Direct receiver","folderUID":"general","ruleGroup":"service","condition":"A","for":"0s","noDataState":"OK","execErrState":"Error","notification_settings":{"receiver":"Operations renamed"},"data":[{"refId":"A","datasourceUid":"metricspanel","model":{"expr":"vector(1)","instant":true}}]}`
	w = call(h, "POST", "/api/v1/provisioning/alert-rules", grafana, "", "")
	require.Equal(t, 201, w.Code, w.Body.String())
	w = call(h, "POST", "/api/v1/alerts/rules/direct/evaluate", "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = call(h, "GET", "/api/v1/alerting/notifications", "", "", "")
	require.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), `"rule_uid":"direct"`)
	assert.Contains(t, w.Body.String(), `"contact_point_uid":"grafana-contact"`)
	assert.Equal(t, 409, call(h, "DELETE", "/api/v1/provisioning/contact-points/grafana-contact", "", "", "").Code)
	assert.Equal(t, 204, call(h, "DELETE", "/api/v1/provisioning/alert-rules/direct", "", "", "").Code)
	assert.Equal(t, 202, call(h, "DELETE", "/api/v1/provisioning/contact-points/grafana-contact", "", "", "").Code)
}

func TestNotificationReceiverRenameExportAndGrafanaGroupRoundTrip(t *testing.T) {
	s, h := setup(t, "")
	point, err := s.SaveContactPoint(context.Background(), model.ContactPointInput{ContactPoint: model.ContactPoint{UID: "point", Name: "Before rename", URL: "http://localhost/webhook"}})
	require.NoError(t, err)
	body := `{"uid":"direct","title":"Receiver","folderUID":"general","ruleGroup":"service","condition":"A","for":"0s","notification_settings":{"receiver":"Before rename"},"data":[{"refId":"A","datasourceUid":"metricspanel","model":{"expr":"vector(1)","instant":true}}]}`
	w := call(h, "POST", "/api/v1/provisioning/alert-rules", body, "", "")
	require.Equal(t, 201, w.Code, w.Body.String())
	w = call(h, "POST", "/api/v1/alerts/rules/direct/evaluate", "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	before, err := s.AlertRule(context.Background(), "direct")
	require.NoError(t, err)
	point.Name = "After rename"
	_, err = s.SaveContactPoint(context.Background(), model.ContactPointInput{ContactPoint: point})
	require.NoError(t, err)
	for _, path := range []string{"/api/v1/provisioning/alert-rules", "/api/v1/provisioning/alert-rules/direct", "/api/v1/provisioning/folder/general/rule-groups/service"} {
		w = call(h, "GET", path, "", "", "")
		require.Equal(t, 200, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), `"notification_settings":{"receiver":"After rename"}`)
		assert.NotContains(t, w.Body.String(), "Before rename")
	}
	w = call(h, "PUT", "/api/v1/provisioning/folder/general/rule-groups/service", w.Body.String(), "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	after, err := s.AlertRule(context.Background(), "direct")
	require.NoError(t, err)
	assert.Equal(t, before.Version, after.Version, "exporting an updated receiver name does not change executable rule configuration")
	assert.Equal(t, before.Runtime, after.Runtime, "a contact point rename cannot reset pending/firing timers")
	after.NotificationReceiver = ""
	raw, err := json.Marshal(after.AlertRule)
	require.NoError(t, err)
	w = call(h, "PUT", "/api/v1/alerts/rules/direct", string(raw), "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "notification_settings")
	w = call(h, "GET", "/api/v1/provisioning/alert-rules/direct", "", "", "")
	require.Equal(t, 200, w.Code)
	assert.NotContains(t, w.Body.String(), "notification_settings")
	assert.Equal(t, 200, call(h, "DELETE", "/api/v1/alerting/contact-points/point", "", "", "").Code)
}
