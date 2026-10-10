package sqlframes

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueryJoinsCTEsWindowsAndExactIntegers(t *testing.T) {
	a := data.NewFrame("A", data.NewField("host", nil, []string{"a", "b"}), data.NewField("value", nil, []float64{90, 240}), data.NewField("id", nil, []uint64{14695981039346656037, math.MaxUint64}))
	b := data.NewFrame("B", data.NewField("host", nil, []string{"b", "a"}), data.NewField("factor", nil, []int64{2, 3}))
	frame, err := Query(context.Background(), "C", `WITH joined AS (SELECT A.host, A.id, A.value * B.factor AS total FROM A JOIN B ON A.host=B.host) SELECT host, id, total, ROW_NUMBER() OVER (ORDER BY total DESC) AS rn FROM joined ORDER BY rn`, map[string]data.Frames{"A": {a}, "B": {b}}, time.Unix(233, 0))
	require.NoError(t, err)
	require.Equal(t, 2, frame.Rows())
	assert.Equal(t, "b", frame.Fields[0].At(0))
	assert.Equal(t, uint64(math.MaxUint64), frame.Fields[1].At(0))
	assert.Equal(t, uint64(14695981039346656037), frame.Fields[1].At(1))
	n, err := frame.Fields[2].FloatAt(0)
	require.NoError(t, err)
	assert.Equal(t, float64(480), n)
	assert.Empty(t, a.RefID)
	assert.Equal(t, uint64(14695981039346656037), a.Fields[2].At(0))
}

func TestQueryPrimitiveColumnsNullsAndFunctions(t *testing.T) {
	at := time.Unix(1790000000, 0).UTC()
	text := "hello"
	f := data.NewFrame("", data.NewField("i8", nil, []int8{-8, 8}), data.NewField("u8", nil, []uint8{8, 255}), data.NewField("i16", nil, []int16{-16, 16}), data.NewField("u16", nil, []uint16{16, 65535}), data.NewField("i32", nil, []int32{-32, 32}), data.NewField("u32", nil, []uint32{32, math.MaxUint32}), data.NewField("i64", nil, []int64{-64, math.MaxInt64}), data.NewField("u64", nil, []uint64{64, math.MaxUint64}), data.NewField("f32", nil, []float32{1.5, float32(math.Inf(1))}), data.NewField("f64", nil, []float64{2.5, math.NaN()}), data.NewField("flag", nil, []bool{true, false}), data.NewField("clock", nil, []time.Time{at, at}), data.NewField("text", nil, []*string{&text, nil}), data.NewField("doc", nil, []json.RawMessage{json.RawMessage(`{"n":233}`), json.RawMessage(`{"n":2}`)}))
	out, err := Query(context.Background(), "Q", "SELECT * FROM A", map[string]data.Frames{"A": {f}}, at)
	require.NoError(t, err)
	require.Len(t, out.Fields, 14)
	for i := 0; i < 8; i++ {
		assert.Equal(t, f.Fields[i].At(1), out.Fields[i].At(1))
	}
	assert.True(t, out.Fields[8].NilAt(1))
	assert.True(t, out.Fields[9].NilAt(1))
	assert.True(t, out.Fields[12].NilAt(1))
	assert.Equal(t, true, out.Fields[10].At(0))
	assert.JSONEq(t, `{"n":233}`, string(out.Fields[13].At(0).(json.RawMessage)))
	out, err = Query(context.Background(), "Q", `SELECT COALESCE(text,'missing') AS text, JSON_EXTRACT(doc,'$.n') AS n, CAST(12.34 AS DECIMAL(10,2)) AS amount, UNIX_TIMESTAMP(clock) AS epoch FROM A ORDER BY u64`, map[string]data.Frames{"A": {f}}, at)
	require.NoError(t, err)
	textValue, _ := out.Fields[0].ConcreteAt(1)
	assert.Equal(t, "missing", textValue)
	value, err := out.Fields[2].FloatAt(0)
	require.NoError(t, err)
	assert.InDelta(t, 12.34, value, 1e-9)
}

func TestReferencesReadOnlyGuardsCoverHiddenASTChildren(t *testing.T) {
	refs, err := References(context.Background(), "WITH c AS (SELECT * FROM A) SELECT * FROM c UNION ALL SELECT * FROM B ORDER BY 1")
	require.NoError(t, err)
	assert.Equal(t, []string{"A", "B"}, refs)
	for _, query := range []string{"UPDATE A SET value=2", "DELETE FROM A", "DROP TABLE A", "SELECT LOAD_FILE('/etc/passwd')", "SELECT SLEEP(1)", "SELECT @@version", "SELECT @x", "SELECT 1 INTO OUTFILE '/tmp/x'", "SELECT * FROM A FOR UPDATE", "SELECT 1 UNION SELECT 2 ORDER BY SLEEP(1)", "SELECT 1 UNION SELECT 2 LIMIT SLEEP(1)", "SELECT ROW_NUMBER() OVER (ORDER BY SLEEP(1)) FROM A", "SELECT 1; SELECT 2", strings.Repeat("a", QueryBytes+1)} {
		t.Run(query[:min(60, len(query))], func(t *testing.T) { _, err := References(context.Background(), query); require.Error(t, err) })
	}
	out, err := Query(context.Background(), "Q", "SELECT 233 AS value", nil, time.Unix(1, 0))
	require.NoError(t, err)
	n, err := out.Fields[0].FloatAt(0)
	require.NoError(t, err)
	assert.Equal(t, float64(233), n)
}

func TestQueryLimitsCancellationAndEmptySchema(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Query(ctx, "Q", "SELECT 1", nil, time.Now())
	require.ErrorIs(t, err, context.Canceled)
	_, err = Query(context.Background(), "Q", "SELECT * FROM Missing", nil, time.Now())
	require.ErrorContains(t, err, "Missing")
	f := data.NewFrame("", data.NewField("value", nil, make([]int64, InputCells+1)))
	_, err = Query(context.Background(), "Q", "SELECT * FROM A", map[string]data.Frames{"A": {f}}, time.Now())
	require.ErrorContains(t, err, "100000")
	f = data.NewFrame("", data.NewField("value", nil, make([]int64, 400)))
	out, err := Query(context.Background(), "Q", "SELECT A.value FROM A CROSS JOIN A AS B", map[string]data.Frames{"A": {f}}, time.Now())
	require.NoError(t, err)
	assert.Equal(t, OutputCells, out.Rows())
	require.Len(t, out.Meta.Notices, 1)
	out, err = Query(context.Background(), "Q", "SELECT value FROM A WHERE 1=0", map[string]data.Frames{"A": {f}}, time.Now())
	require.NoError(t, err)
	assert.Zero(t, out.Rows())
	require.Len(t, out.Fields, 1)
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	f = data.NewFrame("", data.NewField("value", nil, make([]int64, 10000)))
	_, err = Query(ctx, "Q", "SELECT COUNT(*) AS count FROM A CROSS JOIN A AS B", map[string]data.Frames{"A": {f}}, time.Now())
	require.Error(t, err)
	require.Error(t, ctx.Err(), "expensive SQL must stop when its request expires")
}

func TestNumericWideAndMultiFullLong(t *testing.T) {
	a := data.NewFrame("", data.NewField("memory", data.Labels{"host": "go"}, []float64{233}), data.NewField("cpu", data.Labels{"host": "mysql"}, []*float64{nil}))
	a.Meta = &data.FrameMeta{Type: data.FrameTypeNumericWide}
	table, err := ToTable(context.Background(), "A", data.Frames{a})
	require.NoError(t, err)
	assert.Equal(t, 2, table.Rows())
	assert.Equal(t, "numeric-full-long", string(table.Meta.Type))
	assert.True(t, table.Fields[1].NilAt(1))
	out, err := Query(context.Background(), "Q", "SELECT __metric_name__, host, __value__ FROM A ORDER BY host", map[string]data.Frames{"A": {a}}, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 2, out.Rows())
	a.Meta.Type = data.FrameTypeNumericMulti
	b := data.NewFrame("", data.NewField("memory", data.Labels{"host": "worker"}, []float64{100}))
	b.Meta = &data.FrameMeta{Type: data.FrameTypeNumericMulti}
	table, err = ToTable(context.Background(), "A", data.Frames{a, b})
	require.NoError(t, err)
	assert.Equal(t, 3, table.Rows())
}

func TestFullLongConversionSortedLabelsNullsAndDisplay(t *testing.T) {
	at := time.Unix(233, 0)
	a := data.NewFrame("", data.NewField("clock", nil, []time.Time{at.Add(time.Second), at}), data.NewField("metric", data.Labels{"host": "a", "zone": "cn"}, []*float64{nil, new(float64(233))}))
	a.Fields[1].Config = &data.FieldConfig{DisplayNameFromDS: "Memory"}
	a.Meta = &data.FrameMeta{Type: data.FrameTypeTimeSeriesMulti}
	b := data.NewFrame("", data.NewField("clock", nil, []time.Time{at}), data.NewField("metric", data.Labels{"host": "b"}, []float64{20}))
	b.Meta = &data.FrameMeta{Type: data.FrameTypeTimeSeriesMulti}
	out, err := Query(context.Background(), "Q", "SELECT host, SUM(__value__) AS total, MAX(__display_name__) AS display, MAX(zone) AS zone FROM A GROUP BY host ORDER BY host", map[string]data.Frames{"A": {a, b}}, at)
	require.NoError(t, err)
	require.Equal(t, 2, out.Rows())
	assert.True(t, out.Fields[3].NilAt(1))
	assert.Equal(t, "Memory", *out.Fields[2].At(0).(*string))
	n, err := out.Fields[1].FloatAt(0)
	require.NoError(t, err)
	assert.Equal(t, float64(233), n)
	assert.Empty(t, a.RefID)
	assert.Equal(t, at.Add(time.Second), a.Fields[0].At(0))
}
