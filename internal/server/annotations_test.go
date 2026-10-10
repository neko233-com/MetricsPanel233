package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnnotationHTTPContractsAuthenticationScopesRegionsAndTags(t *testing.T) {
	_, h := setup(t, "annot-token")
	token := "annot-token"
	source := `{"dashboardUID":"system","panelId":3,"time":1000,"timeEnd":5000,"text":"deploy <script>literal</script>","tags":["deploy","mysql"],"data":{"agent":"go"},"idempotencyKey":"deploy"}`
	assert.Equal(t, 401, call(h, "POST", "/api/annotations", source, "", "").Code)
	first := call(h, "POST", "/api/annotations", source, token, "")
	require.Equal(t, 200, first.Code, first.Body.String())
	var created struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &created))
	require.Positive(t, created.ID)
	assert.JSONEq(t, first.Body.String(), call(h, "POST", "/api/annotations", source, token, "").Body.String())
	assert.Equal(t, 409, call(h, "POST", "/api/annotations", `{"time":1000,"text":"changed","idempotencyKey":"deploy"}`, token, "").Code)
	queried := call(h, "GET", "/api/annotations?dashboardUID=system&panelId=3&from=2000&to=3000&tags=deploy&tags=mysql", "", token, "")
	require.Equal(t, 200, queried.Code, queried.Body.String())
	var items []model.Annotation
	require.NoError(t, json.Unmarshal(queried.Body.Bytes(), &items))
	require.Len(t, items, 1)
	assert.Equal(t, created.ID, items[0].ID)
	assert.Equal(t, int64(5000), items[0].TimeEnd)
	url := fmt.Sprintf("/api/annotations/%d", created.ID)
	require.Equal(t, 200, call(h, "PATCH", url, `{"text":"updated","tags":[]}`, token, "").Code)
	item := call(h, "GET", url, "", token, "")
	var a model.Annotation
	require.NoError(t, json.Unmarshal(item.Body.Bytes(), &a))
	assert.Equal(t, "updated", a.Text)
	assert.Empty(t, a.Tags)
	assert.Equal(t, int64(1000), a.Time)
	assert.Equal(t, "system", a.DashboardUID)
	require.Equal(t, 200, call(h, "PUT", url, `{"time":6000,"text":"point"}`, token, "").Code)
	item = call(h, "GET", url, "", token, "")
	require.NoError(t, json.Unmarshal(item.Body.Bytes(), &a))
	assert.Equal(t, a.Time, a.TimeEnd)
	assert.Equal(t, 400, call(h, "POST", "/api/annotations", `{"time":1,"timeEnd":0,"text":""}`, token, "").Code)
	assert.Equal(t, 400, call(h, "POST", "/api/annotations", `{"time":3000,"timeEnd":2000,"text":"bad"}`, token, "").Code)
	assert.Equal(t, 404, call(h, "POST", "/api/annotations", `{"dashboardUID":"missing","time":1,"text":"bad"}`, token, "").Code)
	assert.Equal(t, 400, call(h, "GET", "/api/annotations?limit=1001", "", token, "").Code)
	graphite := call(h, "POST", "/api/annotations/graphite", `{"what":"Deploy","when":123,"tags":"go release","data":"main"}`, token, "")
	require.Equal(t, 200, graphite.Code, graphite.Body.String())
	tags := call(h, "GET", "/api/annotations/tags?tag=release", "", token, "")
	require.Equal(t, 200, tags.Code)
	assert.Contains(t, tags.Body.String(), `"count":1`)
	require.Equal(t, 200, call(h, "POST", "/api/annotations/mass-delete", `{"dashboardUID":"system","panelId":3}`, token, "").Code)
	assert.Equal(t, 404, call(h, "DELETE", url, "", token, "").Code)
	assert.Equal(t, 400, call(h, "POST", "/api/annotations/mass-delete", `{}`, token, "").Code)
	assert.Equal(t, 200, call(h, "GET", "/api/v1/annotations?type=alert", "", token, "").Code)
}

func TestAutomaticAlertAnnotationsGrafanaLinksFiltersAndRuleRemoval(t *testing.T) {
	s, h := setup(t, "alert-annotation-token")
	token := "alert-annotation-token"
	source := `{"uid":"linked-alert","title":"API is busy","folderUID":"general","ruleGroup":"service","condition":"C","for":"0s","annotations":{"__dashboardUid__":"system","__panelId__":"2"},"labels":{"service":"api"},"data":[{"refId":"C","datasourceUid":"metricspanel","model":{"expr":"vector(233)","instant":true}}]}`
	w := call(h, "POST", "/api/v1/provisioning/alert-rules", source, token, "")
	require.Equal(t, 201, w.Code, w.Body.String())
	w = call(h, "POST", "/api/v1/alerts/rules/linked-alert/evaluate", "", token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, 401, call(h, "GET", "/api/annotations?type=alert", "", "", "").Code)
	w = call(h, "GET", "/api/annotations?type=alert&alertUID=linked-alert&dashboardUID=system&panelId=2&tags=service:api", "", token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var items []model.Annotation
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &items))
	require.Len(t, items, 1)
	assert.Equal(t, "Alerting", items[0].NewState)
	assert.Equal(t, "Normal", items[0].PrevState)
	assert.Contains(t, string(items[0].Data), `"C":233`)
	url := fmt.Sprintf("/api/annotations?alertId=%d&type=alert", items[0].AlertID)
	assert.JSONEq(t, w.Body.String(), call(h, "GET", url, "", token, "").Body.String())
	assert.JSONEq(t, "[]", call(h, "GET", "/api/annotations?type=annotation", "", token, "").Body.String())
	assert.JSONEq(t, "[]", call(h, "GET", "/api/annotations?type=alert&userUID=metricspanel", "", token, "").Body.String())
	assert.Equal(t, 400, call(h, "GET", "/api/annotations?alertId=-1", "", token, "").Code)
	assert.Equal(t, 400, call(h, "GET", "/api/annotations/tags?type=wrong", "", token, "").Code)
	assert.Contains(t, call(h, "GET", "/api/annotations/tags?type=alert&tag=service:", "", token, "").Body.String(), `"count":1`)
	record := `{"uid":"recorded","title":"Record only","expr":"vector(1)","record":"recorded:one"}`
	require.Equal(t, 200, call(h, "POST", "/api/v1/alerts/rules", record, token, "").Code)
	require.Equal(t, 200, call(h, "POST", "/api/v1/alerts/rules/recorded/evaluate", "", token, "").Code)
	assert.JSONEq(t, "[]", call(h, "GET", "/api/annotations?alertUID=recorded", "", token, "").Body.String())
	require.Equal(t, 200, call(h, "DELETE", "/api/v1/alerts/rules/linked-alert", "", token, "").Code)
	items, err := s.Annotations(context.Background(), model.AnnotationQuery{AlertUID: "linked-alert"})
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "Normal (Deleted)", items[0].NewState)
	assert.Equal(t, "system", items[0].DashboardUID)
}
