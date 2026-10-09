package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
)

const PatternDimensions = 64

// Pattern is an immutable, content-addressed time-series window embedding.
// Shape removes level/scale; raw preserves the original measurement scale.
type Pattern struct {
	ID            string            `json:"id"`
	Metric        string            `json:"metric"`
	Labels        map[string]string `json:"labels"`
	Start         int64             `json:"start"`
	End           int64             `json:"end"`
	Aggregation   string            `json:"aggregation"`
	Normalization string            `json:"normalization"`
	Values        []float32         `json:"values,omitempty"`
	Mean          float64           `json:"mean"`
	StdDev        float64           `json:"stddev"`
	Coverage      float64           `json:"coverage"`
	CreatedAt     int64             `json:"created_at"`
}

func (p Pattern) Validate() error {
	if !metricName.MatchString(p.Metric) || p.Start < 0 || p.End <= p.Start || p.End-p.Start > 31*24*3600000 {
		return errors.New("pattern needs a valid metric and positive window <=31 days")
	}
	if p.Normalization != "shape" && p.Normalization != "raw" {
		return errors.New("normalization must be shape or raw")
	}
	if p.Aggregation != "last" && p.Aggregation != "rate" {
		return errors.New("pattern aggregation must be last or rate")
	}
	if len(p.Values) != PatternDimensions {
		return errors.New("pattern must have exactly 64 dimensions")
	}
	for _, v := range p.Values {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) || math.Abs(float64(v)) > 1e15 {
			return errors.New("pattern values must be finite Float32 values with absolute value <=1e15")
		}
	}
	for _, v := range []float64{p.Mean, p.StdDev, p.Coverage} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("pattern statistics must be finite")
		}
	}
	if p.Coverage < 0.75 || p.Coverage > 1 || p.StdDev < 0 {
		return errors.New("pattern needs at least 75% observed buckets and nonnegative standard deviation")
	}
	return ValidateLabels(p.Labels)
}

func (p *Pattern) SetID() {
	if p.Labels == nil {
		p.Labels = map[string]string{}
	}
	copy := *p
	copy.ID = ""
	copy.CreatedAt = 0
	b, _ := json.Marshal(copy)
	hash := sha256.Sum256(b)
	p.ID = hex.EncodeToString(hash[:])
}
func (p Pattern) Summary() Pattern { p.Values = nil; return p }

type PatternHit struct {
	Pattern  Pattern `json:"pattern"`
	Distance float64 `json:"distance"`
}
type PatternSearch struct {
	Reference   Pattern
	Metric      string
	Labels      map[string]string
	Limit       int
	Exact       bool
	IncludeSelf bool
}
