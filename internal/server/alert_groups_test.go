package server_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/alerting"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGrafanaRuleGroupReplacementIdempotenceOrderingPoliciesAndMoves(t *testing.T) {
	s, h := setup(t, "group-token-233233")
	token := "group-token-233233"
	const path = "/api/v1/provisioning/folder/general/rule-groups/service"
	rule := func(uid, title string) alerting.GrafanaRule {
		var g alerting.GrafanaRule
		require.NoError(t, json.Unmarshal([]byte(`{"condition":"C","for":"10s","data":[{"refId":"C","datasourceUid":"__expr__","model":{"type":"math","expression":"1"}}]}`), &g))
		g.UID, g.Title, g.FolderUID, g.RuleGroup = uid, title, "ignored-body-folder", "ignored-body-group"
		return g
	}
	group := alerting.GrafanaGroup{Title: "ignored", FolderUID: "ignored", Interval: 30, Rules: []alerting.GrafanaRule{rule("z", "First"), rule("a", "Second")}}
	marshal := func(v any) string { raw, err := json.Marshal(v); require.NoError(t, err); return string(raw) }
	assert.Equal(t, 401, call(h, "PUT", path, marshal(group), "", "").Code)
	w := call(h, "PUT", path, marshal(group), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var result alerting.GrafanaGroup
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	assert.Equal(t, "general", result.FolderUID)
	assert.Equal(t, "service", result.Title)
	require.Len(t, result.Rules, 2)
	assert.Equal(t, "z", result.Rules[0].UID)
	assert.Equal(t, "a", result.Rules[1].UID)
	assert.Equal(t, "general", result.Rules[0].FolderUID)
	assert.Equal(t, "service", result.Rules[0].RuleGroup)
	w = call(h, "POST", "/api/v1/alerts/rules/z/evaluate", "", token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var pending model.AlertRuleView
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &pending))
	require.Len(t, pending.Runtime.Instances, 1)
	assert.Equal(t, "Pending", pending.Runtime.Instances[0].State)
	assert.Equal(t, 200, call(h, "PUT", path, marshal(group), token, "").Code)
	again, err := s.AlertRule(t.Context(), "z")
	require.NoError(t, err)
	assert.Equal(t, pending, again, "identical group PUT must preserve version, timers and state")
	get := call(h, "GET", path, "", token, "")
	require.Equal(t, 200, get.Code, get.Body.String())
	w = call(h, "PUT", path, get.Body.String(), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	again, err = s.AlertRule(t.Context(), "z")
	require.NoError(t, err)
	assert.Equal(t, pending, again, "GET output is safe to resubmit")
	before, err := s.AlertRules(t.Context())
	require.NoError(t, err)
	bad := group
	bad.Rules = append([]alerting.GrafanaRule{}, group.Rules...)
	bad.Rules[0].Title = "Would change"
	bad.Rules[1].Data = []alerting.GrafanaQuery{{RefID: "C", DatasourceUID: "unknown-source", Model: json.RawMessage(`{"query":"bad"}`)}}
	assert.Equal(t, 400, call(h, "PUT", path, marshal(bad), token, "").Code)
	after, err := s.AlertRules(t.Context())
	require.NoError(t, err)
	assert.Equal(t, before, after)
	bad = group
	bad.Rules = []alerting.GrafanaRule{group.Rules[0], group.Rules[0]}
	assert.Equal(t, 400, call(h, "PUT", path, marshal(bad), token, "").Code)
	assert.Equal(t, 400, call(h, "PUT", path, `{"interval":0}`, token, "").Code)
	assert.Equal(t, 200, call(h, "PUT", path, `{"interval":60}`, token, "").Code)
	again, err = s.AlertRule(t.Context(), "z")
	require.NoError(t, err)
	assert.Equal(t, pending.Runtime, again.Runtime, "interval-only update must retain pending timers")
	assert.Equal(t, 60, again.IntervalSeconds)
	assert.Equal(t, pending.Version+1, again.Version)
	req := httptest.NewRequest("PUT", path, strings.NewReader(`{"interval":60}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Disable-Provenance", "true")
	editable := httptest.NewRecorder()
	h.ServeHTTP(editable, req)
	require.Equal(t, 200, editable.Code, editable.Body.String())
	result = alerting.GrafanaGroup{}
	require.NoError(t, json.Unmarshal(editable.Body.Bytes(), &result))
	for _, rule := range result.Rules {
		assert.Empty(t, rule.Provenance)
	}
	// Moving First also normalizes Second's group index without resetting state.
	move := result
	move.Rules = move.Rules[:1]
	w = call(h, "PUT", "/api/v1/provisioning/folder/general/rule-groups/destination", marshal(move), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	moved, err := s.AlertRule(t.Context(), "z")
	require.NoError(t, err)
	assert.Equal(t, "destination", moved.Group)
	assert.Equal(t, pending.Runtime, moved.Runtime)
	second, err := s.AlertRule(t.Context(), "a")
	require.NoError(t, err)
	assert.Zero(t, second.GroupIndex)
	assert.Equal(t, "service", second.Group)
	assert.Equal(t, 200, call(h, "PUT", path, `{"interval":60,"rules":[]}`, token, "").Code)
	assert.Equal(t, 404, call(h, "GET", path, "", token, "").Code)
	assert.Equal(t, 401, call(h, "DELETE", "/api/v1/provisioning/folder/general/rule-groups/destination", "", "", "").Code)
	assert.Equal(t, 204, call(h, "DELETE", "/api/v1/provisioning/folder/general/rule-groups/destination", "", token, "").Code)
	assert.Equal(t, 204, call(h, "DELETE", "/api/v1/provisioning/folder/general/rule-groups/destination", "", token, "").Code)
	generated := rule("", "Generated UID")
	group.Rules = []alerting.GrafanaRule{generated}
	w = call(h, "PUT", path, marshal(group), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	result = alerting.GrafanaGroup{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	require.Len(t, result.Rules, 1)
	assert.NotEmpty(t, result.Rules[0].UID)
	w = call(h, "PUT", path, `{"interval":45,"rules":null}`, token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), result.Rules[0].UID, "null rules retain existing membership")
	assert.Equal(t, 204, call(h, "DELETE", path, "", token, "").Code)
}
