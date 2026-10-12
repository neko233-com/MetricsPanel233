package server_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInhibitionNativeAPIAuthCASPreviewAndActive(t *testing.T) {
	s, h := setup(t, "inhibition-token")
	token := "inhibition-token"
	base := "/api/v1/alerting/inhibition-rules"
	value := model.InhibitionRule{UID: "root-causes", Name: "Root causes", SourceMatchers: []model.NotificationMatcher{{Name: "role", Operator: "=", Value: "source"}}, TargetMatchers: []model.NotificationMatcher{{Name: "role", Operator: "!=", Value: "source"}}, Equal: []string{"rack"}}
	body := templateJSON(t, value)
	assert.Equal(t, 401, call(h, "GET", base, "", "", "").Code)
	assert.Equal(t, 401, call(h, "POST", base, body, "", "").Code)
	assert.Equal(t, 403, call(h, "POST", base, body, token, "https://foreign.invalid").Code)
	w := call(h, "POST", base, body, token, "")
	require.Equal(t, 201, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &value))
	assert.Equal(t, 1, value.Version)
	assert.Equal(t, 409, call(h, "POST", base, body, token, "").Code)
	assert.Equal(t, 200, call(h, "GET", base+"/root-causes", "", token, "").Code)
	assert.Equal(t, 404, call(h, "GET", base+"/missing", "", token, "").Code)
	rule, err := s.SaveAlertRule(context.Background(), model.AlertRule{UID: "source", Title: "Source", Expr: "vector(1)", Condition: "nonzero"})
	require.NoError(t, err)
	require.NoError(t, s.CommitAlertEvaluation(context.Background(), rule.UID, rule.Version, model.AlertRuntime{LastEvaluation: 1000, Instances: []model.AlertInstance{{Key: "active", State: "Firing", Labels: map[string]string{"role": "source"}}}}, nil))
	w = call(h, "GET", base+"/active?limit=1", "", token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"total":1`)
	assert.Contains(t, w.Body.String(), `"state":"Firing"`)
	assert.Equal(t, 400, call(h, "GET", base+"/active?limit=201", "", token, "").Code)
	preview := model.InhibitionPreviewInput{SourceMatchers: value.SourceMatchers, TargetMatchers: value.TargetMatchers, Equal: value.Equal, Labels: &map[string]string{}}
	w = call(h, "POST", base+"/preview", templateJSON(t, preview), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"inhibited":true`)
	assert.Contains(t, w.Body.String(), `"source_count":1`)
	preview.Labels = nil
	w = call(h, "POST", base+"/preview", templateJSON(t, preview), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"target_matches":false`)
	rows, err := s.Notifications(context.Background(), "", 100)
	require.NoError(t, err)
	assert.Empty(t, rows)
	value.Name = "Edited"
	w = call(h, "PUT", base+"/root-causes", templateJSON(t, value), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, 409, call(h, "PUT", base+"/root-causes", templateJSON(t, value), token, "").Code)
	assert.Equal(t, 409, call(h, "DELETE", base+"/root-causes", "", token, "").Code)
	assert.Equal(t, 409, call(h, "DELETE", base+"/root-causes?version=1", "", token, "").Code)
	assert.Equal(t, 200, call(h, "DELETE", base+"/root-causes?version=2", "", token, "").Code)
	assert.Equal(t, 404, call(h, "DELETE", base+"/root-causes?version=2", "", token, "").Code)
	value.UID = "a.-b"
	value.Version = 0
	assert.Equal(t, 400, call(h, "POST", base, templateJSON(t, value), token, "").Code)
	assert.Equal(t, 400, call(h, "POST", base+"/preview", `{"source_matchers":[{"name":"role","operator":"=~","value":"("}],"target_matchers":[{"name":"role","operator":"=","value":"target"}],"equal":[]}`, token, "").Code)
	assert.Equal(t, 400, call(h, "POST", base+"/preview", `{"source_matchers":[],"target_matchers":[],"unknown":"not-silently-accepted"}`, token, "").Code)
}

func TestInhibitionGrafanaResourceCRUDAndVersionOrdering(t *testing.T) {
	s, h := setup(t, "")
	base := "/apis/notifications.alerting.grafana.app/v1beta1/namespaces/default/inhibitionrules"
	body := `{"apiVersion":"notifications.alerting.grafana.app/v1beta1","kind":"InhibitionRule","metadata":{"name":"root-causes","namespace":"default","annotations":{"metricspanel233.io/display-name":"Root causes","metricspanel233.io/disabled":"true"}},"spec":{"source_matchers":[{"label":"role","type":"=","value":"source"},{"label":"job","type":"=~","value":"mysql|redis"}],"target_matchers":[{"label":"role","type":"!=","value":"source"}],"equal":["cluster","rack"]}}`
	w := call(h, "POST", base, body, "", "")
	require.Equal(t, 201, w.Code, w.Body.String())
	var v map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &v))
	metadata := v["metadata"].(map[string]any)
	version := metadata["resourceVersion"].(string)
	assert.Len(t, version, 16)
	native, err := s.InhibitionRule(context.Background(), "root-causes")
	require.NoError(t, err)
	assert.True(t, native.Disabled)
	assert.Equal(t, "Root causes", native.Name)
	// Equivalent matcher/equal reorder retains pinned Grafana resourceVersion.
	spec := v["spec"].(map[string]any)
	matchers := spec["source_matchers"].([]any)
	spec["source_matchers"] = []any{matchers[1], matchers[0]}
	spec["equal"] = []string{"rack", "cluster"}
	w = call(h, "PUT", base+"/root-causes", templateJSON(t, v), "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var ordered map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &ordered))
	assert.Equal(t, version, ordered["metadata"].(map[string]any)["resourceVersion"])
	assert.Equal(t, 409, call(h, "PUT", base+"/root-causes", templateJSON(t, v), "", "").Code, "same Grafana spec hash must not let stale native extensions overwrite newer versions")
	v = ordered
	spec = v["spec"].(map[string]any)
	spec["target_matchers"] = []any{map[string]any{"label": "role", "type": "=", "value": "target"}}
	w = call(h, "PUT", base+"/root-causes", templateJSON(t, v), "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	ordered = map[string]any{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &ordered))
	latest := ordered["metadata"].(map[string]any)["resourceVersion"].(string)
	assert.NotEqual(t, version, latest)
	assert.Equal(t, 409, call(h, "PUT", base+"/root-causes", templateJSON(t, v), "", "").Code)
	assert.Equal(t, 400, call(h, "PUT", base+"/other", templateJSON(t, v), "", "").Code)
	w = call(h, "GET", base, "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"kind":"InhibitionRuleList"`)
	assert.Equal(t, 404, call(h, "GET", "/apis/notifications.alerting.grafana.app/v1beta1/namespaces/other/inhibitionrules", "", "", "").Code)
	assert.Equal(t, 409, call(h, "DELETE", base+"/root-causes", "", "", "").Code)
	assert.Equal(t, 409, call(h, "DELETE", base+"/root-causes", `{"apiVersion":"v1","kind":"DeleteOptions","preconditions":{"resourceVersion":"`+version+`"}}`, "", "").Code)
	w = call(h, "DELETE", base+"/root-causes", `{"apiVersion":"v1","kind":"DeleteOptions","preconditions":{"resourceVersion":"`+latest+`"}}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, 404, call(h, "GET", base+"/root-causes", "", "", "").Code)
}
