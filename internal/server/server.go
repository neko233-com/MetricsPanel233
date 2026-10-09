package server

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/neko233-com/MetricsPanel233/internal/collector"
	"github.com/neko233-com/MetricsPanel233/internal/grafana"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/promcompat"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	webassets "github.com/neko233-com/MetricsPanel233/web"
)

type Server struct {
	Store         *store.Store
	Collector     *collector.Collector
	Token         string
	RetentionDays int
	Started       time.Time
	requests      atomic.Uint64
}

func New(s *store.Store, token string, retention int) *Server {
	return &Server{Store: s, Collector: collector.New(s), Token: token, RetentionDays: retention, Started: time.Now()}
}
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Error("encode response", "error", err)
	}
}
func fail(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": err.Error(), "status": status}})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("Content-Type must be application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4*1024*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("request must contain exactly one JSON value")
	}
	return nil
}
func (s *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				fail(w, 403, errors.New("cross-origin requests are not allowed"))
				return
			}
		}
		if s.Token != "" {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(s.Token)) != 1 {
				fail(w, 401, errors.New("valid Bearer token required"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	api := http.NewServeMux()
	prom := promcompat.New(s.Store)
	s.grafanaRoutes(api, prom.Handler())
	mux.Handle("/prometheus/", s.protect(http.StripPrefix("/prometheus", prom.Handler())))
	api.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Store.Health(r.Context()); err != nil {
			fail(w, 503, err)
			return
		}
		writeJSON(w, 200, map[string]any{"status": "ok", "version": "0.1.0", "storage": s.Store.Kind(), "started_at": s.Started.UnixMilli()})
	})
	api.HandleFunc("GET /api/v1/metrics", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Store.Metrics(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, v)
	})
	api.HandleFunc("GET /api/v1/stats", s.stats)
	api.HandleFunc("GET /api/v1/query", s.query)
	api.HandleFunc("POST /api/v1/ingest", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Samples []model.Sample `json:"samples"`
		}
		if err := decode(w, r, &v); err != nil {
			fail(w, 400, err)
			return
		}
		if err := s.Store.Ingest(r.Context(), v.Samples); err != nil {
			fail(w, 400, err)
			return
		}
		writeJSON(w, 200, map[string]int{"accepted": len(v.Samples)})
	})
	api.HandleFunc("GET /api/v1/targets", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Store.Targets(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, v)
	})
	api.HandleFunc("POST /api/v1/targets", s.saveTarget)
	api.HandleFunc("PUT /api/v1/targets/{id}", s.saveTarget)
	api.HandleFunc("DELETE /api/v1/targets/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			fail(w, 400, err)
			return
		}
		if err = s.Store.DeleteTarget(r.Context(), id); err != nil {
			resourceError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
	api.HandleFunc("POST /api/v1/targets/{id}/scrape", s.scrape)
	api.HandleFunc("GET /api/v1/dashboards", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Store.Dashboards(r.Context())
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, v)
	})
	api.HandleFunc("POST /api/v1/dashboards", s.saveDashboard)
	api.HandleFunc("POST /api/v1/import/grafana", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4*1024*1024)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			fail(w, 400, err)
			return
		}
		result, err := grafana.Import(data)
		if err != nil {
			fail(w, 400, err)
			return
		}
		result.Dashboard, err = s.Store.SaveDashboard(r.Context(), result.Dashboard)
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, 200, result)
	})
	api.HandleFunc("PUT /api/v1/dashboards/{id}", s.saveDashboard)
	api.HandleFunc("DELETE /api/v1/dashboards/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "system" {
			fail(w, 400, errors.New("system dashboard cannot be deleted"))
			return
		}
		if err := s.Store.DeleteDashboard(r.Context(), r.PathValue("id")); err != nil {
			resourceError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
	})
	mux.Handle("/api/", s.protect(api))
	mux.Handle("GET /metrics", s.protect(http.HandlerFunc(s.prometheus)))
	assets := webassets.Files
	files := http.FileServer(http.FS(assets))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'")
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(assets, path); err != nil {
			if strings.Contains(path, ".") {
				http.NotFound(w, r)
				return
			}
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.requests.Add(1); mux.ServeHTTP(w, r) })
}

func resourceError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, errors.New("resource not found"))
		return
	}
	fail(w, 500, err)
}
func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.Stats(r.Context())
	if err != nil {
		fail(w, 500, err)
		return
	}
	targets, err := s.Store.Targets(r.Context())
	if err != nil {
		fail(w, 500, err)
		return
	}
	v.RetentionDays = s.RetentionDays
	v.StartedAt = s.Started.UnixMilli()
	v.CollectorsTotal = 1
	v.CollectorsOnline = 1
	for _, t := range targets {
		if !t.Enabled {
			continue
		}
		v.CollectorsTotal++
		if t.LastError == "" && t.LastScrape > 0 && time.Now().UnixMilli()-t.LastScrape < int64(t.IntervalSeconds*2+10)*1000 {
			v.CollectorsOnline++
		}
	}
	writeJSON(w, 200, v)
}
func (s *Server) query(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	now := time.Now().UnixMilli()
	start := now - int64(30*time.Minute/time.Millisecond)
	end := now
	for _, item := range []struct {
		name string
		v    *int64
	}{{"start", &start}, {"end", &end}} {
		if raw := params.Get(item.name); raw != "" {
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				fail(w, 400, fmt.Errorf("%s must be Unix milliseconds", item.name))
				return
			}
			*item.v = v
		}
	}
	if rangeValue := params.Get("range"); rangeValue != "" {
		d, err := time.ParseDuration(rangeValue)
		if err != nil || d <= 0 {
			fail(w, 400, errors.New("range must be a positive Go duration, e.g. 30m or 24h"))
			return
		}
		start = end - d.Milliseconds()
	}
	step := int64(math.Max(1000, math.Ceil(float64(end-start)/120/1000)*1000))
	if raw := params.Get("step"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			fail(w, 400, err)
			return
		}
		step = d.Milliseconds()
	}
	labels := map[string]string{}
	if raw := params.Get("labels"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &labels); err != nil {
			fail(w, 400, errors.New("labels must be a JSON object of string values"))
			return
		}
	}
	aggregation := params.Get("aggregation")
	if aggregation == "" {
		aggregation = "last"
	}
	v, err := s.Store.Query(r.Context(), model.Query{Metric: params.Get("metric"), Start: start, End: end, Step: step, Labels: labels, Aggregation: aggregation})
	if err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) saveTarget(w http.ResponseWriter, r *http.Request) {
	t := model.Target{Enabled: true, IntervalSeconds: 15}
	if err := decode(w, r, &t); err != nil {
		fail(w, 400, err)
		return
	}
	if raw := r.PathValue("id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			fail(w, 400, errors.New("invalid target ID"))
			return
		}
		t.ID = id
	} else {
		t.ID = 0
	}
	if err := t.Validate(); err != nil {
		fail(w, 400, err)
		return
	}
	v, err := s.Store.SaveTarget(r.Context(), t)
	if err != nil {
		resourceError(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) scrape(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, err)
		return
	}
	targets, err := s.Store.Targets(r.Context())
	if err != nil {
		fail(w, 500, err)
		return
	}
	for _, t := range targets {
		if t.ID == id {
			ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
			defer cancel()
			n, err := s.Collector.Scrape(ctx, t)
			if err != nil {
				fail(w, 502, err)
				return
			}
			writeJSON(w, 200, map[string]int{"accepted": n})
			return
		}
	}
	fail(w, 404, errors.New("target not found"))
}
func (s *Server) saveDashboard(w http.ResponseWriter, r *http.Request) {
	var d model.Dashboard
	if err := decode(w, r, &d); err != nil {
		fail(w, 400, err)
		return
	}
	if r.PathValue("id") != "" {
		d.ID = r.PathValue("id")
	} else {
		d.ID = uuid.NewString()
	}
	if err := d.Validate(); err != nil {
		fail(w, 400, err)
		return
	}
	v, err := s.Store.SaveDashboard(r.Context(), d)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) selfSamples() []model.Sample {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	hostname, _ := os.Hostname()
	labels := map[string]string{"job": "metricspanel", "instance": hostname}
	return []model.Sample{{Name: "metricspanel_memory_bytes", Value: float64(m.Alloc), Labels: labels}, {Name: "metricspanel_goroutines", Value: float64(runtime.NumGoroutine()), Labels: labels}, {Name: "metricspanel_http_requests_total", Value: float64(s.requests.Load()), Labels: labels}, {Name: "metricspanel_uptime_seconds", Value: time.Since(s.Started).Seconds(), Labels: labels}}
}
func (s *Server) prometheus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	for _, v := range s.selfSamples() {
		kind := "gauge"
		if strings.HasSuffix(v.Name, "_total") {
			kind = "counter"
		}
		fmt.Fprintf(w, "# TYPE %s %s\n%s %g\n", v.Name, kind, v.Name, v.Value)
	}
}
func (s *Server) RunBackground(ctx context.Context) {
	collectorDone := make(chan struct{})
	go func() { s.Collector.Run(ctx); close(collectorDone) }()
	defer func() { <-collectorDone }()
	sample := time.NewTicker(5 * time.Second)
	defer sample.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	ingest := func() {
		if err := s.Store.Ingest(ctx, s.selfSamples()); err != nil && ctx.Err() == nil {
			slog.Error("self collection", "error", err)
		}
	}
	cleanup := func() {
		if err := s.Store.Prune(ctx, time.Now().Add(-time.Duration(s.RetentionDays)*24*time.Hour).UnixMilli()); err != nil && ctx.Err() == nil {
			slog.Error("retention cleanup", "error", err)
		}
	}
	cleanup()
	ingest()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sample.C:
			ingest()
		case <-prune.C:
			cleanup()
		}
	}
}
