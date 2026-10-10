package alerting

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/alertgraph"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/prometheus/prometheus/promql/parser"
)

type GrafanaQuery struct {
	RefID             string `json:"refId"`
	QueryType         string `json:"queryType"`
	RelativeTimeRange struct {
		From int64 `json:"from"`
		To   int64 `json:"to"`
	} `json:"relativeTimeRange"`
	DatasourceUID string          `json:"datasourceUid"`
	Model         json.RawMessage `json:"model"`
}
type GrafanaRule struct {
	ID                   int64             `json:"id,omitempty"`
	UID                  string            `json:"uid"`
	OrgID                int64             `json:"orgID"`
	FolderUID            string            `json:"folderUID"`
	RuleGroup            string            `json:"ruleGroup"`
	Title                string            `json:"title"`
	Condition            string            `json:"condition"`
	Data                 []GrafanaQuery    `json:"data"`
	For                  string            `json:"for"`
	KeepFiringFor        string            `json:"keepFiringFor"`
	NoDataState          string            `json:"noDataState"`
	ExecErrState         string            `json:"execErrState"`
	Annotations          map[string]string `json:"annotations"`
	Labels               map[string]string `json:"labels"`
	IsPaused             bool              `json:"isPaused"`
	Updated              string            `json:"updated,omitempty"`
	Provenance           string            `json:"provenance,omitempty"`
	NotificationSettings json.RawMessage   `json:"notification_settings,omitempty"`
	Record               *struct {
		Metric              string `json:"metric"`
		From                string `json:"from"`
		TargetDatasourceUID string `json:"target_datasource_uid"`
	} `json:"record,omitempty"`
}
type queryModel struct {
	Expr       string `json:"expr"`
	Instant    bool   `json:"instant"`
	Type       string `json:"type"`
	Expression string `json:"expression"`
	Reducer    string `json:"reducer"`
	IntervalMS int64  `json:"intervalMs"`
	Settings   *struct {
		Mode string `json:"mode"`
	} `json:"settings"`
	Conditions []struct {
		UnloadEvaluator json.RawMessage `json:"unloadEvaluator"`
		Evaluator       struct {
			Type   string    `json:"type"`
			Params []float64 `json:"params"`
		} `json:"evaluator"`
		Operator struct {
			Type string `json:"type"`
		} `json:"operator"`
		Query struct {
			Params []string `json:"params"`
		} `json:"query"`
		Reducer struct {
			Type string `json:"type"`
		} `json:"reducer"`
	} `json:"conditions"`
}
type compiled struct {
	expr   string
	window int64
	step   int64
}

var refPattern = regexp.MustCompile(`\$([A-Za-z][A-Za-z0-9_]*)`)

func durationSeconds(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 || d%time.Second != 0 {
		return 0, errors.New("duration must be a nonnegative whole number of seconds")
	}
	return int(d / time.Second), nil
}
func reduce(c compiled, reducer string) (compiled, error) {
	if reducer == "" {
		reducer = "last"
	}
	if c.window == 0 {
		if reducer != "last" {
			return c, errors.New("reduce needs a range query; instant queries support last only")
		}
		return c, nil
	}
	fn := map[string]string{"last": "last_over_time", "min": "min_over_time", "max": "max_over_time", "mean": "avg_over_time", "avg": "avg_over_time", "sum": "sum_over_time", "count": "count_over_time"}[reducer]
	if fn == "" {
		return c, fmt.Errorf("unsupported Grafana reducer %q", reducer)
	}
	return compiled{expr: fmt.Sprintf("%s((%s)[%ds:%dms])", fn, c.expr, c.window, c.step)}, nil
}
func threshold(expr, kind string, params []float64) (string, error) {
	if len(params) == 0 {
		return "", errors.New("threshold needs a value")
	}
	value := func(i int) string { return strconv.FormatFloat(params[i], 'g', -1, 64) }
	op := map[string]string{"gt": ">", "lt": "<", "eq": "==", "ne": "!=", "gte": ">=", "lte": "<="}[kind]
	if op != "" {
		if len(params) != 1 {
			return "", errors.New("comparison threshold needs exactly one value; recovery thresholds are not supported")
		}
		return fmt.Sprintf("((%s) %s bool %s)", expr, op, value(0)), nil
	}
	if len(params) != 2 {
		return "", errors.New("range threshold needs two values")
	}
	switch kind {
	case "within_range":
		return fmt.Sprintf("(((%s) > bool %s) * ((%s) < bool %s))", expr, value(0), expr, value(1)), nil
	case "outside_range":
		return fmt.Sprintf("clamp_max(((%s) < bool %s) + ((%s) > bool %s),1)", expr, value(0), expr, value(1)), nil
	default:
		return "", fmt.Errorf("unsupported threshold %q", kind)
	}
}

// compileLegacyGrafana reproduces the original PromQL compiler for comparing
// previously stored rules when native edits determine whether to keep export data.
func compileLegacyGrafana(g GrafanaRule, interval int) (model.AlertRule, error) {
	r := model.AlertRule{UID: g.UID, Title: g.Title, Condition: "nonzero", IntervalSeconds: interval, NoDataState: g.NoDataState, ErrorState: g.ExecErrState, Labels: g.Labels, Annotations: g.Annotations, Paused: g.IsPaused, FolderUID: g.FolderUID, Group: g.RuleGroup}
	if r.NoDataState == "" {
		r.NoDataState = "NoData"
	}
	for _, value := range g.Labels {
		if strings.Contains(value, "{{") {
			return r, errors.New("templated alert labels are not supported")
		}
	}
	if g.OrgID != 0 && g.OrgID != 1 {
		return r, errors.New("only organization 1 is supported")
	}
	if len(g.NotificationSettings) > 0 && string(g.NotificationSettings) != "null" {
		return r, errors.New("notification_settings requires a notification integration, which is not configured")
	}
	var err error
	if r.ForSeconds, err = durationSeconds(g.For); err != nil {
		return r, err
	}
	if r.KeepFiringForSeconds, err = durationSeconds(g.KeepFiringFor); err != nil {
		return r, err
	}
	if len(g.Data) == 0 || len(g.Data) > 32 {
		return r, errors.New("Grafana rule needs 1–32 query nodes")
	}
	nodes := map[string]GrafanaQuery{}
	for _, q := range g.Data {
		if !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`).MatchString(q.RefID) {
			return r, errors.New("invalid query refId")
		}
		if _, ok := nodes[q.RefID]; ok {
			return r, errors.New("duplicate query refId")
		}
		nodes[q.RefID] = q
	}
	cache := map[string]compiled{}
	visiting := map[string]bool{}
	var visit func(string) (compiled, error)
	visit = func(id string) (compiled, error) {
		if c, ok := cache[id]; ok {
			return c, nil
		}
		if visiting[id] {
			return compiled{}, errors.New("query dependency cycle")
		}
		q, ok := nodes[id]
		if !ok {
			return compiled{}, fmt.Errorf("unknown query reference %q", id)
		}
		visiting[id] = true
		defer delete(visiting, id)
		var m queryModel
		if err := json.Unmarshal(q.Model, &m); err != nil {
			return compiled{}, err
		}
		for _, condition := range m.Conditions {
			if len(condition.UnloadEvaluator) > 0 && string(condition.UnloadEvaluator) != "null" {
				return compiled{}, errors.New("custom recovery thresholds are not supported")
			}
		}
		var c compiled
		if q.DatasourceUID != "__expr__" && q.DatasourceUID != "-100" {
			if q.DatasourceUID != "metricspanel" && q.DatasourceUID != "prometheus" {
				return c, fmt.Errorf("datasource %q must be mapped to metricspanel before import", q.DatasourceUID)
			}
			if q.QueryType != "" {
				return c, errors.New("unsupported Prometheus queryType")
			}
			if len(m.Expr) == 0 || len(m.Expr) > 10000 {
				return c, errors.New("Prometheus query needs an expression under 10000 bytes")
			}
			c.expr = m.Expr
			if q.RelativeTimeRange.To != 0 {
				return c, errors.New("relativeTimeRange.to must be zero")
			}
			if !m.Instant {
				c.window = q.RelativeTimeRange.From
				if c.window < 1 || c.window > 86400 {
					return c, errors.New("range query window must be 1–86400 seconds")
				}
				c.step = m.IntervalMS
				if c.step == 0 {
					c.step = 15000
				}
				if c.step < 1000 {
					return c, errors.New("query intervalMs must be at least 1000")
				}
				if c.window*1000/c.step > 2000 {
					return c, errors.New("range query exceeds 2000 evaluation points")
				}
			}
		} else {
			switch m.Type {
			case "reduce":
				if m.Settings != nil && m.Settings.Mode != "" && m.Settings.Mode != "strict" {
					return c, errors.New("only strict reduction is supported")
				}
				source, err := visit(strings.TrimPrefix(m.Expression, "$"))
				if err != nil {
					return c, err
				}
				c, err = reduce(source, m.Reducer)
				if err != nil {
					return c, err
				}
			case "threshold":
				if len(m.Conditions) != 1 {
					return c, errors.New("threshold needs one condition")
				}
				source, err := visit(strings.TrimPrefix(m.Expression, "$"))
				if err != nil {
					return c, err
				}
				if source.window != 0 {
					return c, errors.New("threshold requires a reduced or instant query")
				}
				c.expr, err = threshold(source.expr, m.Conditions[0].Evaluator.Type, m.Conditions[0].Evaluator.Params)
				if err != nil {
					return c, err
				}
			case "classic_conditions":
				if len(m.Conditions) != 1 {
					return c, errors.New("classic_conditions currently supports one condition; use threshold/math nodes for compound expressions")
				}
				{
					cond := m.Conditions[0]
					if len(cond.Query.Params) == 0 {
						return c, errors.New("classic condition needs a query")
					}
					source, err := visit(cond.Query.Params[0])
					if err != nil {
						return c, err
					}
					source, err = reduce(source, cond.Reducer.Type)
					if err != nil {
						return c, err
					}
					expr, err := threshold(source.expr, cond.Evaluator.Type, cond.Evaluator.Params)
					if err != nil {
						return c, err
					}
					// Classic conditions create one instance, dropping query labels.
					c.expr = "(max(" + expr + "))"
				}
			case "math":
				if strings.ContainsAny(m.Expression, "&|!") || len(m.Expression) > 10000 {
					return c, errors.New("math supports PromQL arithmetic and comparisons; logical operators need threshold/classic nodes")
				}
				var refErr error
				refs := map[string]bool{}
				for _, match := range refPattern.FindAllStringSubmatch(m.Expression, -1) {
					refs[match[1]] = true
				}
				if len(refs) > 1 {
					return c, errors.New("math with multiple query references requires Grafana label joins, which are not supported")
				}
				c.expr = refPattern.ReplaceAllStringFunc(m.Expression, func(ref string) string {
					source, err := visit(ref[1:])
					if err != nil {
						refErr = err
						return "0"
					}
					if source.window != 0 {
						refErr = errors.New("math requires reduced or instant queries")
					}
					return "(" + source.expr + ")"
				})
				if refErr != nil {
					return c, refErr
				}
				if strings.Contains(c.expr, "$") {
					return c, errors.New("unsupported math reference")
				}
				parsed, err := parser.NewParser(parser.Options{}).ParseExpr(c.expr)
				if err != nil {
					return c, err
				}
				// Grafana comparisons retain false values. Preserve bool semantics.
				parser.Inspect(parsed, func(node parser.Node, _ []parser.Node) error {
					if b, ok := node.(*parser.BinaryExpr); ok && b.Op.IsComparisonOperator() {
						b.ReturnBool = true
					}
					return nil
				})
				c.expr = parsed.String()
			default:
				return c, fmt.Errorf("unsupported Grafana expression node %q", m.Type)
			}
		}
		if len(c.expr) > 10000 {
			return c, errors.New("compiled query exceeds 10000 bytes")
		}
		parsed, err := parser.NewParser(parser.Options{}).ParseExpr(c.expr)
		if err != nil {
			return c, err
		}
		if parsed.Type() == parser.ValueTypeScalar {
			c.expr = "vector(" + c.expr + ")"
		}
		cache[id] = c
		return c, nil
	}
	condition := g.Condition
	if g.Record != nil {
		if g.Record.Metric == "" {
			return r, errors.New("recording rule needs a metric name")
		}
		if g.Record.TargetDatasourceUID != "" && g.Record.TargetDatasourceUID != "metricspanel" {
			return r, errors.New("recording target must be metricspanel")
		}
		r.Record = g.Record.Metric
		condition = g.Record.From
	}
	compiled, err := visit(condition)
	if err != nil {
		return r, err
	}
	if compiled.window != 0 {
		return r, errors.New("rule condition must use an instant or reduced query")
	}
	r.Expr = compiled.expr
	// Validate unused nodes too, so a successful import never hides an invalid graph.
	for id := range nodes {
		if _, err := visit(id); err != nil {
			return r, err
		}
	}
	r.Defaults()
	if err := r.Validate(); err != nil {
		return r, err
	}
	g.OrgID = 1
	g.UID = r.UID
	g.Provenance = "api"
	r.Provenance = &g.Provenance
	payload, err := json.Marshal(g)
	r.Grafana = payload
	return r, err
}

// CompileGrafana persists the validated graph and uses the shared SDK-frame
// expression runtime instead of rewriting Grafana operations into PromQL.
func CompileGrafana(g GrafanaRule, interval int) (model.AlertRule, error) {
	r := model.AlertRule{UID: g.UID, Title: g.Title, Execution: "grafana", Condition: "nonzero", IntervalSeconds: interval, NoDataState: g.NoDataState, ErrorState: g.ExecErrState, Labels: g.Labels, Annotations: g.Annotations, Paused: g.IsPaused, FolderUID: g.FolderUID, Group: g.RuleGroup}
	if g.OrgID != 0 && g.OrgID != 1 {
		return r, errors.New("only organization 1 is supported")
	}
	if len(g.NotificationSettings) > 0 && string(g.NotificationSettings) != "null" {
		return r, errors.New("notification_settings requires a notification integration, which is not configured")
	}
	for _, value := range g.Labels {
		if strings.Contains(value, "{{") {
			return r, errors.New("templated alert labels are not supported")
		}
	}
	var err error
	if r.ForSeconds, err = durationSeconds(g.For); err != nil {
		return r, err
	}
	if r.KeepFiringForSeconds, err = durationSeconds(g.KeepFiringFor); err != nil {
		return r, err
	}
	if g.Record != nil {
		if g.Record.TargetDatasourceUID != "" && g.Record.TargetDatasourceUID != "metricspanel" {
			return r, errors.New("recording target must be metricspanel")
		}
		r.Record = g.Record.Metric
	}
	if g.Record != nil && g.Record.Metric == "" {
		return r, errors.New("recording rule needs a metric name")
	}
	g.OrgID = 1
	g.Provenance = "api"
	r.Provenance = &g.Provenance
	r.Grafana, err = json.Marshal(g)
	if err != nil {
		return r, err
	}
	r.Defaults()
	if err = r.Validate(); err != nil {
		return r, err
	}
	return r, nil
}

// Previously stored rules without execution=grafana retain their native
// PromQL path. Native edits still discard a stale legacy export graph.
func LegacyGraphMatches(rule model.AlertRule) bool {
	var g GrafanaRule
	if json.Unmarshal(rule.Grafana, &g) != nil {
		return false
	}
	compiled, err := compileLegacyGrafana(g, rule.IntervalSeconds)
	if err != nil || rule.Condition != "nonzero" || compiled.Record != rule.Record {
		return false
	}
	a, err := parser.NewParser(parser.Options{}).ParseExpr(compiled.Expr)
	if err != nil {
		return false
	}
	b, err := parser.NewParser(parser.Options{}).ParseExpr(rule.Expr)
	return err == nil && a.String() == b.String()
}

func ExportGrafana(r model.AlertRule) GrafanaRule {
	var g GrafanaRule
	if len(r.Grafana) > 0 {
		_ = json.Unmarshal(r.Grafana, &g)
		if r.Execution == "grafana" {
			if plan, err := alertgraph.Parse(r.Grafana); err == nil {
				if r.Record == "" {
					g.Record = nil
					g.Condition = plan.Condition
				} else {
					g.Record = &struct {
						Metric              string `json:"metric"`
						From                string `json:"from"`
						TargetDatasourceUID string `json:"target_datasource_uid"`
					}{r.Record, plan.Condition, "metricspanel"}
				}
			}
		}
	} else {
		g.Condition = "A"
		query := GrafanaQuery{RefID: "A", DatasourceUID: "metricspanel"}
		expr := r.Expr
		if r.Record == "" && r.Condition == "presence" {
			expr = "((" + expr + ") * 0 + 1)"
		}
		query.Model, _ = json.Marshal(map[string]any{"expr": expr, "instant": true, "refId": "A"})
		g.Data = []GrafanaQuery{query}
		if r.Record != "" {
			g.Record = &struct {
				Metric              string `json:"metric"`
				From                string `json:"from"`
				TargetDatasourceUID string `json:"target_datasource_uid"`
			}{r.Record, "A", "metricspanel"}
			g.Condition = ""
		}
	}
	g.UID = r.UID
	g.OrgID = 1
	g.Title = r.Title
	g.FolderUID = r.FolderUID
	g.RuleGroup = r.Group
	g.For = strconv.Itoa(r.ForSeconds) + "s"
	g.KeepFiringFor = strconv.Itoa(r.KeepFiringForSeconds) + "s"
	g.NoDataState = r.NoDataState
	g.ExecErrState = r.ErrorState
	g.Labels = r.Labels
	g.Annotations = r.Annotations
	g.IsPaused = r.Paused
	g.Updated = time.UnixMilli(r.UpdatedAt).UTC().Format(time.RFC3339Nano)
	g.Provenance = "api"
	if r.Provenance != nil {
		g.Provenance = *r.Provenance
	}
	return g
}
