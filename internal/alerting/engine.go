package alerting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/promcompat"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
)

type Value struct {
	Labels map[string]string
	Value  float64
}
type QueryFunc func(context.Context, string, time.Time) ([]Value, error)
type Engine struct {
	Store  *store.Store
	Query  QueryFunc
	mu     sync.Mutex
	active map[string]bool
	slots  chan struct{}
}

func New(s *store.Store, prom *promcompat.API) *Engine {
	e := &Engine{Store: s, active: map[string]bool{}, slots: make(chan struct{}, 4)}
	e.Query = func(ctx context.Context, expr string, at time.Time) ([]Value, error) {
		query, err := prom.Engine.NewInstantQuery(ctx, prom.Queryable, promql.NewPrometheusQueryOpts(false, 0), expr, at)
		if err != nil {
			return nil, err
		}
		defer query.Close()
		result := query.Exec(ctx)
		if result.Err != nil {
			return nil, result.Err
		}
		out := []Value{}
		switch values := result.Value.(type) {
		case promql.Vector:
			if len(values) > 1000 {
				return nil, errors.New("alert query exceeds 1000 instances")
			}
			for _, v := range values {
				if v.H != nil || math.IsNaN(v.F) || math.IsInf(v.F, 0) {
					return nil, errors.New("alert query must return finite float values")
				}
				ls := map[string]string{}
				v.Metric.Range(func(l labels.Label) {
					if l.Name != "__name__" {
						ls[l.Name] = l.Value
					}
				})
				out = append(out, Value{Labels: ls, Value: v.F})
			}
		case promql.Scalar:
			if math.IsNaN(values.V) || math.IsInf(values.V, 0) {
				return nil, errors.New("alert query must return finite values")
			}
			out = append(out, Value{Labels: map[string]string{}, Value: values.V})
		default:
			return nil, errors.New("alert query must return an instant vector or scalar")
		}
		return out, nil
	}
	return e
}
func instanceKey(ls map[string]string) string {
	sum := sha256.Sum256([]byte(model.LabelsJSON(ls)))
	return hex.EncodeToString(sum[:])
}
func mergedLabels(source, extra map[string]string, title string) map[string]string {
	out := map[string]string{}
	for k, v := range source {
		if k != "__name__" {
			out[k] = v
		}
	}
	for k, v := range extra {
		out[k] = v
	}
	if title != "" {
		out["alertname"] = title
	}
	return out
}
func normal(v model.AlertInstance) model.AlertInstance {
	v.State = "Normal"
	v.ActiveAt = 0
	v.FiringAt = 0
	v.RecoveringAt = 0
	return v
}
func advance(rule model.AlertRule, v model.AlertInstance, active bool, at int64) model.AlertInstance {
	if !active {
		if (v.State == "Firing" || v.State == "Recovering") && rule.KeepFiringForSeconds > 0 {
			if v.RecoveringAt == 0 {
				v.RecoveringAt = at
			}
			v.State = "Recovering"
			if at-v.RecoveringAt >= int64(rule.KeepFiringForSeconds)*1000 {
				v = normal(v)
			}
		} else {
			v = normal(v)
		}
		return v
	}
	if v.State == "Firing" || v.State == "Recovering" {
		v.State = "Firing"
		v.RecoveringAt = 0
		return v
	}
	if v.State != "Pending" {
		v.ActiveAt = at
		v.FiringAt = 0
	}
	v.State = "Pending"
	v.RecoveringAt = 0
	if at-v.ActiveAt >= int64(rule.ForSeconds)*1000 {
		v.State = "Firing"
		v.FiringAt = at
	}
	return v
}

// Transition is deterministic: evaluation timestamps are supplied by the caller,
// and persisted timers are used even after the process is restarted.
func Transition(rule model.AlertRule, previous model.AlertRuntime, values []Value, queryErr error, at int64) (model.AlertRuntime, []model.AlertEvent, error) {
	out := model.AlertRuntime{LastEvaluation: at, Health: "ok", Instances: []model.AlertInstance{}}
	old := map[string]model.AlertInstance{}
	for _, v := range previous.Instances {
		old[v.Key] = v
	}
	current := map[string]model.AlertInstance{}
	policy := ""
	reason := ""
	if queryErr != nil {
		out.Health = "error"
		out.Error = queryErr.Error()
		policy = rule.ErrorState
		reason = "QueryError"
	} else if len(values) == 0 {
		out.Health = "nodata"
		policy = rule.NoDataState
		reason = "NoData"
	}
	if rule.Record != "" {
		return out, nil, nil
	}
	if policy != "" {
		if len(old) == 0 && policy != "OK" && policy != "KeepLast" {
			ls := mergedLabels(nil, rule.Labels, rule.Title)
			k := instanceKey(ls)
			old[k] = model.AlertInstance{Key: k, Labels: ls, State: "Normal"}
		}
		for key, v := range old {
			v.UpdatedAt = at
			v.Reason = reason
			switch policy {
			case "KeepLast": // Retain state and timers; no synthetic instance is created.
			case "OK":
				v = advance(rule, v, false, at)
				v.Value = nil
			case "Alerting":
				v = advance(rule, v, true, at)
				v.Value = nil
			case "NoData", "Error":
				v.State = policy
				if v.ActiveAt == 0 {
					v.ActiveAt = at
				}
				if v.FiringAt == 0 {
					v.FiringAt = at
				}
				v.RecoveringAt = 0
				v.Value = nil
			}
			current[key] = v
		}
	} else {
		for _, value := range values {
			if math.IsNaN(value.Value) || math.IsInf(value.Value, 0) {
				return out, nil, errors.New("alert value must be finite")
			}
			ls := mergedLabels(value.Labels, rule.Labels, rule.Title)
			key := instanceKey(ls)
			if _, duplicate := current[key]; duplicate {
				return out, nil, errors.New("rule labels collapse multiple query series into one alert instance")
			}
			v, exists := old[key]
			if !exists {
				v = model.AlertInstance{Key: key, Labels: ls, State: "Normal"}
			}
			n := value.Value
			v.Value = &n
			v.UpdatedAt = at
			v.MissingEvaluations = 0
			v.Reason = ""
			v = advance(rule, v, rule.Condition == "presence" || value.Value != 0, at)
			current[key] = v
		}
		for key, v := range old {
			if _, exists := current[key]; exists {
				continue
			}
			if v.State == "Normal" {
				continue
			}
			v.MissingEvaluations++
			v.UpdatedAt = at
			missing := rule.MissingSeriesEvaluations
			if rule.Condition == "presence" {
				missing = 1
			}
			if v.MissingEvaluations >= missing {
				v.Reason = "MissingSeries"
				v.Value = nil
				v = advance(rule, v, false, at)
			}
			current[key] = v
		}
	}
	events := []model.AlertEvent{}
	if len(current) > 1000 {
		return out, nil, errors.New("alert runtime exceeds 1000 retained instances")
	}
	for key, v := range current {
		before := old[key].State
		if before == "" {
			before = "Normal"
		}
		if before != v.State {
			events = append(events, model.AlertEvent{UID: rule.UID, Key: key, Labels: v.Labels, From: before, To: v.State, Timestamp: at, Reason: v.Reason, PrevReason: old[key].Reason, Value: v.Value, Error: out.Error})
		}
		out.Instances = append(out.Instances, v)
	}
	sort.Slice(out.Instances, func(i, j int) bool { return out.Instances[i].Key < out.Instances[j].Key })
	sort.Slice(events, func(i, j int) bool { return events[i].Key < events[j].Key })
	return out, events, nil
}

var ErrBusy = errors.New("rule is already being evaluated")

// Mutations share the same per-rule gate as evaluation. In particular, a recording
// rule cannot write samples from an obsolete configuration during an API edit.
func (e *Engine) mutate(uid string, action func() error) error {
	e.mu.Lock()
	if e.active[uid] {
		e.mu.Unlock()
		return ErrBusy
	}
	e.active[uid] = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); delete(e.active, uid); e.mu.Unlock() }()
	return action()
}
func (e *Engine) SaveRule(ctx context.Context, rule model.AlertRule) (model.AlertRuleView, error) {
	var view model.AlertRuleView
	err := e.mutate(rule.UID, func() error { var err error; view, err = e.Store.SaveAlertRule(ctx, rule); return err })
	return view, err
}
func (e *Engine) DeleteRule(ctx context.Context, uid string) error {
	return e.mutate(uid, func() error { return e.Store.DeleteAlertRule(ctx, uid) })
}

func (e *Engine) Evaluate(ctx context.Context, uid string, at time.Time) (model.AlertRuleView, error) {
	e.mu.Lock()
	if e.active[uid] {
		e.mu.Unlock()
		return model.AlertRuleView{}, ErrBusy
	}
	e.active[uid] = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); delete(e.active, uid); e.mu.Unlock() }()
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	case <-ctx.Done():
		return model.AlertRuleView{}, ctx.Err()
	}
	view, err := e.Store.AlertRule(ctx, uid)
	if err != nil {
		return view, err
	}
	if view.Paused {
		return view, errors.New("rule is paused")
	}
	if at.UnixMilli() <= view.Runtime.LastEvaluation {
		return view, store.ErrStaleEvaluation
	}
	started := time.Now()
	queryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	values, queryErr := e.Query(queryCtx, view.Expr, at)
	if ctx.Err() != nil {
		return view, ctx.Err()
	} // Shutdown/canceled requests never become alert failures.
	runtime, events, stateErr := Transition(view.AlertRule, view.Runtime, values, queryErr, at.UnixMilli())
	if stateErr != nil {
		runtime, events, _ = Transition(view.AlertRule, view.Runtime, nil, stateErr, at.UnixMilli())
	}
	if view.Record != "" && queryErr == nil && len(values) > 0 {
		samples := []model.Sample{}
		seen := map[string]bool{}
		for _, v := range values {
			ls := mergedLabels(v.Labels, view.Labels, "")
			key := instanceKey(ls)
			if seen[key] {
				queryErr = errors.New("recording labels collapse multiple query series")
				break
			}
			seen[key] = true
			samples = append(samples, model.Sample{Name: view.Record, Labels: ls, Value: v.Value, Timestamp: at.UnixMilli()})
		}
		if queryErr == nil {
			queryErr = e.Store.Ingest(queryCtx, samples)
		}
		if queryErr != nil {
			runtime.Health = "error"
			runtime.Error = queryErr.Error()
		}
	}
	runtime.DurationMS = time.Since(started).Milliseconds()
	if err = e.Store.CommitAlertEvaluation(ctx, uid, view.Version, runtime, events); err != nil {
		return view, err
	}
	view.Runtime = runtime
	return view, nil
}
func (e *Engine) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var workers sync.WaitGroup
	defer workers.Wait()
	// The dispatcher limits queued work too, so 1000 rules cannot spawn 1000
	// blocked goroutines. Each rule has one in-flight evaluation at most.
	dispatch := make(chan struct{}, 4)
	for {
		select {
		case <-ctx.Done():
			return
		case at := <-ticker.C:
			rules, err := e.Store.DueAlertRules(ctx, at.UnixMilli())
			if err != nil {
				if ctx.Err() == nil {
					slog.Error("load alert rules", "error", err)
				}
				continue
			}
			for _, uid := range rules {
				select {
				case dispatch <- struct{}{}:
					workers.Add(1)
					go func(uid string) {
						defer workers.Done()
						defer func() { <-dispatch }()
						if _, err := e.Evaluate(ctx, uid, at); err != nil && ctx.Err() == nil && !errors.Is(err, ErrBusy) && !errors.Is(err, store.ErrStaleEvaluation) {
							slog.Error("evaluate rule", "uid", uid, "error", fmt.Sprint(err))
						}
					}(uid)
				default:
				}
			}
		}
	}
}
