// Package promcompat connects the upstream Prometheus PromQL engine to our
// durable metrics backends and exposes Grafana's Prometheus datasource protocol.
package promcompat

import (
	"context"
	"sort"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/tsdb/chunks"
	"github.com/prometheus/prometheus/util/annotations"
)

type Queryable struct{ Store *store.Store }

func (q Queryable) Querier(start, end int64) (storage.Querier, error) {
	return &querier{store: q.Store, start: start, end: end}, nil
}

type querier struct {
	store      *store.Store
	start, end int64
}

func convertMatchers(ms []*labels.Matcher) []store.Matcher {
	out := make([]store.Matcher, 0, len(ms))
	for _, m := range ms {
		out = append(out, store.Matcher{Name: m.Name, Value: m.Value, Type: m.Type.String()})
	}
	return out
}
func (q *querier) Select(ctx context.Context, sorted bool, hints *storage.SelectHints, ms ...*labels.Matcher) storage.SeriesSet {
	start, end := q.start, q.end
	if hints != nil {
		start = hints.Start
		end = hints.End
	}
	raw, err := q.store.LoadSeries(ctx, start, end, convertMatchers(ms))
	if err != nil {
		return storage.ErrSeriesSet(err)
	}
	series := make([]storage.Series, 0, len(raw))
	for _, r := range raw {
		ls := make([]labels.Label, 0, len(r.Labels)+1)
		ls = append(ls, labels.Label{Name: "__name__", Value: r.Name})
		for k, v := range r.Labels {
			ls = append(ls, labels.Label{Name: k, Value: v})
		}
		samples := make([]chunks.Sample, len(r.Points))
		for i, p := range r.Points {
			samples[i] = floatSample{point: p}
		}
		series = append(series, storage.NewListSeries(labels.New(ls...), samples))
	}
	if sorted {
		sort.Slice(series, func(i, j int) bool { return labels.Compare(series[i].Labels(), series[j].Labels()) < 0 })
	}
	return &seriesSet{series: series, index: -1}
}
func (q *querier) LabelValues(ctx context.Context, name string, _ *storage.LabelHints, ms ...*labels.Matcher) ([]string, annotations.Annotations, error) {
	raw, err := q.store.SelectSeries(ctx, q.start, q.end, convertMatchers(ms))
	if err != nil {
		return nil, nil, err
	}
	values := map[string]bool{}
	for _, s := range raw {
		if name == "__name__" {
			values[s.Name] = true
		} else if v, ok := s.Labels[name]; ok {
			values[v] = true
		}
	}
	out := make([]string, 0, len(values))
	for v := range values {
		out = append(out, v)
	}
	sort.Strings(out)
	return out, nil, nil
}
func (q *querier) LabelNames(ctx context.Context, _ *storage.LabelHints, ms ...*labels.Matcher) ([]string, annotations.Annotations, error) {
	raw, err := q.store.SelectSeries(ctx, q.start, q.end, convertMatchers(ms))
	if err != nil {
		return nil, nil, err
	}
	values := map[string]bool{}
	for _, s := range raw {
		values["__name__"] = true
		for k := range s.Labels {
			values[k] = true
		}
	}
	out := make([]string, 0, len(values))
	for v := range values {
		out = append(out, v)
	}
	sort.Strings(out)
	return out, nil, nil
}
func (*querier) Close() error { return nil }

type floatSample struct{ point model.Point }

func (s floatSample) T() int64                    { return s.point.Timestamp }
func (floatSample) ST() int64                     { return 0 }
func (s floatSample) F() float64                  { return s.point.Value }
func (floatSample) H() *histogram.Histogram       { return nil }
func (floatSample) FH() *histogram.FloatHistogram { return nil }
func (floatSample) Type() chunkenc.ValueType      { return chunkenc.ValFloat }
func (s floatSample) Copy() chunks.Sample         { return s }

type seriesSet struct {
	series []storage.Series
	index  int
}

func (s *seriesSet) Next() bool                      { s.index++; return s.index < len(s.series) }
func (s *seriesSet) At() storage.Series              { return s.series[s.index] }
func (*seriesSet) Err() error                        { return nil }
func (*seriesSet) Warnings() annotations.Annotations { return nil }
