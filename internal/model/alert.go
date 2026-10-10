package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/neko233-com/MetricsPanel233/internal/alertgraph"
	"github.com/prometheus/prometheus/promql/parser"
)

var alertUID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

// AlertRule also represents a recording rule when Record is nonempty. Grafana
// keeps the accepted provisioning payload; Execution selects its graph or Expr.
type AlertRule struct {
	UID                      string            `json:"uid"`
	Title                    string            `json:"title"`
	Expr                     string            `json:"expr"`
	Execution                string            `json:"execution,omitempty"`
	Condition                string            `json:"condition"` // presence (Prometheus) or nonzero (Grafana)
	IntervalSeconds          int               `json:"interval_seconds"`
	ForSeconds               int               `json:"for_seconds"`
	KeepFiringForSeconds     int               `json:"keep_firing_for_seconds"`
	NoDataState              string            `json:"no_data_state"`
	ErrorState               string            `json:"error_state"`
	MissingSeriesEvaluations int               `json:"missing_series_evaluations"`
	Labels                   map[string]string `json:"labels"`
	Annotations              map[string]string `json:"annotations"`
	Paused                   bool              `json:"paused"`
	Record                   string            `json:"record,omitempty"`
	FolderUID                string            `json:"folder_uid"`
	Group                    string            `json:"group"`
	GroupIndex               int               `json:"group_index,omitempty"`
	Provenance               *string           `json:"provenance,omitempty"`
	Version                  int               `json:"version"`
	UpdatedAt                int64             `json:"updated_at"`
	Grafana                  json.RawMessage   `json:"grafana,omitempty"`
}

func (r *AlertRule) Defaults() {
	if r.Condition == "" {
		if r.Execution == "grafana" {
			r.Condition = "nonzero"
		} else {
			r.Condition = "presence"
		}
	}
	if r.IntervalSeconds == 0 {
		r.IntervalSeconds = 30
	}
	if r.NoDataState == "" {
		if r.Execution == "grafana" {
			r.NoDataState = "NoData"
		} else {
			r.NoDataState = "OK"
		}
	}
	if r.ErrorState == "" {
		r.ErrorState = "Error"
	}
	if r.MissingSeriesEvaluations == 0 {
		r.MissingSeriesEvaluations = 2
	}
	if r.FolderUID == "" {
		r.FolderUID = "general"
	}
	if r.Group == "" {
		r.Group = "default"
	}
}
func (r AlertRule) Validate() error {
	if !alertUID.MatchString(r.UID) || len(r.Title) == 0 || len(r.Title) > 256 {
		return errors.New("rule needs a valid UID and title (1–256 bytes)")
	}
	if r.Execution == "grafana" {
		if r.Expr != "" || r.Condition != "nonzero" {
			return errors.New("Grafana graph execution requires an empty expr and nonzero condition")
		}
		if _, err := alertgraph.Parse(r.Grafana); err != nil {
			return err
		}
	} else {
		if r.Execution != "" && r.Execution != "promql" {
			return errors.New("execution must be promql or grafana")
		}
		if len(r.Expr) == 0 || len(r.Expr) > 10000 {
			return errors.New("PromQL expression must be 1–10000 bytes")
		}
		expr, err := parser.NewParser(parser.Options{}).ParseExpr(r.Expr)
		if err != nil {
			return fmt.Errorf("invalid PromQL: %w", err)
		}
		if expr.Type() != parser.ValueTypeVector && expr.Type() != parser.ValueTypeScalar {
			return errors.New("alert expression must return an instant vector or scalar")
		}
	}
	if r.Condition != "presence" && r.Condition != "nonzero" {
		return errors.New("condition must be presence or nonzero")
	}
	if r.IntervalSeconds < 5 || r.IntervalSeconds > 86400 || r.ForSeconds < 0 || r.ForSeconds > 604800 || r.KeepFiringForSeconds < 0 || r.KeepFiringForSeconds > 604800 {
		return errors.New("evaluation interval: 5–86400s; pending/recovery periods: 0–604800s")
	}
	if r.MissingSeriesEvaluations < 1 || r.MissingSeriesEvaluations > 100 {
		return errors.New("missing_series_evaluations must be 1–100")
	}
	for _, p := range []string{r.NoDataState, r.ErrorState} {
		if p != "OK" && p != "Alerting" && p != "NoData" && p != "Error" && p != "KeepLast" {
			return errors.New("state policy must be OK, Alerting, NoData, Error or KeepLast")
		}
	}
	if len(r.FolderUID) > 100 || len(r.Group) > 100 || r.GroupIndex < 0 || r.GroupIndex >= 1000 || len(r.Annotations) > 32 {
		return errors.New("folder/group limit: 100 bytes; group index: 0–999; annotation limit: 32")
	}
	if r.Provenance != nil && *r.Provenance != "" && *r.Provenance != "api" {
		return errors.New("provenance must be empty or api")
	}
	for key, value := range r.Annotations {
		if len(key) == 0 || len(key) > 100 || len(value) > 4096 {
			return errors.New("annotation key/value limit: 100/4096 bytes")
		}
	}
	if uid, panel := r.Annotations["__dashboardUid__"], r.Annotations["__panelId__"]; uid != "" || panel != "" {
		number, err := strconv.ParseInt(panel, 10, 64)
		if uid == "" || len(uid) > 128 || err != nil || number <= 0 || number > 9007199254740991 {
			return errors.New("alert dashboard link requires __dashboardUid__ and a positive __panelId__")
		}
	}
	if err := ValidateLabels(r.Labels); err != nil {
		return err
	}
	if r.Record != "" && (!metricName.MatchString(r.Record) || r.ForSeconds != 0 || r.KeepFiringForSeconds != 0) {
		return errors.New("recording rule needs a valid metric name and zero pending/recovery periods")
	}
	if len(r.Grafana) > 512*1024 || (len(r.Grafana) > 0 && !json.Valid(r.Grafana)) {
		return errors.New("Grafana rule payload must be valid JSON under 512 KiB")
	}
	return nil
}

type AlertInstance struct {
	Key                string            `json:"key"`
	Labels             map[string]string `json:"labels"`
	State              string            `json:"state"`
	Value              *float64          `json:"value"`
	ValueText          string            `json:"value_text,omitempty"`
	Matches            json.RawMessage   `json:"matches,omitempty"`
	ActiveAt           int64             `json:"active_at"`
	FiringAt           int64             `json:"firing_at"`
	RecoveringAt       int64             `json:"recovering_at"`
	UpdatedAt          int64             `json:"updated_at"`
	MissingEvaluations int               `json:"missing_evaluations"`
	Reason             string            `json:"reason,omitempty"`
}
type AlertRuntime struct {
	LastEvaluation int64           `json:"last_evaluation"`
	DurationMS     int64           `json:"duration_ms"`
	Health         string          `json:"health"`
	Error          string          `json:"error,omitempty"`
	Instances      []AlertInstance `json:"instances"`
}
type AlertRuleView struct {
	AlertRule
	Runtime AlertRuntime `json:"runtime"`
}
type AlertEvent struct {
	ID         int64             `json:"id"`
	UID        string            `json:"uid"`
	Key        string            `json:"key"`
	Labels     map[string]string `json:"labels"`
	From       string            `json:"from"`
	To         string            `json:"to"`
	Timestamp  int64             `json:"timestamp"`
	Reason     string            `json:"reason,omitempty"`
	PrevReason string            `json:"prev_reason,omitempty"`
	Value      *float64          `json:"value,omitempty"`
	ValueText  string            `json:"value_text,omitempty"`
	Error      string            `json:"error,omitempty"`
}
