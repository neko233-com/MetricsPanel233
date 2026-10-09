package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"regexp"
	"sort"
)

type Matcher struct{ Name, Value, Type string }
type RawSeries struct {
	Name   string
	Labels map[string]string
	Points []model.Point
}

func Matches(name string, labels map[string]string, matchers []Matcher) bool {
	for _, m := range matchers {
		value := labels[m.Name]
		if m.Name == "__name__" {
			value = name
		}
		switch m.Type {
		case "=":
			if value != m.Value {
				return false
			}
		case "!=":
			if value == m.Value {
				return false
			}
		case "=~", "!~":
			r, err := regexp.Compile("^(?:" + m.Value + ")$")
			if err != nil {
				return false
			}
			match := r.MatchString(value)
			if m.Type == "=~" && !match || m.Type == "!~" && match {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func (s *Store) LoadSeries(ctx context.Context, start, end int64, matchers []Matcher) ([]RawSeries, error) {
	if s.Backend != nil {
		return s.Backend.LoadSeries(ctx, start, end, matchers)
	}
	query := `SELECT series.id,series.name,series.labels,samples.timestamp,samples.value FROM series JOIN samples ON series.id=samples.series_id WHERE samples.timestamp>=? AND samples.timestamp<=?`
	args := []any{start, end}
	for _, m := range matchers {
		if m.Name == "__name__" && m.Type == "=" {
			query += ` AND series.name=?`
			args = append(args, m.Value)
		}
	}
	query += ` ORDER BY series.id,samples.timestamp LIMIT 1000001`
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RawSeries{}
	lastID := int64(-1)
	index := -1
	count := 0
	for rows.Next() {
		count++
		if count > 1000000 {
			return nil, errors.New("PromQL selection exceeds 1000000 samples; narrow time range or selectors")
		}
		var id, ts int64
		var name, raw string
		var v float64
		if err = rows.Scan(&id, &name, &raw, &ts, &v); err != nil {
			return nil, err
		}
		if id != lastID {
			lastID = id
			var labels map[string]string
			if err = json.Unmarshal([]byte(raw), &labels); err != nil {
				return nil, err
			}
			index = -1
			if Matches(name, labels, matchers) {
				if len(out) >= 10000 {
					return nil, errors.New("PromQL selection exceeds 10000 series")
				}
				out = append(out, RawSeries{Name: name, Labels: labels, Points: []model.Point{}})
				index = len(out) - 1
			}
		}
		if index >= 0 {
			out[index].Points = append(out[index].Points, model.Point{Timestamp: ts, Value: v})
		}
	}
	return out, rows.Err()
}
func SortSeries(series []RawSeries) {
	sort.Slice(series, func(i, j int) bool {
		return series[i].Name+model.LabelsJSON(series[i].Labels) < series[j].Name+model.LabelsJSON(series[j].Labels)
	})
}
