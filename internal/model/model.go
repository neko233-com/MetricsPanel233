package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"time"
)

var metricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]{0,199}$`)
var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,99}$`)

type Sample struct {
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels,omitempty"`
	Value     float64           `json:"value"`
	Timestamp int64             `json:"timestamp,omitempty"` // Unix milliseconds; zero means now.
}

func ValidateLabels(labels map[string]string) error {
	if len(labels) > 32 {
		return errors.New("at most 32 labels per series")
	}
	for k, v := range labels {
		if !labelName.MatchString(k) || k == "__name__" || len(v) > 512 {
			return fmt.Errorf("invalid label %q (value limit: 512 bytes)", k)
		}
	}
	return nil
}

func (s Sample) Validate() error {
	if !metricName.MatchString(s.Name) {
		return fmt.Errorf("invalid metric name %q", s.Name)
	}
	if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
		return errors.New("sample value must be finite")
	}
	if s.Timestamp < 0 || s.Timestamp > time.Now().Add(5*time.Minute).UnixMilli() {
		return errors.New("timestamp must be Unix milliseconds, not more than 5 minutes in the future")
	}
	return ValidateLabels(s.Labels)
}

type Target struct {
	ID              int64             `json:"id"`
	Name            string            `json:"name"`
	URL             string            `json:"url"`
	IntervalSeconds int               `json:"interval_seconds"`
	Labels          map[string]string `json:"labels"`
	Enabled         bool              `json:"enabled"`
	LastScrape      int64             `json:"last_scrape"`
	LastError       string            `json:"last_error"`
	Samples         int               `json:"samples"`
	DurationMS      int64             `json:"duration_ms"`
}

func (t Target) Validate() error {
	u, err := url.Parse(t.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("URL must be http(s), without credentials or fragment")
	}
	if len(t.Name) == 0 || len(t.Name) > 100 {
		return errors.New("name must contain 1–100 bytes")
	}
	if t.IntervalSeconds < 5 || t.IntervalSeconds > 86400 {
		return errors.New("interval_seconds must be between 5 and 86400")
	}
	return ValidateLabels(t.Labels)
}

type Panel struct {
	ID            string            `json:"id"`
	Title         string            `json:"title"`
	Metric        string            `json:"metric"`
	Aggregation   string            `json:"aggregation"`
	Unit          string            `json:"unit"`
	Labels        map[string]string `json:"labels,omitempty"`
	Expr          string            `json:"expr,omitempty"`
	Expressions   []string          `json:"expressions,omitempty"`
	Visualization string            `json:"visualization,omitempty"`
	// Config retains the Grafana panel contract rather than a lossy conversion.
	Config json.RawMessage `json:"config,omitempty"`
}

type Variable struct {
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	Query      string          `json:"query"`
	Current    string          `json:"current"`
	Options    []string        `json:"options"`
	Multi      bool            `json:"multi"`
	IncludeAll bool            `json:"include_all"`
	Config     json.RawMessage `json:"config,omitempty"`
}

type Dashboard struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Panels    []Panel         `json:"panels"`
	UpdatedAt int64           `json:"updated_at"`
	Variables []Variable      `json:"variables,omitempty"`
	Grafana   json.RawMessage `json:"grafana,omitempty"`
}

func ValidAggregation(a string) bool {
	switch a {
	case "last", "avg", "sum", "min", "max", "rate":
		return true
	}
	return false
}

func (d Dashboard) Validate() error {
	if len(d.Name) == 0 || len(d.Name) > 100 {
		return errors.New("dashboard name must contain 1–100 bytes")
	}
	if len(d.Panels) > 500 {
		return errors.New("at most 500 panels per dashboard")
	}
	ids := map[string]bool{}
	for _, p := range d.Panels {
		if p.ID == "" || ids[p.ID] {
			return errors.New("panel IDs must be nonempty and unique")
		}
		ids[p.ID] = true
		if len(p.Title) == 0 || len(p.Title) > 256 || (len(p.Config) == 0 && p.Expr == "" && len(p.Expressions) == 0 && !metricName.MatchString(p.Metric)) || !ValidAggregation(p.Aggregation) {
			return errors.New("each panel needs a title, valid metric and aggregation")
		}
		if len(p.Config) > 512*1024 || (len(p.Config) > 0 && !json.Valid(p.Config)) {
			return errors.New("panel config must be valid JSON under 512 KiB")
		}
		if p.Unit != "" && p.Unit != "bytes" && p.Unit != "seconds" && p.Unit != "percent" && p.Unit != "count" && p.Unit != "ops" {
			return errors.New("unsupported panel unit")
		}
		if err := ValidateLabels(p.Labels); err != nil {
			return err
		}
		if len(p.Expr) > 10000 || len(p.Expressions) > 32 {
			return errors.New("expression limit: 10000 bytes, 32 queries per panel")
		}
		for _, expr := range p.Expressions {
			if len(expr) > 10000 {
				return errors.New("expression exceeds 10000 bytes")
			}
		}
	}
	if len(d.Variables) > 32 {
		return errors.New("at most 32 variables")
	}
	for _, v := range d.Variables {
		if !labelName.MatchString(v.Name) || len(v.Config) > 128*1024 || (len(v.Config) > 0 && !json.Valid(v.Config)) {
			return errors.New("variable needs a valid name and JSON config under 128 KiB")
		}
	}
	return nil
}

func LabelsJSON(labels map[string]string) string {
	if labels == nil {
		return "{}"
	}
	b, _ := json.Marshal(labels)
	return string(b)
}

type Point struct {
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
}
type Series struct {
	Labels map[string]string `json:"labels"`
	Points []Point           `json:"points"`
}
type Query struct {
	Metric           string
	Labels           map[string]string
	Start, End, Step int64
	Aggregation      string
}
type QueryResult struct {
	Metric      string   `json:"metric"`
	Aggregation string   `json:"aggregation"`
	Start       int64    `json:"start"`
	End         int64    `json:"end"`
	Step        int64    `json:"step"`
	Series      []Series `json:"series"`
}
