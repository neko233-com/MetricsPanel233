package alerting

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/neko233-com/MetricsPanel233/internal/alertgraph"
	"github.com/neko233-com/MetricsPanel233/internal/querycontext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGraphPreviewSharesRangesHeadersAndQueriesEachSourceOnce(t *testing.T) {
	const raw = `{"condition":"C","data":[{"refId":"A","datasourceUid":"fake","relativeTimeRange":{"from":120,"to":5},"model":{"custom":233}},{"refId":"C","datasourceUid":"__expr__","model":{"type":"math","expression":"$A * 2"}}]}`
	p, err := alertgraph.Parse(json.RawMessage(raw))
	require.NoError(t, err)
	e := New(nil, nil)
	calls := 0
	e.GraphSource = func(ctx context.Context, uid string, queries []backend.DataQuery) (backend.Responses, string, error) {
		calls++
		assert.Equal(t, "fake", uid)
		require.Len(t, queries, 1)
		assert.Equal(t, map[string]string{"FromAlert": "true", "X-Cache-Skip": "true", "X-Grafana-Org-Id": "1"}, querycontext.Headers(ctx))
		assert.Equal(t, int64(1480000), queries[0].TimeRange.From.UnixMilli())
		assert.Equal(t, int64(1595000), queries[0].TimeRange.To.UnixMilli())
		return backend.Responses{"A": {Frames: data.Frames{data.NewFrame("A", data.NewField("Value", data.Labels{"host": "go"}, []float64{233}))}}}, "fake", nil
	}
	preview, err := e.PreviewGraph(context.Background(), p, time.UnixMilli(1600000))
	require.NoError(t, err)
	require.Empty(t, preview.Error)
	require.Equal(t, 1, calls)
	require.Len(t, preview.Values, 1)
	assert.Equal(t, 466.0, *preview.Values[0].Value)
	assert.True(t, *preview.Values[0].Satisfied)
	assert.Equal(t, map[string]string{"host": "go"}, preview.Values[0].Labels)
	assert.Contains(t, preview.Results, "A")
	assert.Contains(t, preview.Results, "C")
	assert.Equal(t, json.RawMessage(`{"custom":233}`), p.Data[0].Model)
}

func TestGraphPreviewNonFiniteNullAndConditionErrorsRemainJSONSafe(t *testing.T) {
	e := New(nil, nil)
	for _, expression := range []string{"nan()", "inf()", "infn()", "null()", "0"} {
		t.Run(expression, func(t *testing.T) {
			p, err := alertgraph.Parse(json.RawMessage(fmt.Sprintf(`{"condition":"C","data":[{"refId":"C","datasourceUid":"__expr__","model":{"type":"math","expression":%q}}]}`, expression)))
			require.NoError(t, err)
			preview, err := e.PreviewGraph(context.Background(), p, time.Now())
			require.NoError(t, err)
			require.Empty(t, preview.Error)
			encoded, err := json.Marshal(preview)
			require.NoError(t, err)
			assert.True(t, json.Valid(encoded))
			require.Len(t, preview.Values, 1)
			value := preview.Values[0]
			if expression == "null()" {
				assert.True(t, value.Missing)
				assert.Nil(t, value.Satisfied)
			} else if expression == "0" {
				assert.False(t, *value.Satisfied)
				assert.Equal(t, 0.0, *value.Value)
			} else {
				assert.True(t, *value.Satisfied)
				assert.Nil(t, value.Value)
				assert.NotEmpty(t, value.ValueText)
			}
		})
	}
	p, err := alertgraph.Parse(json.RawMessage(`{"condition":"C","data":[{"refId":"A","datasourceUid":"fake","model":{}},{"refId":"C","datasourceUid":"__expr__","model":{"type":"reduce","expression":"A","reducer":"mean"}}]}`))
	require.NoError(t, err)
	e.GraphSource = func(context.Context, string, []backend.DataQuery) (backend.Responses, string, error) {
		return backend.Responses{"A": {Error: fmt.Errorf("source failed")}}, "fake", nil
	}
	preview, err := e.PreviewGraph(context.Background(), p, time.Now())
	require.NoError(t, err)
	assert.Contains(t, preview.Error, "source failed")
	assert.Contains(t, preview.Results, "C")
}

func TestGraphPreviewIgnoresCachedRecoveryFingerprintsAndKeepsTheModel(t *testing.T) {
	labels := data.Labels{"host": "go"}
	const prefix = `{"condition":"C","data":[{"refId":"A","datasourceUid":"fake","model":{}},{"refId":"C","datasourceUid":"__expr__","model":{"type":"threshold","expression":"A","conditions":[{"evaluator":{"type":"gt","params":[100]},"unloadEvaluator":{"type":"lt","params":[80]}}],"loadedFingerprints":[`
	raw := json.RawMessage(prefix + fmt.Sprintf(`"%d"]}}]}`, uint64(labels.Fingerprint())))
	p, err := alertgraph.Parse(raw)
	require.NoError(t, err)
	original := append(json.RawMessage(nil), p.Data[1].Model...)
	e := New(nil, nil)
	e.GraphSource = func(context.Context, string, []backend.DataQuery) (backend.Responses, string, error) {
		return backend.Responses{"A": {Frames: data.Frames{data.NewFrame("A", data.NewField("Value", labels, []float64{90}))}}}, "fake", nil
	}
	preview, err := e.PreviewGraph(context.Background(), p, time.Now())
	require.NoError(t, err)
	require.Empty(t, preview.Error)
	require.Len(t, preview.Values, 1)
	assert.Equal(t, 0.0, *preview.Values[0].Value)
	assert.False(t, *preview.Values[0].Satisfied)
	assert.Equal(t, original, p.Data[1].Model)
}

func TestGraphPreviewCapacityAndCancellationReleaseEvaluationSlots(t *testing.T) {
	p, err := alertgraph.Parse(json.RawMessage(`{"condition":"C","data":[{"refId":"A","datasourceUid":"fake","model":{}},{"refId":"C","datasourceUid":"__expr__","model":{"type":"reduce","expression":"A","reducer":"mean"}}]}`))
	require.NoError(t, err)
	e := New(nil, nil)
	var active atomic.Int64
	started, release := make(chan struct{}, 4), make(chan struct{})
	e.GraphSource = func(ctx context.Context, _ string, _ []backend.DataQuery) (backend.Responses, string, error) {
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, "fake", ctx.Err()
		}
		return backend.Responses{"A": {Frames: data.Frames{data.NewFrame("A", data.NewField("Value", nil, []float64{233}))}}}, "fake", nil
	}
	results := make(chan error, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for range 4 {
		go func() { _, err := e.PreviewGraph(ctx, p, time.Now()); results <- err }()
	}
	for range 4 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("preview failed to occupy the shared slots")
		}
	}
	assert.Equal(t, int64(4), active.Load())
	queued, stop := context.WithCancel(context.Background())
	stop()
	_, err = e.PreviewGraph(queued, p, time.Now())
	require.ErrorIs(t, err, context.Canceled)
	cancel()
	for range 4 {
		require.ErrorIs(t, <-results, context.Canceled)
	}
	assert.Zero(t, active.Load())
	assert.Empty(t, e.slots)
	close(release)
	preview, err := e.PreviewGraph(context.Background(), p, time.Now())
	require.NoError(t, err)
	assert.Empty(t, preview.Error)
	assert.Empty(t, e.slots)
}
