package server_test

import (
	"encoding/json"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlertAPISecurityVersionsEvaluationAndGrafanaProvisioning(t *testing.T) {
	_, h := setup(t, "alerts-token-233233")
	token := "alerts-token-233233"
	body := `{"uid":"native","title":"Native","expr":"vector(0)","condition":"presence"}`
	assert.Equal(t, 401, call(h, "POST", "/api/v1/alerts/rules", body, "", "").Code)
	w := call(h, "POST", "/api/v1/alerts/rules", body, token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, 409, call(h, "POST", "/api/v1/alerts/rules", body, token, "").Code)
	w = call(h, "POST", "/api/v1/alerts/rules/native/evaluate", "", token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"state":"Firing"`)
	assert.Contains(t, call(h, "GET", "/prometheus/api/v1/alerts", "", token, "").Body.String(), `"value":"0"`)
	assert.Contains(t, call(h, "GET", "/prometheus/api/v1/rules", "", token, "").Body.String(), `"type":"alerting"`)
	assert.Equal(t, 400, call(h, "POST", "/api/v1/alerts/rules", `{"uid":"bad","title":"Bad","expr":"rate("}`, token, "").Code)
	assert.Equal(t, 400, call(h, "GET", "/api/v1/alerts/history?limit=1001", "", token, "").Code)
	var view model.AlertRuleView
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &view))
	view.Paused = true
	config, _ := json.Marshal(view.AlertRule)
	w = call(h, "PUT", "/api/v1/alerts/rules/native", string(config), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, 409, call(h, "PUT", "/api/v1/alerts/rules/native", string(config), token, "").Code)
	assert.Equal(t, 409, call(h, "POST", "/api/v1/alerts/rules/native/evaluate", "", token, "").Code)
	assert.Contains(t, call(h, "GET", "/api/v1/alerts/history?uid=native", "", token, "").Body.String(), `"reason":"Paused"`)
	graf := `{"uid":"graf","title":"Grafana imported","folderUID":"general","ruleGroup":"service","condition":"A","for":"0s","noDataState":"OK","execErrState":"Error","data":[{"refId":"A","datasourceUid":"metricspanel","model":{"expr":"vector(1)","instant":true}}]}`
	w = call(h, "POST", "/api/v1/provisioning/alert-rules", graf, token, "")
	require.Equal(t, 201, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"provenance":"api"`)
	w = call(h, "GET", "/api/v1/provisioning/folder/general/rule-groups/service", "", token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "Grafana imported")
	w = call(h, "POST", "/api/v1/alerts/rules/graf/evaluate", "", token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"state":"Firing"`)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &view))
	view.Expr = "vector(0)"
	config, _ = json.Marshal(view.AlertRule)
	w = call(h, "PUT", "/api/v1/alerts/rules/graf", string(config), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), `"grafana"`, "native expression edits must not export a stale Grafana graph")
	assert.Equal(t, 204, call(h, "DELETE", "/api/v1/provisioning/alert-rules/graf", "", token, "").Code)
	assert.Equal(t, 404, call(h, "GET", "/api/v1/alerts/rules/graf", "", token, "").Code)
}
