package server_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/alerting"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlertPreviewIsAuthenticatedAndNeverChangesRulesHistoryOrRecordings(t *testing.T) {
	s, handler := setup(t, "preview-secret-233233")
	ctx := context.Background()
	saved, err := s.SaveAlertRule(ctx, model.AlertRule{UID: "existing", Title: "Existing", Expr: "vector(1)", Condition: "nonzero", IntervalSeconds: 30})
	require.NoError(t, err)
	before, err := s.AlertRule(ctx, saved.UID)
	require.NoError(t, err)
	rules, err := s.AlertRules(ctx)
	require.NoError(t, err)
	history, err := s.AlertHistory(ctx, "", 100)
	require.NoError(t, err)
	metrics, err := s.Metrics(ctx)
	require.NoError(t, err)
	const body = `{"at":1600000,"grafana":{"condition":"Q","record":{"from":"Q","target_datasource_uid":"metricspanel"},"opaque":233,"data":[{"refId":"A","datasourceUid":"metricspanel","relativeTimeRange":{"from":120,"to":5},"model":{"expr":"vector(233)","instant":true}},{"refId":"Q","datasourceUid":"__expr__","model":{"type":"sql","expression":"SELECT __value__ * 2 AS result FROM A","format":"table"}}]}}`
	assert.Equal(t, 401, call(handler, "POST", "/api/v1/alerts/preview", body, "", "").Code)
	for range 2 {
		w := call(handler, "POST", "/api/v1/alerts/preview", body, "preview-secret-233233", "")
		require.Equal(t, 200, w.Code, w.Body.String())
		var result alerting.GraphPreview
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		assert.Equal(t, int64(1600000), result.At)
		require.Empty(t, result.Error)
		require.Len(t, result.Values, 1)
		assert.Equal(t, 466.0, *result.Values[0].Value)
		assert.Contains(t, result.Results, "A")
		assert.Contains(t, result.Results, "Q")
	}
	after, err := s.AlertRule(ctx, saved.UID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	currentRules, err := s.AlertRules(ctx)
	require.NoError(t, err)
	assert.Equal(t, rules, currentRules)
	currentHistory, err := s.AlertHistory(ctx, "", 100)
	require.NoError(t, err)
	assert.Equal(t, history, currentHistory)
	currentMetrics, err := s.Metrics(ctx)
	require.NoError(t, err)
	assert.Equal(t, metrics, currentMetrics)
	for _, invalid := range []string{`{}`, `{"at":-1,"grafana":{}}`, `{"at":253402300800000,"grafana":{}}`, `{"grafana":{"condition":"A","data":[{"refId":"A","datasourceUid":"missing","model":{}}]}}`} {
		assert.Equal(t, 400, call(handler, "POST", "/api/v1/alerts/preview", invalid, "preview-secret-233233", "").Code)
	}
}
