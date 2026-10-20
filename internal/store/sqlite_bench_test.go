package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

// These benchmarks document the reproducible single-node baseline used by
// docs/performance-baseline.md. They are not production guarantees.
func benchmarkStore(tb testing.TB) *store.Store {
	tb.Helper()
	s, err := store.Open(filepath.Join(tb.TempDir(), "bench.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = s.DB.Close() })
	return s
}

func ingestSamples(tb testing.TB, s *store.Store, samples []model.Sample) time.Duration {
	tb.Helper()
	start := time.Now()
	for offset := 0; offset < len(samples); offset += 1000 {
		end := min(offset+1000, len(samples))
		if err := s.Ingest(context.Background(), samples[offset:end]); err != nil {
			tb.Fatal(err)
		}
	}
	return time.Since(start)
}

func TestBaselineIngestAndAggregate100k(t *testing.T) {
	if testing.Short() {
		t.Skip("baseline ingestion is a manual benchmark; use -bench or remove -short")
	}
	s := benchmarkStore(t)
	now := time.Now().UnixMilli()
	samples := make([]model.Sample, 100000)
	for i := range samples {
		samples[i] = model.Sample{
			Name:      "baseline_requests",
			Labels:    map[string]string{"service": fmt.Sprintf("svc-%d", i%20)},
			Timestamp: now - 24*3600*1000 + int64(i)*864,
			Value:     float64(i % 1000),
		}
	}
	duration := ingestSamples(t, s, samples)
	t.Logf("ingested %d samples in %s", len(samples), duration)
	result, err := s.Query(context.Background(), model.Query{Metric: "baseline_requests", Start: now - 24*3600*1000, End: now, Step: 60000, Aggregation: "avg"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Series) != 1 || len(result.Series[0].Points) == 0 {
		t.Fatalf("expected one aggregated series with points, got %d", len(result.Series))
	}
	t.Logf("24h aggregation returned %d points", len(result.Series[0].Points))
}

func BenchmarkSQLiteAggregate24Hours(b *testing.B) {
	s := benchmarkStore(b)
	now := time.Now().UnixMilli()
	samples := make([]model.Sample, 100000)
	for i := range samples {
		samples[i] = model.Sample{
			Name:      "baseline_requests",
			Labels:    map[string]string{"service": fmt.Sprintf("svc-%d", i%20)},
			Timestamp: now - 24*3600*1000 + int64(i)*864,
			Value:     float64(i % 1000),
		}
	}
	ingestSamples(b, s, samples)
	query := model.Query{Metric: "baseline_requests", Start: now - 24*3600*1000, End: now, Step: 60000, Aggregation: "avg"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Query(context.Background(), query); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSQLitePatternSearch10k(b *testing.B) {
	s := benchmarkStore(b)
	ctx := context.Background()
	batch := make([]model.Pattern, 0, 200)
	for i := 0; i < 10000; i++ {
		p := model.Pattern{
			Metric:        "orders",
			Labels:        map[string]string{"service": fmt.Sprintf("svc-%d", i%50)},
			Start:         1000,
			End:           2000,
			Normalization: "shape",
			Aggregation:   "last",
			Coverage:      1,
			Values:        make([]float32, model.PatternDimensions),
		}
		for j := range p.Values {
			p.Values[j] = float32((i + j) % 64)
		}
		batch = append(batch, p)
		if len(batch) == cap(batch) {
			if err := s.SavePatterns(ctx, batch); err != nil {
				b.Fatal(err)
			}
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := s.SavePatterns(ctx, batch); err != nil {
			b.Fatal(err)
		}
	}
	reference := model.Pattern{
		Metric:        "orders",
		Start:         1000,
		End:           2000,
		Normalization: "shape",
		Aggregation:   "last",
		Coverage:      1,
		Values:        make([]float32, model.PatternDimensions),
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.SearchPatterns(ctx, model.PatternSearch{Reference: reference, Limit: 10}); err != nil {
			b.Fatal(err)
		}
	}
}
