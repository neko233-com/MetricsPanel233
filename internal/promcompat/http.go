package promcompat

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
)

type API struct {
	Store     *store.Store
	Engine    *promql.Engine
	Queryable Queryable
}

func New(s *store.Store) *API {
	return &API{Store: s, Queryable: Queryable{Store: s}, Engine: promql.NewEngine(promql.EngineOpts{Logger: slog.Default(), MaxSamples: 1000000, Timeout: 20 * time.Second, LookbackDelta: 5 * time.Minute, NoStepSubqueryIntervalFn: func(int64) int64 { return 15000 }, EnableAtModifier: true, EnableNegativeOffset: true})}
}
func respond(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(data)
}
func success(w http.ResponseWriter, data any) {
	respond(w, 200, map[string]any{"status": "success", "data": data})
}
func failure(w http.ResponseWriter, err error) {
	respond(w, 400, map[string]any{"status": "error", "errorType": "bad_data", "error": err.Error()})
}
func parseTime(raw string, fallback time.Time) (time.Time, error) {
	if raw == "" {
		return fallback, nil
	}
	if n, err := strconv.ParseFloat(raw, 64); err == nil {
		if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 253402300799 {
			return time.Time{}, errors.New("invalid timestamp")
		}
		return time.UnixMilli(int64(n * 1000)), nil
	}
	return time.Parse(time.RFC3339Nano, raw)
}
func parseStep(raw string) (time.Duration, error) {
	if n, err := strconv.ParseFloat(raw, 64); err == nil {
		if math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > 2678400 {
			return 0, errors.New("invalid step")
		}
		return time.Duration(n * float64(time.Second)), nil
	}
	return time.ParseDuration(raw)
}
func labelMap(ls labels.Labels) map[string]string {
	out := map[string]string{}
	ls.Range(func(l labels.Label) { out[l.Name] = l.Value })
	return out
}
func valuePair(timestamp int64, value float64) []any {
	return []any{float64(timestamp) / 1000, strconv.FormatFloat(value, 'g', -1, 64)}
}
func resultData(result *promql.Result) (any, error) {
	if result.Err != nil {
		return nil, result.Err
	}
	switch v := result.Value.(type) {
	case promql.Vector:
		out := []any{}
		for _, s := range v {
			out = append(out, map[string]any{"metric": labelMap(s.Metric), "value": valuePair(s.T, s.F)})
		}
		return map[string]any{"resultType": "vector", "result": out}, nil
	case promql.Matrix:
		out := []any{}
		for _, s := range v {
			points := []any{}
			for _, p := range s.Floats {
				points = append(points, valuePair(p.T, p.F))
			}
			out = append(out, map[string]any{"metric": labelMap(s.Metric), "values": points})
		}
		return map[string]any{"resultType": "matrix", "result": out}, nil
	case promql.Scalar:
		return map[string]any{"resultType": "scalar", "result": valuePair(v.T, v.V)}, nil
	case promql.String:
		return map[string]any{"resultType": "string", "result": []any{float64(v.T) / 1000, v.V}}, nil
	}
	return nil, errors.New("unsupported result type")
}
func (a *API) query(w http.ResponseWriter, r *http.Request, isRange bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 4*1024*1024)
	if err := r.ParseForm(); err != nil {
		failure(w, err)
		return
	}
	expr := r.Form.Get("query")
	if expr == "" || len(expr) > 10000 {
		failure(w, errors.New("query expression required (max 10000 bytes)"))
		return
	}
	var query promql.Query
	var err error
	if isRange {
		start, e := parseTime(r.Form.Get("start"), time.Now().Add(-30*time.Minute))
		if e != nil {
			failure(w, e)
			return
		}
		end, e := parseTime(r.Form.Get("end"), time.Now())
		if e != nil {
			failure(w, e)
			return
		}
		step, e := parseStep(r.Form.Get("step"))
		if e != nil || step < time.Second || end.Before(start) || end.Sub(start) > 31*24*time.Hour || end.Sub(start)/step > 2000 {
			failure(w, errors.New("range <=31 days, step >=1s, <=2000 points required"))
			return
		}
		query, err = a.Engine.NewRangeQuery(r.Context(), a.Queryable, promql.NewPrometheusQueryOpts(false, 0), expr, start, end, step)
	} else {
		at, e := parseTime(r.Form.Get("time"), time.Now())
		if e != nil {
			failure(w, e)
			return
		}
		query, err = a.Engine.NewInstantQuery(r.Context(), a.Queryable, promql.NewPrometheusQueryOpts(false, 0), expr, at)
	}
	if err != nil {
		failure(w, err)
		return
	}
	defer query.Close()
	result := query.Exec(r.Context())
	data, err := resultData(result)
	if err != nil {
		failure(w, err)
		return
	}
	warnings, infos := result.Warnings.AsStrings(expr, 10, 10)
	out := map[string]any{"status": "success", "data": data}
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	if len(infos) > 0 {
		out["infos"] = infos
	}
	respond(w, 200, out)
}
func (a *API) discovery(w http.ResponseWriter, r *http.Request, kind string) {
	r.Body = http.MaxBytesReader(w, r.Body, 4*1024*1024)
	if err := r.ParseForm(); err != nil {
		failure(w, err)
		return
	}
	start, err := parseTime(r.Form.Get("start"), time.Now().Add(-24*time.Hour))
	if err != nil {
		failure(w, err)
		return
	}
	end, err := parseTime(r.Form.Get("end"), time.Now())
	if err != nil {
		failure(w, err)
		return
	}
	if end.Before(start) || end.Sub(start) > 31*24*time.Hour {
		failure(w, errors.New("invalid discovery range"))
		return
	}
	groups := [][]*labels.Matcher{}
	for _, selector := range r.Form["match[]"] {
		matchers, err := parser.NewParser(parser.Options{}).ParseMetricSelector(selector)
		if err != nil {
			failure(w, err)
			return
		}
		groups = append(groups, matchers)
	}
	if len(groups) == 0 {
		groups = append(groups, nil)
	}
	values := map[string]bool{}
	seriesMap := map[string]map[string]string{}
	for _, group := range groups {
		raw, err := a.Store.LoadSeries(r.Context(), start.UnixMilli(), end.UnixMilli(), convertMatchers(group))
		if err != nil {
			failure(w, err)
			return
		}
		for _, s := range raw {
			ls := map[string]string{"__name__": s.Name}
			for k, v := range s.Labels {
				ls[k] = v
			}
			switch kind {
			case "labels":
				for k := range ls {
					values[k] = true
				}
			case "values":
				if v, ok := ls[r.PathValue("name")]; ok {
					values[v] = true
				}
			case "series":
				b, _ := json.Marshal(ls)
				seriesMap[string(b)] = ls
			}
		}
	}
	if kind == "series" {
		keys := []string{}
		for k := range seriesMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := []map[string]string{}
		for _, k := range keys {
			out = append(out, seriesMap[k])
		}
		success(w, out)
		return
	}
	out := []string{}
	for v := range values {
		out = append(out, v)
	}
	sort.Strings(out)
	success(w, out)
}
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, method := range []string{"GET", "POST"} {
		mux.HandleFunc(method+" /api/v1/query", func(w http.ResponseWriter, r *http.Request) { a.query(w, r, false) })
		mux.HandleFunc(method+" /api/v1/query_range", func(w http.ResponseWriter, r *http.Request) { a.query(w, r, true) })
		for _, item := range []struct{ path, kind string }{{"/api/v1/labels", "labels"}, {"/api/v1/label/{name}/values", "values"}, {"/api/v1/series", "series"}} {
			mux.HandleFunc(method+" "+item.path, func(w http.ResponseWriter, r *http.Request) { a.discovery(w, r, item.kind) })
		}
	}
	mux.HandleFunc("GET /api/v1/status/buildinfo", func(w http.ResponseWriter, _ *http.Request) {
		success(w, map[string]string{"version": "3.15.0", "revision": "MetricsPanel233", "goVersion": "go1.27.0"})
	})
	mux.HandleFunc("GET /api/v1/metadata", func(w http.ResponseWriter, r *http.Request) {
		metrics, err := a.Store.Metrics(r.Context())
		if err != nil {
			failure(w, err)
			return
		}
		out := map[string]any{}
		for _, m := range metrics {
			if filter := r.URL.Query().Get("metric"); filter != "" && filter != m.Name {
				continue
			}
			out[m.Name] = []any{map[string]string{"type": "unknown", "help": "", "unit": ""}}
		}
		success(w, out)
	})
	mux.HandleFunc("GET /api/v1/targets", func(w http.ResponseWriter, r *http.Request) {
		targets, err := a.Store.Targets(r.Context())
		if err != nil {
			failure(w, err)
			return
		}
		out := []any{}
		for _, t := range targets {
			health := "unknown"
			if t.LastScrape > 0 {
				health = "up"
				if t.LastError != "" {
					health = "down"
				}
			}
			out = append(out, map[string]any{"labels": map[string]string{"job": t.Name, "instance": t.URL}, "scrapeUrl": t.URL, "health": health, "lastError": t.LastError, "lastScrape": time.UnixMilli(t.LastScrape).Format(time.RFC3339Nano), "scrapeInterval": fmt.Sprintf("%ds", t.IntervalSeconds)})
		}
		success(w, map[string]any{"activeTargets": out, "droppedTargets": []any{}})
	})
	return mux
}
