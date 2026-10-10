package alerting

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/promcompat"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTemplatesRenderPerInstanceAndKeepWarningsOutOfAlertTruth(t *testing.T) {
	r := testRule()
	r.ForSeconds = 0
	r.Labels = map[string]string{"team": "operations", "severity": `{{ if ge $value 400.0 }}critical{{ else }}warning{{ end }}`}
	r.Annotations = map[string]string{"summary": `{{ $labels.host }} has {{ printf "%.0f" $values.A.Value }} requests`, "description": `{{ $labels.team }} / {{ $labels.alertname }}`, "bad": `{{ invalidFunction }}`}
	state, events, err := Transition(r, model.AlertRuntime{}, []Value{{Labels: map[string]string{"host": "go"}, Value: 233}, {Labels: map[string]string{"host": "mysql"}, Value: 466}}, nil, 100000)
	require.NoError(t, err)
	require.Len(t, state.Instances, 2)
	require.Len(t, events, 2)
	for _, v := range state.Instances {
		assert.Equal(t, "Firing", v.State)
		severity := "warning"
		count := "233"
		if v.Labels["host"] == "mysql" {
			severity = "critical"
			count = "466"
		}
		assert.Equal(t, severity, v.Labels["severity"])
		assert.Equal(t, v.Labels["host"]+" has "+count+" requests", v.Annotations["summary"])
		assert.Equal(t, "[no value] / "+r.Title, v.Annotations["description"])
		assert.Equal(t, r.Annotations["bad"], v.Annotations["bad"])
		require.Len(t, v.TemplateErrors, 1)
	}
	assert.Equal(t, "ok", state.Health)
	assert.Equal(t, r.Annotations["summary"], `{{ $labels.host }} has {{ printf "%.0f" $values.A.Value }} requests`)
	r.NoDataState = "KeepLast"
	next, _, err := Transition(r, state, nil, nil, 101000)
	require.NoError(t, err)
	assert.Equal(t, state.Instances[0].Annotations, next.Instances[0].Annotations)
}

func TestRenderedAnnotationsTimersAndHistorySurviveRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "templates.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	r, err := CompileGrafana(grafRule(t, `{"uid":"templated","title":"Templated","condition":"C","for":"10s","labels":{"severity":"{{ if gt $values.A.Value 200.0 }}critical{{ else }}warning{{ end }}"},"annotations":{"summary":"{{ printf \"%.0f\" $values.A.Value }} requests / {{ $value }}","description":"{{ $labels.severity }}"},"data":[{"refId":"A","datasourceUid":"metricspanel","model":{"expr":"vector(233)","instant":true}},{"refId":"C","datasourceUid":"__expr__","model":{"type":"math","expression":"$A>200"}}]}`), 30)
	require.NoError(t, err)
	// The engine's real built-in datasource callback is covered by server tests;
	// this pure expression source makes persistence timestamps deterministic.
	r.Grafana = json.RawMessage(`{"condition":"A","data":[{"refId":"A","datasourceUid":"__expr__","model":{"type":"math","expression":"233"}}]}`)
	e := New(s, promcompat.New(s))
	_, err = e.SaveRule(ctx, r)
	require.NoError(t, err)
	first, err := e.Evaluate(ctx, r.UID, time.UnixMilli(100000))
	require.NoError(t, err)
	require.Len(t, first.Runtime.Instances, 1)
	assert.Equal(t, "Pending", first.Runtime.Instances[0].State)
	assert.Equal(t, "critical", first.Runtime.Instances[0].Labels["severity"])
	assert.Contains(t, first.Runtime.Instances[0].Annotations["summary"], "233 requests")
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	e = New(s, promcompat.New(s))
	second, err := e.Evaluate(ctx, r.UID, time.UnixMilli(110000))
	require.NoError(t, err)
	assert.Equal(t, "Firing", second.Runtime.Instances[0].State)
	assert.Equal(t, first.Runtime.Instances[0].Annotations, second.Runtime.Instances[0].Annotations)
	history, err := s.AlertHistory(ctx, r.UID, 10)
	require.NoError(t, err)
	require.Len(t, history, 2)
	assert.Equal(t, second.Runtime.Instances[0].Annotations, history[0].Annotations)
	annotations, err := s.Annotations(ctx, model.AnnotationQuery{})
	require.NoError(t, err)
	require.NotEmpty(t, annotations)
	assert.Contains(t, string(annotations[0].Data), `"summary":"233 requests`)
}
