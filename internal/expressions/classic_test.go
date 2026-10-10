package expressions

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func classicModel(t *testing.T, body string) operation {
	t.Helper()
	var m queryModel
	require.NoError(t, json.Unmarshal([]byte(body), &m))
	o, err := compile(m)
	require.NoError(t, err)
	return o
}

func TestClassicReducersIgnoreNullNaNAndPreserveLegacyDifferences(t *testing.T) {
	points := []*float64{nil, ptr(10), ptr(math.NaN()), ptr(30), ptr(20), nil}
	for reducer, want := range map[string]float64{
		"avg": 20, "sum": 60, "min": 10, "max": 30, "count": 6, "last": 20, "median": 20,
		"diff": 10, "diff_abs": 10, "percent_diff": 100, "percent_diff_abs": 100, "count_non_null": 3,
	} {
		t.Run(reducer, func(t *testing.T) {
			n, err := classicReduce(points, reducer, &budget{ctx: context.Background()})
			require.NoError(t, err)
			require.NotNil(t, n)
			assert.Equal(t, want, *n)
			n, err = classicReduce(nil, reducer, &budget{ctx: context.Background()})
			require.NoError(t, err)
			assert.Nil(t, n)
			n, err = classicReduce([]*float64{nil, ptr(math.NaN())}, reducer, &budget{ctx: context.Background()})
			require.NoError(t, err)
			if reducer == "count" {
				assert.Equal(t, float64(2), *n)
			} else {
				assert.Nil(t, n)
			}
		})
	}
	for reducer, want := range map[string]float64{"diff": -10, "diff_abs": 10, "percent_diff": -100, "percent_diff_abs": 100} {
		n, err := classicReduce([]*float64{ptr(-10), ptr(-20)}, reducer, &budget{ctx: context.Background()})
		require.NoError(t, err)
		assert.Equal(t, want, *n)
	}
	// The pinned classic contract differentiates a sole sample at position zero
	// from a sole zero following missing samples for percentage differences.
	n, err := classicReduce([]*float64{ptr(0)}, "percent_diff", &budget{ctx: context.Background()})
	require.NoError(t, err)
	assert.Equal(t, float64(0), *n)
	n, err = classicReduce([]*float64{nil, ptr(0)}, "percent_diff", &budget{ctx: context.Background()})
	require.NoError(t, err)
	assert.True(t, math.IsNaN(*n))
	n, err = classicReduce([]*float64{ptr(math.Inf(1))}, "count_non_null", &budget{ctx: context.Background()})
	require.NoError(t, err)
	assert.Equal(t, float64(1), *n)
	n, err = classicReduce([]*float64{ptr(math.Inf(1))}, "min", &budget{ctx: context.Background()})
	require.NoError(t, err)
	assert.Equal(t, math.MaxFloat64, *n)
	assert.Equal(t, float64(10), *points[1])
}

func TestClassicEvaluatorsReversedRangesAndNaNNumbers(t *testing.T) {
	for _, tc := range []struct {
		kind   string
		params []float64
		x      float64
		want   bool
	}{
		{"gt", []float64{2, 233}, 3, true}, {"lt", []float64{2}, 2, false},
		{"eq", []float64{2}, 2, true}, {"ne", []float64{2}, math.NaN(), true},
		{"gte", []float64{2}, 2, true}, {"lte", []float64{2}, 2, true},
		{"within_range", []float64{4, 2}, 3, true}, {"within_range", []float64{4, 2}, 2, false},
		{"outside_range", []float64{4, 2}, 5, true}, {"outside_range", []float64{4, 2}, 2, false},
		{"within_range_included", []float64{4, 2}, 2, true}, {"outside_range_included", []float64{4, 2}, 4, true},
	} {
		c := conditionModel{}
		c.Evaluator.Type = tc.kind
		c.Evaluator.Params = tc.params
		assert.Equal(t, tc.want, classicCompare(c, ptr(tc.x)), tc.kind)
		assert.False(t, classicCompare(c, nil), tc.kind)
	}
	c := conditionModel{}
	c.Evaluator.Type = "no_value"
	assert.True(t, classicCompare(c, nil))
	assert.False(t, classicCompare(c, ptr(math.NaN())))
}

func TestClassicSingleOutputMetadataNumbersAndNoData(t *testing.T) {
	op := classicModel(t, `{"type":"classic_conditions","conditions":[{"query":{"params":["A","5m","now"]},"reducer":{"type":"avg"},"evaluator":{"type":"gt","params":[15]}}]}`)
	input := values{
		{name: "orders", labels: data.Labels{"service": "结账"}, times: []time.Time{time.Unix(1, 0), time.Unix(2, 0)}, points: []*float64{ptr(10), ptr(30)}},
		{name: "orders", labels: data.Labels{"service": "other"}, points: []*float64{ptr(10)}},
	}
	out, err := op.execute(map[string]values{"A": input}, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Empty(t, out[0].labels)
	assert.Equal(t, float64(1), *out[0].points[0])
	frames := toFrames("C", out)
	require.Len(t, frames, 1)
	raw, err := json.Marshal(frames[0])
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"custom":[{"value":"20","metric":"orders","labels":{"service":"结账"}}]`)
	out[0].meta.([]classicMatch)[0].Labels["service"] = "changed"
	assert.Equal(t, "结账", input[0].labels["service"])
	// Classic reducers act only on series. An instant number remains 233 for count.
	op.model.Conditions[0].Reducer.Type = "count"
	out, err = op.execute(map[string]values{"A": {{name: "instant", points: []*float64{ptr(233)}}}}, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
	require.NoError(t, err)
	assert.Equal(t, "233", out[0].meta.([]classicMatch)[0].Value)
	for _, input := range []values{nil, {{points: []*float64{nil}}}, {{times: []time.Time{}, points: nil}}} {
		op.model.Conditions[0].Reducer.Type = "avg"
		out, err = op.execute(map[string]values{"A": input}, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
		require.NoError(t, err)
		assert.Nil(t, out[0].points[0])
		assert.Equal(t, "NoData", out[0].meta.([]classicMatch)[0].Metric)
		op.model.Conditions[0].Evaluator.Type = "no_value"
		out, err = op.execute(map[string]values{"A": input}, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
		require.NoError(t, err)
		assert.Equal(t, float64(1), *out[0].points[0])
		assert.Equal(t, "", out[0].meta.([]classicMatch)[0].Value)
		op.model.Conditions[0].Evaluator.Type = "gt"
	}
}

func TestClassicOrderedBooleanFoldAndLogicOrShortCircuit(t *testing.T) {
	base := `{"type":"classic_conditions","conditions":[{"query":{"params":["A"]},"reducer":{"type":"last"},"evaluator":{"type":"gt","params":[0]}},{"query":{"params":["B"]},"reducer":{"type":"last"},"operator":{"type":"or"},"evaluator":{"type":"gt","params":[0]}},{"query":{"params":["C"]},"reducer":{"type":"last"},"operator":{"type":"and"},"evaluator":{"type":"gt","params":[0]}}]}`
	op := classicModel(t, base)
	vars := map[string]values{"A": {{points: []*float64{ptr(1)}}}, "B": {{points: []*float64{ptr(0)}}}, "C": {{points: []*float64{ptr(0)}}}}
	out, err := op.execute(vars, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
	require.NoError(t, err)
	assert.Equal(t, float64(0), *out[0].points[0])
	op.model.Conditions[1].Operator.Type = "logic-or"
	vars["B"] = values{{scalar: true, points: []*float64{ptr(0)}}}
	out, err = op.execute(vars, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
	require.NoError(t, err)
	assert.Equal(t, float64(1), *out[0].points[0])
	require.Len(t, out[0].meta.([]classicMatch), 1)
	// Normal OR propagates NoData even with a firing sibling; AND does not.
	op.model.Conditions = op.model.Conditions[:2]
	op.model.Conditions[1].Operator.Type = "or"
	vars["B"] = nil
	out, err = op.execute(vars, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
	require.NoError(t, err)
	assert.Nil(t, out[0].points[0])
	op.model.Conditions[1].Operator.Type = "and"
	out, err = op.execute(vars, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
	require.NoError(t, err)
	assert.Equal(t, float64(0), *out[0].points[0])
}

func TestClassicGraphValidationCancellationAndChainedMatchNames(t *testing.T) {
	for _, body := range []string{
		`{"conditions":[{}]}`,
		`{"conditions":[{"query":{"params":["A"]},"reducer":{"type":"mean"}}]}`,
		`{"conditions":[{"query":{"params":["A"]},"reducer":{"type":"avg"},"evaluator":{"type":"gt"}}]}`,
		`{"conditions":[{"query":{"params":["A"]},"reducer":{"type":"avg"},"evaluator":{"type":"within_range","params":[1]}}]}`,
		`{"conditions":[{"query":{"params":["A"]},"reducer":{"type":"avg"},"evaluator":{"type":"bad"}}]}`,
	} {
		var model queryModel
		require.NoError(t, json.Unmarshal([]byte(body), &model))
		model.Type = "classic_conditions"
		_, err := compile(model)
		require.Error(t, err)
	}
	empty := classicModel(t, `{"type":"classic_conditions"}`)
	out, err := empty.execute(nil, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
	require.NoError(t, err)
	assert.Equal(t, float64(0), *out[0].points[0])
	assert.Empty(t, out[0].meta)
	op := classicModel(t, `{"type":"classic_conditions","conditions":[{"query":{"params":["A"]},"reducer":{"type":"avg"},"evaluator":{"type":"gt","params":[0]}}]}`)
	op.model.Conditions = append(op.model.Conditions, op.model.Conditions[0])
	op.model.Conditions[1].Operator.Type = "xor"
	_, err = compile(op.model)
	require.ErrorContains(t, err, "operator")
	_, err = classicReduce([]*float64{ptr(233)}, "avg", &budget{ctx: context.Background(), work: 2000000})
	require.ErrorContains(t, err, "computation limit")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = empty.execute(nil, time.Time{}, time.Time{}, &budget{ctx: ctx})
	require.ErrorIs(t, err, context.Canceled)
	q := func(ref, body string) backend.DataQuery {
		return backend.DataQuery{RefID: ref, JSON: json.RawMessage(body)}
	}
	groups := map[string][]backend.DataQuery{"source": {q("A", `{}`)}, "__expr__": {
		q("Alias", `{"type":"math","expression":"$A"}`),
		q("ClassicSource", `{"type":"classic_conditions","conditions":[{"query":{"params":["A"]},"reducer":{"type":"last"},"evaluator":{"type":"gt","params":[200]}}]}`),
		q("Classic", `{"type":"classic_conditions","conditions":[{"query":{"params":["Reduced"]},"reducer":{"type":"last"},"evaluator":{"type":"gt","params":[200]}}]}`),
		q("Reduced", `{"type":"reduce","expression":"A","reducer":"last"}`),
		q("Scalar", `{"type":"math","expression":"233"}`),
		q("Bad", `{"type":"classic_conditions","conditions":[{"query":{"params":["Scalar"]},"reducer":{"type":"last"},"evaluator":{"type":"gt","params":[200]}}]}`),
	}}
	result := Execute(context.Background(), groups, func(context.Context, string, []backend.DataQuery) (backend.Responses, string, error) {
		return backend.Responses{"A": {Frames: data.Frames{data.NewFrame("", data.NewField("source value", data.Labels{"host": "go"}, []float64{233}))}}}, "plugin", nil
	})
	require.NoError(t, result.Responses["Classic"].Error)
	assert.Equal(t, "Reduced", result.Responses["Classic"].Frames[0].Meta.Custom.([]classicMatch)[0].Metric)
	require.NoError(t, result.Responses["ClassicSource"].Error)
	assert.Equal(t, "source value", result.Responses["ClassicSource"].Frames[0].Meta.Custom.([]classicMatch)[0].Metric)
	assert.ErrorContains(t, result.Responses["Bad"].Error, "math scalar")
}
