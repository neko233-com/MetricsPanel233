// Package analysis creates bounded, reproducible time-window embeddings from
// observed samples. Interpolation affects only embeddings, never raw metrics.
package analysis

import (
	"errors"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"math"
)

func Embed(metric string, series model.Series, start, end int64, aggregation, normalization string) (model.Pattern, error) {
	p := model.Pattern{Metric: metric, Labels: series.Labels, Start: start, End: end, Aggregation: aggregation, Normalization: normalization, Values: make([]float32, model.PatternDimensions)}
	if end-start < 64000 {
		return p, errors.New("pattern window must be at least 64 seconds")
	}
	values := make([]float64, 64)
	observed := make([]bool, 64)
	count := 0
	for _, point := range series.Points {
		if point.Timestamp < start || point.Timestamp > end || math.IsNaN(point.Value) || math.IsInf(point.Value, 0) {
			continue
		}
		index := int(float64(point.Timestamp-start) * 64 / float64(end-start))
		if index >= 64 {
			index = 63
		}
		values[index] = point.Value
		if !observed[index] {
			observed[index] = true
			count++
		}
	}
	p.Coverage = float64(count) / 64
	if count < 48 {
		return p, errors.New("insufficient data: at least 48 of 64 buckets must contain samples")
	}
	for i := 0; i < 64; {
		if observed[i] {
			i++
			continue
		}
		first := i
		for i < 64 && !observed[i] {
			i++
		}
		last := i
		if last-first > 8 {
			return p, errors.New("insufficient data: gap exceeds 1/8 of the window")
		}
		for j := first; j < last; j++ {
			switch {
			case first == 0:
				values[j] = values[last]
			case last == 64:
				values[j] = values[first-1]
			default:
				weight := float64(j-first+1) / float64(last-first+1)
				values[j] = values[first-1]*(1-weight) + values[last]*weight
			}
		}
	}
	// Scale before computing moments to avoid overflow for large finite samples.
	scale := 0.0
	for _, v := range values {
		scale = math.Max(scale, math.Abs(v))
	}
	mean, variance := 0.0, 0.0
	if scale > 0 {
		for _, v := range values {
			mean += v / scale / 64
		}
		for _, v := range values {
			delta := v/scale - mean
			variance += delta * delta / 64
		}
	}
	p.Mean = mean * scale
	p.StdDev = math.Sqrt(variance) * scale
	for i, v := range values {
		if normalization == "shape" {
			if variance > 1e-24 {
				v = (v/scale - mean) / math.Sqrt(variance)
			} else {
				v = 0
			}
		}
		p.Values[i] = float32(v)
	}
	if err := p.Validate(); err != nil {
		return p, err
	}
	p.SetID()
	return p, nil
}
