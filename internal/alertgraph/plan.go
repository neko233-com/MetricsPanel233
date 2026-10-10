// Package alertgraph executes persisted Grafana alert query graphs over the
// same SDK frames and expression engine used by dashboard and agent queries.
package alertgraph

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/neko233-com/MetricsPanel233/internal/expressions"
	"github.com/prometheus/prometheus/promql/parser"
)

type Query struct {
	RefID             string `json:"refId"`
	DatasourceUID     string `json:"datasourceUid"`
	QueryType         string `json:"queryType"`
	RelativeTimeRange struct {
		From int64 `json:"from"`
		To   int64 `json:"to"`
	} `json:"relativeTimeRange"`
	Model        json.RawMessage `json:"model"`
	interval     time.Duration
	points       int64
	dependencies []string
}

type Plan struct {
	OrgID                int64           `json:"orgID"`
	NotificationSettings json.RawMessage `json:"notification_settings"`
	Condition            string          `json:"condition"`
	Data                 []Query         `json:"data"`
	Record               *struct {
		From                string `json:"from"`
		TargetDatasourceUID string `json:"target_datasource_uid"`
	} `json:"record"`
}

func Parse(raw json.RawMessage) (*Plan, error) {
	if len(raw) == 0 || len(raw) > 512<<10 {
		return nil, errors.New("Grafana graph must be JSON under 512 KiB")
	}
	var p Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.OrgID != 0 && p.OrgID != 1 {
		return nil, errors.New("only organization 1 is supported")
	}
	if len(p.NotificationSettings) > 0 && string(p.NotificationSettings) != "null" {
		return nil, errors.New("notification_settings requires a notification integration, which is not configured")
	}
	if p.Record != nil {
		if p.Record.TargetDatasourceUID != "" && p.Record.TargetDatasourceUID != "metricspanel" {
			return nil, errors.New("recording target must be metricspanel")
		}
		p.Condition = p.Record.From
	}
	if len(p.Data) == 0 || len(p.Data) > 32 {
		return nil, errors.New("Grafana graph needs 1–32 query nodes")
	}
	nodes := map[string]*Query{}
	for i := range p.Data {
		q := &p.Data[i]
		if q.RefID == "" || len(q.RefID) > 100 {
			return nil, errors.New("query RefID must be 1–100 bytes")
		}
		if nodes[q.RefID] != nil {
			return nil, fmt.Errorf("duplicate query reference %q", q.RefID)
		}
		nodes[q.RefID] = q
		var m struct {
			Expr          string `json:"expr"`
			IntervalMS    int64  `json:"intervalMs"`
			MaxDataPoints int64  `json:"maxDataPoints"`
			Datasource    struct {
				UID  string `json:"uid"`
				Type string `json:"type"`
			} `json:"datasource"`
		}
		if err := json.Unmarshal(q.Model, &m); err != nil {
			return nil, fmt.Errorf("query %s: %w", q.RefID, err)
		}
		if len(q.Model) == 0 || string(q.Model) == "null" {
			return nil, fmt.Errorf("query %s needs a model", q.RefID)
		}
		if q.DatasourceUID == "" {
			q.DatasourceUID = m.Datasource.UID
		}
		if q.DatasourceUID == "" && expressions.IsSource(m.Datasource.Type) {
			q.DatasourceUID = "__expr__"
		}
		if expressions.IsSource(q.DatasourceUID) {
			q.DatasourceUID = "__expr__"
		}
		if q.DatasourceUID == "prometheus" {
			q.DatasourceUID = "metricspanel"
		}
		if q.DatasourceUID == "-- Grafana --" || q.DatasourceUID == "-1" {
			q.DatasourceUID = "grafana"
		}
		if q.DatasourceUID == "" || len(q.DatasourceUID) > 100 {
			return nil, fmt.Errorf("query %s needs a datasource UID", q.RefID)
		}
		tr := q.RelativeTimeRange
		if tr.From < 0 || tr.To < 0 || tr.From < tr.To || tr.From > 31*86400 {
			return nil, fmt.Errorf("query %s relative range must be 0–31 days with from >= to", q.RefID)
		}
		if m.IntervalMS < 0 || m.IntervalMS > 31*86400000 {
			return nil, fmt.Errorf("query %s has invalid intervalMs", q.RefID)
		}
		q.interval = time.Duration(m.IntervalMS) * time.Millisecond
		if q.interval == 0 {
			q.interval = time.Second
		}
		if m.MaxDataPoints < 0 || m.MaxDataPoints > expressions.MaxPoints {
			return nil, fmt.Errorf("query %s has invalid maxDataPoints", q.RefID)
		}
		q.points = m.MaxDataPoints
		if q.points == 0 {
			q.points = 2000
		}
		if expressions.IsSource(q.DatasourceUID) {
			_, refs, err := expressions.Describe(q.Model)
			if err != nil {
				return nil, fmt.Errorf("expression %s: %w", q.RefID, err)
			}
			q.dependencies = refs
		} else if m.Expr != "" {
			if len(m.Expr) > 10000 {
				return nil, fmt.Errorf("query %s exceeds 10000 expression bytes", q.RefID)
			}
			// Only the built-in Prometheus UID is known here. Other backend
			// plugins may use an expr field in a different query language.
			if q.DatasourceUID == "metricspanel" {
				if _, err := parser.NewParser(parser.Options{}).ParseExpr(m.Expr); err != nil {
					return nil, fmt.Errorf("query %s: %w", q.RefID, err)
				}
			}
		}
	}
	if nodes[p.Condition] == nil {
		return nil, fmt.Errorf("unknown condition reference %q", p.Condition)
	}
	visited, active := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(ref string) error {
		if visited[ref] {
			return nil
		}
		if active[ref] {
			return fmt.Errorf("query dependency cycle at %s", ref)
		}
		q := nodes[ref]
		if q == nil {
			return fmt.Errorf("unknown query reference %q", ref)
		}
		active[ref] = true
		for _, dep := range q.dependencies {
			if err := visit(dep); err != nil {
				return fmt.Errorf("query %s: %w", ref, err)
			}
		}
		delete(active, ref)
		visited[ref] = true
		return nil
	}
	for _, q := range p.Data {
		if err := visit(q.RefID); err != nil {
			return nil, err
		}
	}
	return &p, nil
}

// All ranges use the caller's scheduled timestamp, including relativeTimeRange
// offsets. Expressions retain their own provisioned ranges, as in Grafana.
func (p *Plan) Groups(at time.Time) map[string][]backend.DataQuery {
	groups := map[string][]backend.DataQuery{}
	for _, q := range p.Data {
		query := backend.DataQuery{RefID: q.RefID, QueryType: q.QueryType, JSON: q.Model, Interval: q.interval, MaxDataPoints: q.points,
			TimeRange: backend.TimeRange{From: at.Add(-time.Duration(q.RelativeTimeRange.From) * time.Second), To: at.Add(-time.Duration(q.RelativeTimeRange.To) * time.Second)}}
		groups[q.DatasourceUID] = append(groups[q.DatasourceUID], query)
	}
	return groups
}
