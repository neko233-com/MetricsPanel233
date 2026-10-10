package alerting

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/promcompat"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGraphImportPreservesNodesAndSupportsCompoundConditions(t *testing.T) {
	g := grafRule(t, `{"uid":"graph","title":"Graph","condition":"C","for":"10s","data":[{"refId":"A","datasourceUid":"sdk","model":{"value":233,"unknown":233}},{"refId":"B","datasourceUid":"__expr__","model":{"type":"reduce","expression":"A","reducer":"mean","settings":{"mode":"strict"}}},{"refId":"C","datasourceUid":"__expr__","model":{"type":"classic_conditions","conditions":[{"query":{"params":["B"]},"reducer":{"type":"last"},"evaluator":{"type":"gt","params":[200]}},{"query":{"params":["A"]},"reducer":{"type":"last"},"operator":{"type":"and"},"evaluator":{"type":"lt","params":[300]}}]}}]}`)
	r, err := CompileGrafana(g, 15)
	require.NoError(t, err)
	assert.Equal(t, "grafana", r.Execution)
	assert.Empty(t, r.Expr)
	assert.Equal(t, 10, r.ForSeconds)
	assert.Equal(t, "NoData", r.NoDataState)
	out := ExportGrafana(r)
	assert.Equal(t, g.Data, out.Data)
	_, err = CompileGrafana(out, 15)
	require.NoError(t, err)
	g.Data[2].Model = json.RawMessage(`{"type":"threshold","expression":"B","conditions":[{"evaluator":{"type":"gt","params":[233]},"unloadEvaluator":{"type":"lt","params":[200]}}]}`)
	_, err = CompileGrafana(g, 15)
	require.NoError(t, err)
	for _, body := range []string{`{"type":"sql","expression":"select 1"}`, `{"type":"threshold","expression":"B","conditions":[{"evaluator":{"type":"gt","params":[233]},"unloadEvaluator":{"type":"lt","params":[]}}]}`} {
		g.Data[2].Model = json.RawMessage(body)
		_, err = CompileGrafana(g, 15)
		require.Error(t, err)
	}
}

func TestGraphNonFiniteTruthNullableInstancesAndPoliciesAreJSONSafe(t *testing.T) {
	r := testRule()
	r.Execution = "grafana"
	r.ForSeconds = 0
	r.KeepFiringForSeconds = 0
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		state, events, err := Transition(r, model.AlertRuntime{}, []Value{{Value: v}}, nil, 1000)
		require.NoError(t, err)
		require.Len(t, state.Instances, 1)
		assert.Equal(t, "Firing", state.Instances[0].State)
		assert.Nil(t, state.Instances[0].Value)
		assert.NotEmpty(t, state.Instances[0].ValueText)
		_, err = json.Marshal(state)
		require.NoError(t, err)
		_, err = json.Marshal(events)
		require.NoError(t, err)
	}
	r.NoDataState = "NoData"
	values := []Value{{Labels: map[string]string{"host": "a"}, Missing: true}, {Labels: map[string]string{"host": "b"}, Value: 0}}
	state, _, err := Transition(r, model.AlertRuntime{}, values, nil, 2000)
	require.NoError(t, err)
	assert.Equal(t, "nodata", state.Health)
	states := map[string]string{}
	for _, v := range state.Instances {
		states[v.Labels["host"]] = v.State
	}
	assert.Equal(t, map[string]string{"a": "NoData", "b": "Normal"}, states)
	previous, _, err := Transition(r, model.AlertRuntime{}, []Value{{Labels: map[string]string{"host": "a"}, Value: 1}}, nil, 3000)
	require.NoError(t, err)
	for policy, want := range map[string]string{"OK": "Normal", "Alerting": "Firing", "KeepLast": "Firing"} {
		r.NoDataState = policy
		state, _, err := Transition(r, previous, []Value{{Missing: true, Labels: map[string]string{"datasource_uid": "sdk", "ref_id": "A"}}}, nil, 4000)
		require.NoError(t, err)
		require.Len(t, state.Instances, 1)
		assert.Equal(t, want, state.Instances[0].State)
	}
	r.MissingSeriesEvaluations = 1
	previous, _, err = Transition(r, model.AlertRuntime{}, []Value{{Labels: map[string]string{"host": "a"}, Value: math.NaN(), Matches: json.RawMessage(`[{"value":1}]`)}}, nil, 5000)
	require.NoError(t, err)
	state, events, err := Transition(r, previous, []Value{{Labels: map[string]string{"host": "b"}, Value: 0}}, nil, 6000)
	require.NoError(t, err)
	for _, instance := range state.Instances {
		if instance.Labels["host"] == "a" {
			assert.Equal(t, "Normal", instance.State)
			assert.Equal(t, "MissingSeries", instance.Reason)
			assert.Nil(t, instance.Value)
			assert.Empty(t, instance.ValueText)
			assert.Empty(t, instance.Matches)
		}
	}
	require.Len(t, events, 1)
	assert.Empty(t, events[0].ValueText)
}

func TestGraphTimersAndNonFiniteStateSurviveRestartAndRecordingFailsSafely(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "graph.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	r, err := CompileGrafana(grafRule(t, `{"uid":"pure","title":"Pure graph","condition":"C","for":"10s","data":[{"refId":"C","datasourceUid":"__expr__","model":{"type":"math","expression":"nan()"}}]}`), 15)
	require.NoError(t, err)
	e := New(s, promcompat.New(s))
	_, err = e.SaveRule(ctx, r)
	require.NoError(t, err)
	first, err := e.Evaluate(ctx, r.UID, time.UnixMilli(100000))
	require.NoError(t, err)
	assert.Equal(t, "Pending", first.Runtime.Instances[0].State)
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	e = New(s, promcompat.New(s))
	second, err := e.Evaluate(ctx, r.UID, time.UnixMilli(110000))
	require.NoError(t, err)
	assert.Equal(t, "Firing", second.Runtime.Instances[0].State)
	assert.Equal(t, "NaN", second.Runtime.Instances[0].ValueText)
	annotations, err := s.Annotations(ctx, model.AnnotationQuery{})
	require.NoError(t, err)
	require.NotEmpty(t, annotations)
	assert.Contains(t, string(annotations[0].Data), `"valueText":"NaN"`)
	r.UID = "record"
	r.Record = "graph_record"
	r.ForSeconds = 0
	_, err = e.SaveRule(ctx, r)
	require.NoError(t, err)
	record, err := e.Evaluate(ctx, r.UID, time.UnixMilli(120000))
	require.NoError(t, err)
	assert.Equal(t, "error", record.Runtime.Health)
	assert.Contains(t, record.Runtime.Error, "finite")
	var samples int
	require.NoError(t, s.DB.QueryRow("SELECT count(*) FROM samples").Scan(&samples))
	assert.Zero(t, samples)
	r.UID = "finite-record"
	r.Record = "graph_finite"
	r.Grafana = json.RawMessage(`{"condition":"C","data":[{"refId":"C","datasourceUid":"__expr__","model":{"type":"math","expression":"466"}}]}`)
	_, err = e.SaveRule(ctx, r)
	require.NoError(t, err)
	finite, err := e.Evaluate(ctx, r.UID, time.UnixMilli(130000))
	require.NoError(t, err)
	assert.Equal(t, "ok", finite.Runtime.Health)
	require.NoError(t, s.DB.QueryRow("SELECT count(*) FROM samples").Scan(&samples))
	assert.Equal(t, 1, samples)
}
