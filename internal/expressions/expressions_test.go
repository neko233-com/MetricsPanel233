package expressions

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mathResult(t *testing.T, expression string, vars map[string]values) values {
	t.Helper()
	root, _, err := parseMath(expression)
	require.NoError(t, err)
	result, err := root.evaluate(vars, &budget{ctx: context.Background()})
	require.NoError(t, err)
	return result
}
func TestMathSyntaxFunctionsNullAndNonFinite(t *testing.T) {
	for _, test := range []struct {
		input string
		want  float64
	}{
		{"0xFE+072+2.24e2", 536}, {"2 ** 3 ** 2", 64}, {"-2 ** 2", 4},
		{"(5 + 3) * 2 % 7", 2}, {"!0 && (3 >= 2 || 0)", 1},
		{"ceil(-0.4) + floor(1.5) + round(-1.5)", -1},
		{"is_nan(nan()) + is_inf(infn()) + is_null(null()) + is_number(233)", 4},
		{"abs(-233)", 233}, {"log(1)", 0},
	} {
		t.Run(test.input, func(t *testing.T) { require.InDelta(t, test.want, *mathResult(t, test.input, nil)[0].points[0], 1e-10) })
	}
	assert.Nil(t, mathResult(t, "null() + 233", nil)[0].points[0])
	assert.True(t, math.IsNaN(*mathResult(t, "nan() == 0", nil)[0].points[0]))
	assert.True(t, math.IsNaN(*mathResult(t, "abs(null())", nil)[0].points[0]))
	assert.Equal(t, float64(0), *mathResult(t, "is_number(inf())", nil)[0].points[0])
	result := mathResult(t, "${my variable} / $B", map[string]values{"my variable": {{points: []*float64{ptr(466)}}}, "B": {{points: []*float64{ptr(2)}}}})
	require.Equal(t, float64(233), *result[0].points[0])
	for _, invalid := range []string{"", "$", "2 +", "unknown(2)", "abs(1,2)", "1e999", "1;exit", "$A[0]", "abs(2", "0xZZ", "1 |||| 2"} {
		_, _, err := parseMath(invalid)
		require.Error(t, err, invalid)
	}
	_, _, err := parseMath("$A + 2")
	require.NoError(t, err)
	root, _, _ := parseMath("$Missing")
	_, err = root.evaluate(nil, &budget{ctx: context.Background()})
	require.ErrorContains(t, err, "missing")
}
func TestMathJoinsIntersectTimestampsAndNeverMutateInputs(t *testing.T) {
	a := value{labels: data.Labels{"host": "a", "dc": "cn"}, times: []time.Time{time.UnixMilli(1000), time.UnixMilli(2000), time.UnixMilli(3000)}, points: []*float64{ptr(1), ptr(2), ptr(3)}}
	b := value{labels: data.Labels{"host": "a"}, times: []time.Time{time.UnixMilli(2000), time.UnixMilli(3000), time.UnixMilli(4000)}, points: []*float64{ptr(10), nil, ptr(30)}}
	output := mathResult(t, "$A + $B", map[string]values{"A": {a}, "B": {b}})
	require.Len(t, output, 1)
	assert.Equal(t, a.labels, output[0].labels)
	assert.Equal(t, []time.Time{time.UnixMilli(2000), time.UnixMilli(3000)}, output[0].times)
	assert.Equal(t, float64(12), *output[0].points[0])
	assert.Nil(t, output[0].points[1])
	assert.Equal(t, float64(2), *a.points[1])
	assert.Equal(t, float64(10), *b.points[0])
	output[0].labels["host"] = "modified"
	assert.Equal(t, "a", a.labels["host"])
	mismatch := value{labels: data.Labels{"host": "b"}, points: []*float64{ptr(9)}}
	fallback := mathResult(t, "$A * $B", map[string]values{"A": {a}, "B": {mismatch}})
	require.Len(t, fallback, 1)
	assert.Empty(t, fallback[0].labels)
	scalar := mathResult(t, "$A / 2", map[string]values{"A": {a}})
	assert.Equal(t, float64(1.5), *scalar[0].points[2])
	unmatched := mathResult(t, "$A + $B", map[string]values{"A": {a, {labels: data.Labels{"host": "c"}, points: []*float64{ptr(1)}}}, "B": {mismatch}})
	assert.Empty(t, unmatched)
}

func TestIndexedJoinSupportsTenThousandMatchingDimensions(t *testing.T) {
	left, right := make(values, MaxItems), make(values, MaxItems)
	for i := 0; i < MaxItems; i++ {
		left[i] = value{labels: data.Labels{"host": strconv.Itoa(i)}, points: []*float64{ptr(233)}}
		right[i] = value{labels: data.Labels{"host": strconv.Itoa(i)}, points: []*float64{ptr(2)}}
	}
	b := &budget{ctx: context.Background()}
	output, err := binaryValues(left, right, "*", b)
	require.NoError(t, err)
	require.Len(t, output, MaxItems)
	assert.Equal(t, float64(466), *output[MaxItems-1].points[0])
}

func TestThresholdSeriesScalarBoundariesAndRecovery(t *testing.T) {
	var model queryModel
	require.NoError(t, json.Unmarshal([]byte(`{"type":"threshold","expression":"A","conditions":[{"evaluator":{"type":"within_range_included","params":[2,4]}}]}`), &model))
	operation, err := compile(model)
	require.NoError(t, err)
	series := value{times: []time.Time{time.Unix(1, 0), time.Unix(2, 0), time.Unix(3, 0)}, points: []*float64{ptr(2), nil, ptr(5)}}
	output, err := operation.execute(map[string]values{"A": {series}}, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
	require.NoError(t, err)
	assert.Equal(t, series.times, output[0].times)
	assert.Equal(t, []*float64{ptr(1), nil, ptr(0)}, output[0].points)
	scalar := value{scalar: true, points: []*float64{ptr(4)}}
	output, err = operation.execute(map[string]values{"A": {scalar}}, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
	require.NoError(t, err)
	assert.True(t, output[0].scalar)
	assert.Equal(t, float64(1), *output[0].points[0])
	require.NoError(t, json.Unmarshal([]byte(`{"type":"threshold","expression":"A","conditions":[{"evaluator":{"type":"gt","params":[233]},"unloadEvaluator":{"type":"lt","params":[200]}}]}`), &model))
	_, err = compile(model)
	require.NoError(t, err)
}
func TestReduceAllFunctionsAndModes(t *testing.T) {
	clean := value{times: []time.Time{time.Unix(1, 0), time.Unix(2, 0), time.Unix(3, 0), time.Unix(4, 0)}, points: []*float64{ptr(1), ptr(2), ptr(3), ptr(4)}, labels: data.Labels{"host": "go"}}
	for function, want := range map[string]float64{"sum": 10, "mean": 2.5, "min": 1, "max": 4, "count": 4, "last": 4, "median": 2.5} {
		op, err := compile(queryModel{Type: "reduce", Expression: "A", Reducer: function})
		require.NoError(t, err)
		result, err := op.execute(map[string]values{"A": {clean}}, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
		require.NoError(t, err)
		require.Len(t, result, 1)
		assert.Nil(t, result[0].times)
		assert.Equal(t, want, *result[0].points[0])
		assert.Equal(t, clean.labels, result[0].labels)
	}
	dirty := clean
	dirty.points = []*float64{nil, ptr(math.NaN()), ptr(math.Inf(1)), ptr(4)}
	for mode, want := range map[string]float64{"dropNN": 4, "replaceNN": 10} {
		m := queryModel{Type: "reduce", Expression: "$A", Reducer: "sum"}
		m.Settings = &struct {
			Mode    string   `json:"mode"`
			Replace *float64 `json:"replaceWithValue"`
		}{mode, ptr(2)}
		op, err := compile(m)
		require.NoError(t, err)
		result, err := op.execute(map[string]values{"A": {dirty}}, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
		require.NoError(t, err)
		assert.Equal(t, want, *result[0].points[0])
	}
	strict, _ := compile(queryModel{Type: "reduce", Expression: "A", Reducer: "mean"})
	result, err := strict.execute(map[string]values{"A": {dirty}}, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
	require.NoError(t, err)
	assert.True(t, math.IsNaN(*result[0].points[0]))
	legacyStrict := queryModel{Type: "reduce", Expression: "A", Reducer: "mean"}
	require.NoError(t, json.Unmarshal([]byte(`{"type":"reduce","expression":"A","reducer":"mean","settings":{"mode":"strict"}}`), &legacyStrict))
	strictAlias, err := compile(legacyStrict)
	require.NoError(t, err)
	result, err = strictAlias.execute(map[string]values{"A": {dirty}}, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
	require.NoError(t, err)
	assert.True(t, math.IsNaN(*result[0].points[0]))
	// Numbers remain numbers for every reducer; count must not turn an instant 233 into 1.
	for function := range reducers {
		op, _ := compile(queryModel{Type: "reduce", Expression: "A", Reducer: function})
		result, err := op.execute(map[string]values{"A": {{points: []*float64{ptr(233)}}}}, time.Time{}, time.Time{}, &budget{ctx: context.Background()})
		require.NoError(t, err)
		assert.Equal(t, float64(233), *result[0].points[0])
	}
	assert.True(t, math.IsNaN(*reduced(nil, "mean")))
	assert.Equal(t, float64(0), *reduced(nil, "sum"))
}
func TestResampleInclusiveWindowAndFillModes(t *testing.T) {
	input := value{times: []time.Time{time.UnixMilli(500), time.UnixMilli(1500), time.UnixMilli(1700)}, points: []*float64{ptr(1), ptr(3), ptr(5)}}
	for mode, want := range map[string][]*float64{
		"pad":         {nil, ptr(1), ptr(4), ptr(5)},
		"backfilling": {ptr(1), ptr(1), ptr(4), nil},
		"fillna":      {nil, ptr(1), ptr(4), nil},
	} {
		op, err := compile(queryModel{Type: "resample", Expression: "A", Window: "1s", Downsampler: "mean", Upsampler: mode})
		require.NoError(t, err)
		output, err := op.execute(map[string]values{"A": {input}}, time.UnixMilli(0), time.UnixMilli(3000), &budget{ctx: context.Background()})
		require.NoError(t, err)
		require.Len(t, output[0].times, 4)
		assert.Equal(t, time.UnixMilli(3000), output[0].times[3])
		assert.Equal(t, want, output[0].points)
	}
	_, err := compile(queryModel{Type: "resample", Expression: "A", Window: "0s"})
	require.Error(t, err)
}
func TestFrameConversionVectorsNumericTablesAndInvalidShapes(t *testing.T) {
	table := data.NewFrame("sql", data.NewField("Value", nil, []float64{233, 234}), data.NewField("host", nil, []string{"a", "b"}))
	parsed, err := fromFrames(data.Frames{table}, "sql", &budget{ctx: context.Background()})
	require.NoError(t, err)
	require.Len(t, parsed, 2)
	assert.Nil(t, parsed[0].times)
	assert.Equal(t, "a", parsed[0].labels["host"])
	assert.Equal(t, float64(233), *parsed[0].points[0])
	vector := data.NewFrame("instant", data.NewField("Time", nil, []time.Time{time.Unix(1, 0)}), data.NewField("Value", data.Labels{"host": "a"}, []float64{233}))
	vector.Meta = &data.FrameMeta{Custom: map[string]string{"resultType": "vector"}}
	parsed, err = fromFrames(data.Frames{vector}, "prometheus", &budget{ctx: context.Background()})
	require.NoError(t, err)
	assert.Nil(t, parsed[0].times)
	for _, bad := range (data.Frames{data.NewFrame("bad", data.NewField("One", nil, []float64{1}), data.NewField("Two", nil, []float64{2})), data.NewFrame("bad", data.NewField("Time", nil, []time.Time{time.Unix(1, 0), time.Unix(1, 0)}), data.NewField("Value", nil, []float64{1, 2}))}) {
		_, err = fromFrames(data.Frames{bad}, "sdk", &budget{ctx: context.Background()})
		require.Error(t, err)
	}
	for _, literal := range []string{"nan()", "inf()", "null()"} {
		output := toFrames("A", mathResult(t, literal, nil))
		encoded, err := json.Marshal(output)
		require.NoError(t, err)
		require.True(t, json.Valid(encoded))
		var roundtrip data.Frames
		require.NoError(t, json.Unmarshal(encoded, &roundtrip))
		require.Len(t, roundtrip, 1)
	}
}
func TestGraphOrderingCyclesErrorsNoDataAndBudgets(t *testing.T) {
	query := func(ref, model string) backend.DataQuery {
		return backend.DataQuery{RefID: ref, JSON: json.RawMessage(model), TimeRange: backend.TimeRange{From: time.UnixMilli(0), To: time.UnixMilli(3000)}}
	}
	calls := 0
	source := func(_ context.Context, _ string, queries []backend.DataQuery) (backend.Responses, string, error) {
		calls++
		results := backend.Responses{}
		for _, q := range queries {
			frame := data.NewFrame("sdk", data.NewField("Value", nil, []float64{233}))
			frame.Meta = &data.FrameMeta{Channel: "ds/source/counter"}
			results[q.RefID] = backend.DataResponse{Frames: data.Frames{frame}}
		}
		return results, "sdk", nil
	}
	groups := map[string][]backend.DataQuery{"source": {query("A", "{}")}, "__expr__": {
		query("C", `{"type":"math","expression":"$B * 2"}`), query("B", `{"type":"reduce","expression":"A","reducer":"last"}`),
		query("Missing", `{"type":"math","expression":"$NotThere"}`), query("X", `{"type":"math","expression":"$Y"}`), query("Y", `{"type":"math","expression":"$X"}`),
		query("Bad", `{"type":"math","expression":"1 +"}`), query("Dependent", `{"type":"math","expression":"$Bad + 1"}`),
	}}
	result := Execute(context.Background(), groups, source)
	assert.Equal(t, 1, calls)
	assert.Empty(t, result.Responses["A"].Frames[0].Meta.Channel, "expression snapshots must not open transient Live subscriptions")
	require.NoError(t, result.Responses["C"].Error)
	assert.Equal(t, float64(466), *result.Responses["C"].Frames[0].Fields[0].At(0).(*float64))
	assert.ErrorContains(t, result.Responses["Missing"].Error, "missing query reference")
	assert.ErrorContains(t, result.Responses["X"].Error, "cyclic")
	assert.ErrorContains(t, result.Responses["Y"].Error, "cyclic")
	assert.ErrorContains(t, result.Responses["Dependent"].Error, "dependency Bad")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	result = Execute(cancelled, groups, source)
	assert.ErrorIs(t, result.Responses["C"].Error, context.Canceled)
	root, _, err := parseMath("$A * $B")
	require.NoError(t, err)
	budgetLimit := &budget{ctx: context.Background(), points: MaxPoints}
	_, err = root.evaluate(map[string]values{"A": {{points: []*float64{ptr(233)}}}, "B": {{points: []*float64{ptr(2)}}}}, budgetLimit)
	require.ErrorContains(t, err, "working points")
	// An empty query propagates NoData through math instead of treating it as zero.
	output := mathResult(t, "$Empty + 233", map[string]values{"Empty": nil})
	assert.Empty(t, output)
}
