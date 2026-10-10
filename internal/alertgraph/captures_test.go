package alertgraph

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/neko233-com/MetricsPanel233/internal/alerttemplates"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTemplateCapturesMatchInstancesAndPreferExactLabels(t *testing.T) {
	raw := json.RawMessage(`{"condition":"C","data":[{"refId":"A","datasourceUid":"sdk","model":{}},{"refId":"B","datasourceUid":"__expr__","model":{"type":"math","expression":"$A*2"}},{"refId":"C","datasourceUid":"__expr__","model":{"type":"math","expression":"$B>100"}}]}`)
	p, err := Parse(raw)
	require.NoError(t, err)
	first, second := 233.0, 466.0
	source := func(context.Context, string, []backend.DataQuery) (backend.Responses, string, error) {
		return backend.Responses{"A": {Frames: data.Frames{data.NewFrame("load", data.NewField("Value", data.Labels{"host": "a"}, []*float64{&first})), data.NewFrame("load", data.NewField("Value", data.Labels{"host": "b"}, []*float64{&second}))}}}, "sdk", nil
	}
	values, err := Execute(context.Background(), p, time.Now(), source)
	require.NoError(t, err)
	require.Len(t, values, 2)
	for _, v := range values {
		expected := first
		if v.Labels["host"] == "b" {
			expected = second
		}
		require.NotNil(t, v.Captures["A"].Value)
		assert.Equal(t, expected, *v.Captures["A"].Value)
		assert.Equal(t, expected*2, *v.Captures["B"].Value)
		assert.Equal(t, v.Labels["host"], v.Captures["A"].Labels["host"])
	}
	// Exact capture wins over unlabeled/subset candidates; absent exact matches
	// use stable label ordering so broad joins cannot flicker between evaluations.
	index, err := newCaptureIndex(context.Background(), p, backend.Responses{"A": {Frames: data.Frames{data.NewFrame("wide", data.NewField("Value", data.Labels{"host": "a", "zone": "z"}, []*float64{&second})), data.NewFrame("exact", data.NewField("Value", data.Labels{"host": "a"}, []*float64{&first}))}}})
	require.NoError(t, err)
	captures, _, err := index.match(map[string]string{"host": "a"})
	require.NoError(t, err)
	assert.Equal(t, first, *captures["A"].Value)
	_, _, err = index.match(map[string]string{})
	require.NoError(t, err)
}

func TestClassicTemplatesExposeIndexedMatchesIncludingNull(t *testing.T) {
	p, err := Parse(json.RawMessage(`{"condition":"K","data":[{"refId":"A","datasourceUid":"sdk","model":{}},{"refId":"K","datasourceUid":"__expr__","model":{"type":"classic_conditions","conditions":[{"query":{"params":["A"]},"reducer":{"type":"last"},"evaluator":{"type":"no_value","params":[]}}]}}]}`))
	require.NoError(t, err)
	values, err := Execute(context.Background(), p, time.Now(), func(context.Context, string, []backend.DataQuery) (backend.Responses, string, error) {
		return backend.Responses{"A": {Frames: data.Frames{data.NewFrame("empty", data.NewField("Value", data.Labels{"host": "mysql"}, []*float64{nil}))}}}, "sdk", nil
	})
	require.NoError(t, err)
	require.Len(t, values, 1)
	assert.Equal(t, float64(1), values[0].Value)
	assert.Empty(t, values[0].Labels)
	require.Contains(t, values[0].Captures, "K0")
	assert.NotContains(t, values[0].Captures, "A")
	assert.Nil(t, values[0].Captures["K0"].Value)
	assert.Equal(t, "mysql", values[0].Captures["K0"].Labels["host"])
	d := alerttemplates.NewData(nil, values[0].Captures, values[0].EvaluationString)
	assert.True(t, math.IsNaN(d.Values["K0"].Value))
	assert.Contains(t, d.Value, "value=null")
}
