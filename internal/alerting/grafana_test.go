package alerting

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/promcompat"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func grafRule(t *testing.T, source string) GrafanaRule {
	t.Helper()
	var r GrafanaRule
	require.NoError(t, json.Unmarshal([]byte(source), &r))
	return r
}
func TestLegacyGrafanaReduceThresholdMathClassicAndExport(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "grafana.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	e := New(s, promcompat.New(s))
	base := `{"uid":"grafana","title":"Temperature","condition":"C","for":"10s","data":[{"refId":"A","datasourceUid":"metricspanel","relativeTimeRange":{"from":60,"to":0},"model":{"expr":"vector(233)","intervalMs":15000}},{"refId":"B","datasourceUid":"__expr__","model":{"type":"reduce","expression":"A","reducer":"mean"}},{"refId":"C","datasourceUid":"__expr__","model":{"type":"threshold","expression":"B","conditions":[{"evaluator":{"type":"gt","params":[200]}}]}}]}`
	g := grafRule(t, base)
	r, err := compileLegacyGrafana(g, 15)
	require.NoError(t, err)
	assert.Contains(t, r.Expr, "avg_over_time")
	assert.Equal(t, 10, r.ForSeconds)
	values, err := e.Query(context.Background(), r.Expr, time.Now())
	require.NoError(t, err)
	require.Len(t, values, 1)
	assert.Equal(t, 1.0, values[0].Value)
	_, err = compileLegacyGrafana(ExportGrafana(r), 15)
	require.NoError(t, err)
	g.Data[2].Model = json.RawMessage(`{"type":"math","expression":"$B < 200"}`)
	r, err = compileLegacyGrafana(g, 15)
	require.NoError(t, err)
	values, err = e.Query(context.Background(), r.Expr, time.Now())
	require.NoError(t, err)
	require.Len(t, values, 1)
	assert.Zero(t, values[0].Value, "false must remain a zero-valued instance")
	g.Data[2].Model = json.RawMessage(`{"type":"threshold","expression":"B","conditions":[{"evaluator":{"type":"gt","params":[200]},"unloadEvaluator":{"type":"lt","params":[180]}}]}`)
	_, err = compileLegacyGrafana(g, 15)
	require.ErrorContains(t, err, "recovery")
	g.Data[2].Model = json.RawMessage(`{"type":"math","expression":"$A + $B"}`)
	_, err = compileLegacyGrafana(g, 15)
	require.ErrorContains(t, err, "label joins")
	g.Data = g.Data[:1]
	g.Condition = "C"
	q := GrafanaQuery{RefID: "C", DatasourceUID: "-100", Model: json.RawMessage(`{"type":"classic_conditions","conditions":[{"query":{"params":["A"]},"reducer":{"type":"last"},"evaluator":{"type":"gt","params":[200]},"operator":{"type":"and"}}]}`)}
	g.Data = append(g.Data, q)
	r, err = compileLegacyGrafana(g, 15)
	require.NoError(t, err)
	values, err = e.Query(context.Background(), r.Expr, time.Now())
	require.NoError(t, err)
	require.Len(t, values, 1)
	assert.Empty(t, values[0].Labels)
}
func TestLegacyGrafanaRejectsUnsupportedGraphsWithoutPersistence(t *testing.T) {
	base := `{"uid":"grafana","title":"Rule","condition":"A","data":[{"refId":"A","datasourceUid":"metricspanel","model":{"expr":"up","instant":true}}]}`
	for _, mutate := range []func(*GrafanaRule){func(g *GrafanaRule) { g.Data[0].DatasourceUID = "loki" }, func(g *GrafanaRule) { g.Condition = "missing" }, func(g *GrafanaRule) {
		g.Data[0].DatasourceUID = "__expr__"
		g.Data[0].Model = json.RawMessage(`{"type":"reduce","expression":"A"}`)
	}, func(g *GrafanaRule) { g.For = "-1s" }, func(g *GrafanaRule) { g.OrgID = 2 }, func(g *GrafanaRule) { g.NotificationSettings = json.RawMessage(`{"receiver":"slack"}`) }, func(g *GrafanaRule) {
		g.Data[0].DatasourceUID = "__expr__"
		g.Data[0].Model = json.RawMessage(`{"type":"resample"}`)
	}} {
		g := grafRule(t, base)
		mutate(&g)
		_, err := compileLegacyGrafana(g, 30)
		require.Error(t, err)
	}
}
