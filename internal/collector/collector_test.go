package collector_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/collector"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrometheusTypesLabelsAndNonfinite(t *testing.T) {
	text := `# TYPE orders_total counter
orders_total{service="api",job="exporter"} 23
# TYPE memory_bytes gauge
memory_bytes 233
# TYPE duration_seconds histogram
duration_seconds_bucket{le="0.1"} 2
duration_seconds_bucket{le="+Inf"} 3
duration_seconds_sum 0.25
duration_seconds_count 3
# TYPE latency summary
latency{quantile="0.5"} 0.1
latency_sum 0.3
latency_count 3
nonfinite NaN
`
	samples, err := collector.ParsePrometheus(strings.NewReader(text), model.Target{Name: "target", URL: "http://test/metrics", Labels: map[string]string{"service": "override"}})
	require.NoError(t, err)
	assert.Len(t, samples, 9)
	byName := map[string][]model.Sample{}
	for _, s := range samples {
		byName[s.Name] = append(byName[s.Name], s)
		assert.Equal(t, "target", s.Labels["job"])
		assert.Equal(t, "override", s.Labels["service"])
		require.NoError(t, s.Validate())
	}
	assert.Equal(t, 23.0, byName["orders_total"][0].Value)
	assert.Len(t, byName["duration_seconds_bucket"], 2)
	assert.Empty(t, byName["nonfinite"])
	_, err = collector.ParsePrometheus(strings.NewReader("bad invalid\n"), model.Target{})
	require.Error(t, err)
}
func TestScrapeSuccessFailureAndScheduler(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	defer s.DB.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	exporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# TYPE go_business_total counter\ngo_business_total 42\n"))
	}))
	defer exporter.Close()
	target, err := s.SaveTarget(ctx, model.Target{Name: "go", URL: exporter.URL, IntervalSeconds: 5, Enabled: true})
	require.NoError(t, err)
	c := collector.New(s)
	n, err := c.Scrape(ctx, target)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	targets, err := s.Targets(ctx)
	require.NoError(t, err)
	assert.Empty(t, targets[0].LastError)
	assert.Equal(t, 1, targets[0].Samples)
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer fail.Close()
	target.URL = fail.URL
	_, err = c.Scrape(ctx, target)
	require.Error(t, err)
	targets, err = s.Targets(ctx)
	require.NoError(t, err)
	assert.Contains(t, targets[0].LastError, "503")
	q, err := s.Query(ctx, model.Query{Metric: "up", Start: time.Now().Add(-time.Minute).UnixMilli(), End: time.Now().UnixMilli() + 1, Step: 1000, Aggregation: "last", Labels: map[string]string{"instance": fail.URL}})
	require.NoError(t, err)
	require.Len(t, q.Series, 1)
	assert.Zero(t, q.Series[0].Points[0].Value)
	newTarget, err := s.SaveTarget(ctx, model.Target{Name: "scheduled", URL: exporter.URL, IntervalSeconds: 5, Enabled: true})
	require.NoError(t, err)
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	require.Eventually(t, func() bool {
		ts, e := s.Targets(ctx)
		if e != nil {
			return false
		}
		for _, v := range ts {
			if v.ID == newTarget.ID {
				return v.LastScrape > 0 && v.LastError == ""
			}
		}
		return false
	}, 4*time.Second, 100*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("collector did not shut down")
	}
}
