package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
)

// Query buckets the last observed value of each series, then applies cross-series
// aggregation. It never fills gaps or fabricates samples. Rate is per-series,
// reset-aware counter increase divided by elapsed seconds between observations.
func (s *Store) Query(ctx context.Context, q model.Query) (model.QueryResult, error) {
	if s.Backend != nil {
		return s.Backend.Query(ctx, q)
	}
	out := model.QueryResult{Metric: q.Metric, Aggregation: q.Aggregation, Start: q.Start, End: q.End, Step: q.Step, Series: []model.Series{}}
	if q.Start < 0 || q.End <= q.Start || q.End-q.Start > int64(31*24*time.Hour/time.Millisecond) {
		return out, errors.New("query range must be positive and at most 31 days")
	}
	if q.Step < 1000 || (q.End-q.Start)/q.Step > 2000 {
		return out, errors.New("step must be at least 1 second and produce at most 2000 buckets")
	}
	if !model.ValidAggregation(q.Aggregation) || q.Metric == "" {
		return out, errors.New("valid metric and aggregation required")
	}
	if err := model.ValidateLabels(q.Labels); err != nil {
		return out, err
	}
	sqlText := `SELECT series.id,series.labels,samples.timestamp,samples.value FROM series JOIN samples ON samples.series_id=series.id WHERE series.name=? AND samples.timestamp>=? AND samples.timestamp<=?`
	from := q.Start
	if q.Aggregation == "rate" {
		from -= q.Step
	}
	args := []any{q.Metric, from, q.End}
	keys := make([]string, 0, len(q.Labels))
	for k := range q.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sqlText += ` AND EXISTS(SELECT 1 FROM json_each(series.labels) WHERE key=? AND value=?)`
		args = append(args, k, q.Labels[k])
	}
	sqlText += ` ORDER BY series.id,samples.timestamp LIMIT 250001`
	rows, err := s.DB.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	type state struct {
		labels   map[string]string
		buckets  map[int64]float64
		previous *model.Point
	}
	states := map[int64]*state{}
	ids := []int64{}
	count := 0
	for rows.Next() {
		count++
		if count > 250000 {
			return out, errors.New("query exceeds 250000 samples; narrow range or label filters")
		}
		var id, ts int64
		var labels string
		var value float64
		if err = rows.Scan(&id, &labels, &ts, &value); err != nil {
			return out, err
		}
		st, ok := states[id]
		if !ok {
			if len(states) >= 200 {
				return out, errors.New("query exceeds 200 series; add label filters")
			}
			st = &state{buckets: map[int64]float64{}}
			if err = json.Unmarshal([]byte(labels), &st.labels); err != nil {
				return out, err
			}
			states[id] = st
			ids = append(ids, id)
		}
		if ts >= q.Start {
			bucket := q.Start + (ts-q.Start)/q.Step*q.Step
			if q.Aggregation == "rate" {
				if st.previous != nil && ts > st.previous.Timestamp {
					delta := value - st.previous.Value
					if delta < 0 {
						delta = value
					}
					rate := delta / (float64(ts-st.previous.Timestamp) / 1000)
					if !math.IsNaN(rate) && !math.IsInf(rate, 0) {
						st.buckets[bucket] = rate
					}
				}
			} else {
				st.buckets[bucket] = value
			}
		}
		st.previous = &model.Point{Timestamp: ts, Value: value}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	points := func(buckets map[int64]float64) []model.Point {
		times := make([]int64, 0, len(buckets))
		for t := range buckets {
			times = append(times, t)
		}
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		v := make([]model.Point, 0, len(times))
		for _, t := range times {
			v = append(v, model.Point{Timestamp: t, Value: buckets[t]})
		}
		return v
	}
	if q.Aggregation == "last" || q.Aggregation == "rate" {
		for _, id := range ids {
			st := states[id]
			if len(st.buckets) > 0 {
				out.Series = append(out.Series, model.Series{Labels: st.labels, Points: points(st.buckets)})
			}
		}
		return out, nil
	}
	aggregated := map[int64]float64{}
	counts := map[int64]int{}
	for _, st := range states {
		for t, v := range st.buckets {
			n := counts[t]
			switch q.Aggregation {
			case "avg", "sum":
				aggregated[t] += v
			case "min":
				if n == 0 || v < aggregated[t] {
					aggregated[t] = v
				}
			case "max":
				if n == 0 || v > aggregated[t] {
					aggregated[t] = v
				}
			default:
				return out, fmt.Errorf("unknown aggregation")
			}
			counts[t]++
		}
	}
	if q.Aggregation == "avg" {
		for t, n := range counts {
			aggregated[t] /= float64(n)
		}
	}
	if len(aggregated) > 0 {
		out.Series = append(out.Series, model.Series{Labels: map[string]string{}, Points: points(aggregated)})
	}
	return out, nil
}
