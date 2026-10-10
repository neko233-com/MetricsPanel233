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

func sqlQuery(ref, sql string, alert bool) backend.DataQuery {
	format := "table"
	if alert {
		format = "alerting"
	}
	raw, _ := json.Marshal(queryModel{Type: "sql", Expression: sql, Format: format})
	return backend.DataQuery{RefID: ref, JSON: raw, TimeRange: backend.TimeRange{From: time.Unix(1000, 0), To: time.Unix(2000, 0)}}
}

func TestSQLGraphRetainsBackendTablesAndBatchesSources(t *testing.T) {
	frame := data.NewFrame("backend", data.NewField("host", nil, []string{"go", "mysql"}), data.NewField("memory", nil, []float64{200, 300}), data.NewField("budget", nil, []int64{100, 400}), data.NewField("online", nil, []bool{true, true}))
	frame.Meta = &data.FrameMeta{Type: data.FrameTypeTable, Channel: "ds/live/private"}
	groups := map[string][]backend.DataQuery{"sdk": {{RefID: "A"}}, "__expr__": {sqlQuery("Q", "SELECT host, memory > budget AS firing FROM A ORDER BY host", false)}}
	calls := 0
	r := Execute(context.Background(), groups, func(context.Context, string, []backend.DataQuery) (backend.Responses, string, error) {
		calls++
		return backend.Responses{"A": {Frames: data.Frames{frame}}}, "sdk", nil
	})
	require.NoError(t, r.Responses["Q"].Error)
	require.Len(t, r.Responses["Q"].Frames, 1)
	assert.Equal(t, 2, r.Responses["Q"].Frames[0].Rows())
	assert.Len(t, r.Responses["A"].Frames[0].Fields, 4)
	assert.Empty(t, r.Responses["A"].Frames[0].Meta.Channel)
	assert.Equal(t, "ds/live/private", frame.Meta.Channel)
	assert.Empty(t, frame.RefID)
	assert.Equal(t, 1, calls)
	groups["__expr__"][0] = sqlQuery("Q", "SELECT host, CAST(memory > budget AS SIGNED) AS firing FROM A ORDER BY host", true)
	r = Execute(context.Background(), groups, func(context.Context, string, []backend.DataQuery) (backend.Responses, string, error) {
		return backend.Responses{"A": {Frames: data.Frames{frame}}}, "sdk", nil
	})
	require.NoError(t, r.Responses["Q"].Error)
	require.Len(t, r.Responses["Q"].Frames, 2)
	assert.Equal(t, "go", r.Responses["Q"].Frames[0].Fields[0].Labels["host"])
	n, err := r.Responses["Q"].Frames[0].Fields[0].FloatAt(0)
	require.NoError(t, err)
	assert.Equal(t, float64(1), n)
}

func TestSQLTerminalRestrictionsAndIndependentErrors(t *testing.T) {
	for _, ops := range [][]backend.DataQuery{
		{sqlQuery("Q", "SELECT 1", false), sqlQuery("R", "SELECT 2", false)},
		{sqlQuery("Q", "SELECT * FROM M", false), {RefID: "M", JSON: json.RawMessage(`{"type":"math","expression":"233"}`)}},
		{sqlQuery("Q", "SELECT 1", false), {RefID: "M", JSON: json.RawMessage(`{"type":"math","expression":"$Q"}`)}},
	} {
		groups := map[string][]backend.DataQuery{"__expr__": ops}
		require.Error(t, ValidateSQLGraph(groups))
		r := Execute(context.Background(), groups, nil)
		for _, q := range ops {
			require.Error(t, r.Responses[q.RefID].Error)
		}
	}
	groups := map[string][]backend.DataQuery{"__expr__": {sqlQuery("Q", "SELECT * FROM Missing", false), {RefID: "M", JSON: json.RawMessage(`{"type":"math","expression":"233"}`)}}}
	r := Execute(context.Background(), groups, nil)
	require.ErrorContains(t, r.Responses["Q"].Error, "Missing")
	require.NoError(t, r.Responses["M"].Error)
	q := sqlQuery("Q", "SELECT 1", false)
	q.TimeRange = backend.TimeRange{}
	r = Execute(context.Background(), map[string][]backend.DataQuery{"__expr__": {q}}, nil)
	require.ErrorContains(t, r.Responses["Q"].Error, "time range")
}

func TestSQLAlertColumnAndLabelContracts(t *testing.T) {
	a := data.NewFrame("", data.NewField("host", nil, []*string{nil, new("mysql")}), data.NewField("value", nil, []*float64{nil, new(float64(233))}), data.NewField("clock", nil, []time.Time{time.Now(), time.Now()}), data.NewField("online", nil, []bool{true, true}))
	out, err := SQLAlertFrames("Q", a)
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Empty(t, out[0].Fields[0].Labels)
	n, err := out[0].Fields[0].FloatAt(0)
	require.NoError(t, err)
	assert.True(t, math.IsNaN(n))
	assert.Equal(t, data.Labels{"host": "mysql"}, out[1].Fields[0].Labels)
	for _, frame := range []*data.Frame{
		data.NewFrame("", data.NewField("host", nil, []string{"a", "a"}), data.NewField("value", nil, []float64{1, 2})),
		data.NewFrame("", data.NewField("host", nil, []string{"a"})),
		data.NewFrame("", data.NewField("a", nil, []float64{1}), data.NewField("b", nil, []float64{2})),
	} {
		_, err := SQLAlertFrames("Q", frame)
		require.Error(t, err)
	}
}
