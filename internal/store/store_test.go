package store_test

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func open(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "metrics.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.DB.Close()) })
	return s
}
func TestDurableAtomicIngestAndRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	require.NoError(t, s.Ingest(ctx, []model.Sample{{Name: "orders", Labels: map[string]string{"b": "2", "a": "1"}, Timestamp: now, Value: 10}}))
	require.NoError(t, s.Ingest(ctx, []model.Sample{{Name: "orders", Labels: map[string]string{"a": "1", "b": "2"}, Timestamp: now, Value: 23}}))
	require.Error(t, s.Ingest(ctx, []model.Sample{{Name: "would_be_partial", Value: 1}, {Name: "bad-name", Value: 1}}))
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	metrics, err := s.Metrics(ctx)
	require.NoError(t, err)
	require.Equal(t, []store.Metric{{Name: "orders", SeriesCount: 1}}, metrics)
	result, err := s.Query(ctx, model.Query{Metric: "orders", Start: now - 1000, End: now + 1000, Step: 1000, Aggregation: "last"})
	require.NoError(t, err)
	require.Len(t, result.Series, 1)
	require.Len(t, result.Series[0].Points, 1)
	assert.Equal(t, 23.0, result.Series[0].Points[0].Value)
	require.NoError(t, s.Prune(ctx, now+1))
	stats, err := s.Stats(ctx)
	require.NoError(t, err)
	assert.Zero(t, stats.Series)
	assert.Zero(t, stats.Samples)
}
func TestBucketsFiltersAndCounterReset(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	ts := time.Now().Add(-time.Minute).UnixMilli()
	samples := []model.Sample{}
	for _, x := range []struct {
		host   string
		values []float64
	}{{"a", []float64{10, 20, 5, 15}}, {"b", []float64{2, 4, 6, 8}}} {
		for i, v := range x.values {
			samples = append(samples, model.Sample{Name: "requests_total", Labels: map[string]string{"host": x.host}, Timestamp: ts + int64(i)*1000, Value: v})
		}
	}
	require.NoError(t, s.Ingest(ctx, samples))
	for _, test := range []struct {
		aggregation string
		expected    []float64
	}{{"sum", []float64{12, 24, 11, 23}}, {"avg", []float64{6, 12, 5.5, 11.5}}, {"min", []float64{2, 4, 5, 8}}, {"max", []float64{10, 20, 6, 15}}} {
		t.Run(test.aggregation, func(t *testing.T) {
			q, err := s.Query(ctx, model.Query{Metric: "requests_total", Start: ts, End: ts + 4000, Step: 1000, Aggregation: test.aggregation})
			require.NoError(t, err)
			require.Len(t, q.Series, 1)
			for i, p := range q.Series[0].Points {
				assert.Equal(t, test.expected[i], p.Value)
			}
		})
	}
	q, err := s.Query(ctx, model.Query{Metric: "requests_total", Labels: map[string]string{"host": "a"}, Start: ts, End: ts + 4000, Step: 1000, Aggregation: "rate"})
	require.NoError(t, err)
	require.Len(t, q.Series, 1)
	require.Len(t, q.Series[0].Points, 3)
	assert.Equal(t, []model.Point{{Timestamp: ts + 1000, Value: 10}, {Timestamp: ts + 2000, Value: 5}, {Timestamp: ts + 3000, Value: 10}}, q.Series[0].Points)
	q, err = s.Query(ctx, model.Query{Metric: "missing", Start: ts, End: ts + 1000, Step: 1000, Aggregation: "last"})
	require.NoError(t, err)
	assert.Empty(t, q.Series)
}
func TestValidationAndRawSelectors(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	for _, sample := range []model.Sample{{Name: "bad-name", Value: 1}, {Name: "ok", Value: math.NaN()}, {Name: "ok", Value: math.Inf(1)}, {Name: "ok", Labels: map[string]string{"__name__": "bad"}}, {Name: "ok", Labels: map[string]string{"bad-name": "v"}}, {Name: "ok", Timestamp: time.Now().Add(time.Hour).UnixMilli()}} {
		require.Error(t, s.Ingest(ctx, []model.Sample{sample}))
	}
	require.Error(t, s.Ingest(ctx, nil))
	require.Error(t, s.Ingest(ctx, make([]model.Sample, 10001)))
	require.NoError(t, s.Ingest(ctx, []model.Sample{{Name: "orders", Labels: map[string]string{"service": "api"}, Timestamp: now, Value: 1}, {Name: "orders", Timestamp: now, Value: 2}}))
	raw, err := s.LoadSeries(ctx, now-1000, now+1000, []store.Matcher{{Name: "__name__", Type: "=", Value: "orders"}, {Name: "service", Type: "=~", Value: "a.*"}})
	require.NoError(t, err)
	require.Len(t, raw, 1)
	assert.Equal(t, "api", raw[0].Labels["service"])
	raw, err = s.LoadSeries(ctx, now-1000, now+1000, []store.Matcher{{Name: "service", Type: "=", Value: ""}})
	require.NoError(t, err)
	require.Len(t, raw, 1)
	metadata, err := s.SelectSeries(ctx, now-1000, now+1000, []store.Matcher{{Name: "__name__", Type: "=", Value: "orders"}, {Name: "service", Type: "=~", Value: "a.*"}})
	require.NoError(t, err)
	require.Len(t, metadata, 1)
	assert.Equal(t, "api", metadata[0].Labels["service"])
	assert.Nil(t, metadata[0].Points)
	metadata, err = s.SelectSeries(ctx, now+1, now+1000, nil)
	require.NoError(t, err)
	assert.Empty(t, metadata)
	for _, q := range []model.Query{{Metric: "orders", Start: 0, End: 32 * 24 * 3600000, Step: 1000, Aggregation: "last"}, {Metric: "orders", Start: 1, End: 1000, Step: 0, Aggregation: "last"}, {Metric: "orders", Start: 1, End: 1000, Step: 1000, Aggregation: "oops"}} {
		_, err = s.Query(ctx, q)
		require.Error(t, err)
	}
}
func TestResourcesPersistAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	ctx := context.Background()
	target, err := s.SaveTarget(ctx, model.Target{Name: "mysql", URL: "http://mysql-exporter:9104/metrics", IntervalSeconds: 5, Enabled: true})
	require.NoError(t, err)
	require.NotZero(t, target.ID)
	d := model.Dashboard{ID: "business", Name: "Business", Panels: []model.Panel{{ID: "orders", Title: "Orders", Expr: "sum(orders)", Aggregation: "last", Config: []byte(`{"type":"table","gridPos":{"x":0,"y":0,"w":24,"h":8},"transformations":[{"id":"organize","options":{}}]}`)}}, Variables: []model.Variable{{Name: "job", Type: "query", Query: "label_values(job)", Current: "mysql", Config: []byte(`{"allValue":"mysql.*","sort":1}`)}}, Grafana: []byte(`{"title":"Original"}`)}
	_, err = s.SaveDashboard(ctx, d)
	require.NoError(t, err)
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	targets, err := s.Targets(ctx)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, "mysql", targets[0].Name)
	dashboards, err := s.Dashboards(ctx)
	require.NoError(t, err)
	require.Len(t, dashboards, 2)
	assert.Equal(t, d.Variables, dashboards[1].Variables)
	assert.Equal(t, d.Panels, dashboards[1].Panels)
	assert.JSONEq(t, string(d.Grafana), string(dashboards[1].Grafana))
	require.NoError(t, s.DeleteTarget(ctx, target.ID))
	require.Error(t, s.DeleteTarget(ctx, target.ID))
	require.NoError(t, s.DeleteDashboard(ctx, d.ID))
	require.Error(t, s.DeleteDashboard(ctx, d.ID))
}
func BenchmarkSQLiteBatchIngest(b *testing.B) {
	s, err := store.Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.DB.Close()
	samples := make([]model.Sample, 1000)
	for i := range samples {
		samples[i] = model.Sample{Name: "benchmark_requests", Labels: map[string]string{"service": "bench"}, Value: float64(i), Timestamp: int64(i + 1)}
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(samples)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range samples {
			samples[j].Timestamp += 1000
		}
		if err := s.Ingest(context.Background(), samples); err != nil {
			b.Fatal(err)
		}
	}
}
