package alertgraph

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const graph = `{"condition":"C","data":[{"refId":"C","datasourceUid":"__expr__","model":{"type":"classic_conditions","conditions":[{"query":{"params":["D"]},"reducer":{"type":"avg"},"evaluator":{"type":"gt","params":[400]}},{"query":{"params":["A"]},"reducer":{"type":"last"},"operator":{"type":"and"},"evaluator":{"type":"gt","params":[200]}}]}},{"refId":"D","datasourceUid":"Expression","model":{"type":"math","expression":"$A*$B"}},{"refId":"A","datasourceUid":"prometheus","relativeTimeRange":{"from":60,"to":10},"model":{"expr":"vector(233)","instant":true,"intervalMs":1000,"maxDataPoints":43200}},{"refId":"B","datasourceUid":"sdk","model":{"value":2}}]}`

func TestValidatedMixedGraphScheduledRangesAndClassicMatches(t *testing.T) {
	p, err := Parse(json.RawMessage(graph))
	require.NoError(t, err)
	assert.Equal(t, "metricspanel", p.Data[2].DatasourceUID)
	assert.Equal(t, "__expr__", p.Data[1].DatasourceUID)
	at := time.Unix(100000, 0)
	groups := p.Groups(at)
	assert.Equal(t, at.Add(-time.Minute), groups["metricspanel"][0].TimeRange.From)
	assert.Equal(t, at.Add(-10*time.Second), groups["metricspanel"][0].TimeRange.To)
	assert.Equal(t, int64(43200), groups["metricspanel"][0].MaxDataPoints)
	calls := map[string]int{}
	out, err := Execute(context.Background(), p, at, func(_ context.Context, uid string, queries []backend.DataQuery) (backend.Responses, string, error) {
		calls[uid]++
		r := backend.Responses{}
		for _, q := range queries {
			v := 2.0
			if uid == "metricspanel" {
				v = 233
			}
			r[q.RefID] = backend.DataResponse{Frames: data.Frames{data.NewFrame("", data.NewField("temperature", data.Labels{"host": "go"}, []float64{v}))}}
		}
		return r, "backend", nil
	})
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, float64(1), out[0].Value)
	assert.Empty(t, out[0].Labels)
	assert.False(t, out[0].Missing)
	assert.Equal(t, map[string]int{"metricspanel": 1, "sdk": 1}, calls)
	assert.Contains(t, string(out[0].Matches), `"metric":"D"`)
	assert.Contains(t, string(out[0].Matches), `"value":"466"`)
	assert.Contains(t, string(out[0].Matches), `"host":"go"`)
}

func TestGraphMissingReferencesCyclesInvalidExpressionsAndLimits(t *testing.T) {
	for _, body := range []string{
		`null`, `{}`, `{"condition":"A","data":[]}`,
		`{"orgID":2,"condition":"A","data":[{"refId":"A","datasourceUid":"sdk","model":{}}]}`,
		`{"notification_settings":{"receiver":"unconfigured"},"condition":"A","data":[{"refId":"A","datasourceUid":"sdk","model":{}}]}`,
		`{"record":{"from":"A","target_datasource_uid":"other"},"data":[{"refId":"A","datasourceUid":"sdk","model":{}}]}`,
		`{"condition":"A","data":[{"refId":"A","datasourceUid":"__expr__","model":{"type":"math","expression":"$Missing"}}]}`,
		`{"condition":"A","data":[{"refId":"A","datasourceUid":"__expr__","model":{"type":"math","expression":"$B"}},{"refId":"B","datasourceUid":"__expr__","model":{"type":"reduce","expression":"A","reducer":"mean"}}]}`,
		`{"condition":"A","data":[{"refId":"A","datasourceUid":"__expr__","model":{"type":"sql","expression":"delete from X"}}]}`,
		`{"condition":"A","data":[{"refId":"A","datasourceUid":"metricspanel","model":{"expr":"rate("}}]}`,
		`{"condition":"A","data":[{"refId":"A","datasourceUid":"sdk","relativeTimeRange":{"from":0,"to":1},"model":{}}]}`,
		`{"condition":"A","data":[{"refId":"A","datasourceUid":"sdk","relativeTimeRange":{"from":2678401},"model":{}}]}`,
		`{"condition":"A","data":[{"refId":"A","datasourceUid":"sdk","model":{"intervalMs":-1}}]}`,
		`{"condition":"A","data":[{"refId":"A","datasourceUid":"sdk","model":{"maxDataPoints":1000001}}]}`,
		`{"condition":"A","data":[{"refId":"A","datasourceUid":"sdk","model":null}]}`,
		`{"condition":"A","data":[{"refId":"A","model":{}}]}`,
		`{"condition":"A","data":[{"refId":"A","datasourceUid":"sdk","model":{}},{"refId":"A","datasourceUid":"sdk","model":{}}]}`,
	} {
		_, err := Parse(json.RawMessage(body))
		require.Error(t, err, body)
	}
	_, err := Parse(json.RawMessage(strings.Repeat(" ", 512<<10+1)))
	require.Error(t, err)
	// SDK expr fields can hold a query language other than PromQL.
	p, err := Parse(json.RawMessage(`{"record":{"from":"A"},"data":[{"refId":"A","datasourceUid":"sdk","model":{"expr":"custom query"}}]}`))
	require.NoError(t, err)
	assert.Equal(t, "A", p.Condition)
}

func TestGraphNullableDimensionsNonFiniteNumbersAndReducedFormat(t *testing.T) {
	p, err := Parse(json.RawMessage(`{"condition":"A","data":[{"refId":"A","datasourceUid":"sdk","model":{}}]}`))
	require.NoError(t, err)
	for _, n := range []*float64{nil, new(math.NaN()), new(math.Inf(1)), new(0.0)} {
		out, err := Execute(context.Background(), p, time.Now(), func(context.Context, string, []backend.DataQuery) (backend.Responses, string, error) {
			return backend.Responses{"A": {Frames: data.Frames{data.NewFrame("", data.NewField("value", data.Labels{"host": "a"}, []*float64{n}))}}}, "sdk", nil
		})
		require.NoError(t, err)
		require.Len(t, out, 1)
		assert.Equal(t, n == nil, out[0].Missing)
		assert.Equal(t, "a", out[0].Labels["host"])
		if n != nil && math.IsNaN(*n) {
			assert.True(t, math.IsNaN(out[0].Value))
		}
	}
	_, err = Execute(context.Background(), p, time.Now(), func(context.Context, string, []backend.DataQuery) (backend.Responses, string, error) {
		return backend.Responses{"A": {Frames: data.Frames{data.NewFrame("", data.NewField("Time", nil, []time.Time{time.Now()}), data.NewField("value", nil, []float64{233}))}}}, "sdk", nil
	})
	require.ErrorContains(t, err, "reduced numeric")
}

func TestDatasourceNoDataPriorityErrorIsolationAndCancellation(t *testing.T) {
	p, err := Parse(json.RawMessage(`{"condition":"C","data":[{"refId":"C","datasourceUid":"__expr__","model":{"type":"math","expression":"1"}},{"refId":"A","datasourceUid":"sdk","model":{}},{"refId":"B","datasourceUid":"sdk","model":{}}]}`))
	require.NoError(t, err)
	out, err := Execute(context.Background(), p, time.Now(), func(context.Context, string, []backend.DataQuery) (backend.Responses, string, error) {
		return backend.Responses{"A": {}, "B": {Error: errors.New("unrelated")}}, "sdk", nil
	})
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.True(t, out[0].Missing)
	assert.Equal(t, map[string]string{"datasource_uid": "sdk", "ref_id": "A,B"}, out[0].Labels)
	p.Data[0].Model = json.RawMessage(`{"type":"math","expression":"$A"}`)
	_, err = Execute(context.Background(), p, time.Now(), func(context.Context, string, []backend.DataQuery) (backend.Responses, string, error) {
		return nil, "sdk", errors.New("backend down")
	})
	require.ErrorContains(t, err, "backend down")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Execute(ctx, p, time.Now(), nil)
	require.ErrorIs(t, err, context.Canceled)
}
