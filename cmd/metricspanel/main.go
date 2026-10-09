package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
)

const version = "0.1.0"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"error": map[string]string{"message": err.Error()}})
		os.Exit(1)
	}
}
func output(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func usage() {
	fmt.Fprintln(os.Stdout, `MetricsPanel233 — metrics, storage, dashboards and an agent-native CLI

Usage: metricspanel <command> [flags]

  serve        Start collector, SQLite storage and embedded web UI
  health       Check server health
  stats        Show storage and collection statistics
  metrics      List metric names and series counts
  query        Query a metric (--metric NAME --range 30m --aggregation last)
  ingest       Push JSON samples (--file samples.json or --file - for stdin)
  targets      list | add | set | delete | scrape
  dashboards   list | save | export | delete
  patterns     capture | list | get | search | delete (persistent vector analysis)
  alerts       list | get | save | import-grafana | evaluate | history | delete
  schema       Print machine-readable command and API discovery
  version      Print version

All client commands return JSON on stdout. Errors are JSON on stderr (exit 1).
Client flags: --server URL, --token TOKEN, --json (JSON is already the default).
Environment: METRICSPANEL_URL, METRICSPANEL_TOKEN.
Run metricspanel <command> --help for command flags.
Example: metricspanel targets add --name api --url http://localhost:8080/metrics`)
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		usage()
		return nil
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "version":
		return output(map[string]string{"version": version, "go": "1.27"})
	case "schema":
		return output(schema())
	}
	command := args[0]
	rest := args[1:]
	action := ""
	if command == "targets" || command == "dashboards" || command == "patterns" || command == "alerts" {
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			return fmt.Errorf("%s requires an action (see --help)", command)
		}
		action = rest[0]
		rest = rest[1:]
	}
	f := flag.NewFlagSet(command+" "+action, flag.ContinueOnError)
	endpoint := f.String("server", env("METRICSPANEL_URL", "http://127.0.0.1:7333"), "server URL")
	token := f.String("token", env("METRICSPANEL_TOKEN", ""), "API Bearer token; prefer environment variable")
	f.Bool("json", true, "JSON output (always enabled)")
	metric := f.String("metric", "", "metric name")
	expression := f.String("expr", "", "PromQL expression; uses the upstream Prometheus engine")
	queryRange := f.String("range", "30m", "lookback duration (maximum 744h)")
	aggregation := f.String("aggregation", "last", "last, avg, sum, min, max, rate")
	step := f.String("step", "", "bucket duration (automatic by default)")
	labels := f.String("labels", "{}", "JSON object of labels / equality filters")
	file := f.String("file", "-", "JSON file; - reads stdin")
	format := f.String("format", "native", "dashboard export format: native or grafana (original source)")
	id := f.String("id", "", "resource ID")
	name := f.String("name", "", "collector name")
	targetURL := f.String("url", "", "exporter /metrics URL")
	interval := f.Duration("interval", 15*time.Second, "scrape interval (5s–24h)")
	enabled := f.Bool("enabled", true, "enable automatic collection")
	start := f.Int64("start", 0, "pattern window start: Unix milliseconds (optional)")
	end := f.Int64("end", 0, "pattern window end: Unix milliseconds (optional)")
	normalization := f.String("normalization", "shape", "pattern comparison: shape (remove level/scale) or raw")
	limit := f.Int("limit", 10, "pattern search result limit (1–100); list limit (1–1000)")
	exact := f.Bool("exact", false, "exact vector scan instead of approximate HNSW search")
	includeSelf := f.Bool("include-self", false, "include the reference pattern in search results")
	if err := f.Parse(rest); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments; use named flags")
	}
	client := apiClient{endpoint: strings.TrimRight(*endpoint, "/"), token: *token}
	request := func(method, path string, payload any) error {
		result, err := client.call(method, path, payload)
		if err != nil {
			return err
		}
		return output(result)
	}
	var filter map[string]string
	if err := json.Unmarshal([]byte(*labels), &filter); err != nil {
		return fmt.Errorf("invalid --labels: %w", err)
	}
	switch command {
	case "alerts":
		switch action {
		case "list":
			return request("GET", "/api/v1/alerts/rules", nil)
		case "history":
			q := url.Values{"limit": {strconv.Itoa(*limit)}}
			if *id != "" {
				q.Set("uid", *id)
			}
			return request("GET", "/api/v1/alerts/history?"+q.Encode(), nil)
		case "get", "delete", "evaluate":
			if *id == "" {
				return errors.New("--id is required")
			}
			path := "/api/v1/alerts/rules/" + url.PathEscape(*id)
			method := "GET"
			if action == "delete" {
				method = "DELETE"
			}
			if action == "evaluate" {
				method = "POST"
				path += "/evaluate"
			}
			return request(method, path, nil)
		case "save", "import-grafana":
			data, err := readFile(*file)
			if err != nil {
				return err
			}
			var payload any
			if err = json.Unmarshal(data, &payload); err != nil {
				return err
			}
			if action == "save" {
				if object, ok := payload.(map[string]any); ok {
					delete(object, "runtime")
				}
			}
			path := "/api/v1/alerts/rules"
			if action == "import-grafana" {
				path = "/api/v1/provisioning/alert-rules"
			}
			method := "POST"
			if *id != "" {
				method = "PUT"
				path += "/" + url.PathEscape(*id)
			}
			return request(method, path, payload)
		default:
			return errors.New("alerts requires list, get, save, import-grafana, evaluate, history or delete")
		}
	case "patterns":
		switch action {
		case "list":
			return request("GET", "/api/v1/patterns?limit="+strconv.Itoa(*limit), nil)
		case "get", "delete":
			if *id == "" {
				return errors.New("--id is required")
			}
			method := "GET"
			if action == "delete" {
				method = "DELETE"
			}
			return request(method, "/api/v1/patterns/"+url.PathEscape(*id), nil)
		case "capture":
			if *metric == "" {
				return errors.New("--metric is required")
			}
			return request("POST", "/api/v1/patterns/capture", map[string]any{"metric": *metric, "labels": filter, "range": *queryRange, "start": *start, "end": *end, "aggregation": *aggregation, "normalization": *normalization})
		case "search":
			if *id == "" {
				return errors.New("--id is required")
			}
			return request("POST", "/api/v1/patterns/search", map[string]any{"id": *id, "metric": *metric, "labels": filter, "limit": *limit, "exact": *exact, "include_self": *includeSelf})
		default:
			return errors.New("patterns requires list, get, capture, search or delete")
		}
	case "health", "stats", "metrics":
		return request("GET", "/api/v1/"+command, nil)
	case "query":
		if *expression != "" {
			d, err := time.ParseDuration(*queryRange)
			if err != nil || d <= 0 {
				return errors.New("--range must be a positive duration")
			}
			end := time.Now()
			resolution := time.Duration(float64(d) / 120)
			if resolution < time.Second {
				resolution = time.Second
			}
			if *step != "" {
				resolution, err = time.ParseDuration(*step)
				if err != nil {
					return err
				}
			}
			q := url.Values{"query": {*expression}, "start": {strconv.FormatFloat(float64(end.Add(-d).UnixMilli())/1000, 'f', 3, 64)}, "end": {strconv.FormatFloat(float64(end.UnixMilli())/1000, 'f', 3, 64)}, "step": {strconv.FormatFloat(resolution.Seconds(), 'f', 3, 64)}}
			return request("GET", "/prometheus/api/v1/query_range?"+q.Encode(), nil)
		}
		if *metric == "" {
			return errors.New("--metric is required")
		}
		q := url.Values{"metric": {*metric}, "range": {*queryRange}, "aggregation": {*aggregation}, "labels": {*labels}}
		if *step != "" {
			q.Set("step", *step)
		}
		return request("GET", "/api/v1/query?"+q.Encode(), nil)
	case "ingest":
		data, err := readFile(*file)
		if err != nil {
			return err
		}
		var body struct {
			Samples []model.Sample `json:"samples"`
		}
		if err = json.Unmarshal(data, &body); err != nil {
			return err
		}
		return request("POST", "/api/v1/ingest", body)
	case "targets":
		switch action {
		case "list":
			return request("GET", "/api/v1/targets", nil)
		case "add", "set":
			t := model.Target{Name: *name, URL: *targetURL, IntervalSeconds: int(interval.Seconds()), Labels: filter, Enabled: *enabled}
			if err := t.Validate(); err != nil {
				return err
			}
			if action == "set" {
				if *id == "" {
					return errors.New("--id is required")
				}
				return request("PUT", "/api/v1/targets/"+url.PathEscape(*id), t)
			}
			return request("POST", "/api/v1/targets", t)
		case "delete", "scrape":
			if *id == "" {
				return errors.New("--id is required")
			}
			path := "/api/v1/targets/" + url.PathEscape(*id)
			method := "DELETE"
			if action == "scrape" {
				path += "/scrape"
				method = "POST"
			}
			return request(method, path, nil)
		}
	case "dashboards":
		switch action {
		case "list":
			return request("GET", "/api/v1/dashboards", nil)
		case "save":
			data, err := readFile(*file)
			if err != nil {
				return err
			}
			var raw map[string]json.RawMessage
			if err = json.Unmarshal(data, &raw); err != nil {
				return err
			}
			if raw["title"] != nil || raw["dashboard"] != nil || raw["spec"] != nil {
				return request("POST", "/api/v1/import/grafana", json.RawMessage(data))
			}
			var d model.Dashboard
			if err = json.Unmarshal(data, &d); err != nil {
				return err
			}
			if err = d.Validate(); err != nil {
				return err
			}
			if *id != "" {
				d.ID = *id
			}
			if d.ID != "" {
				return request("PUT", "/api/v1/dashboards/"+url.PathEscape(d.ID), d)
			}
			return request("POST", "/api/v1/dashboards", d)
		case "export":
			if *id == "" {
				return errors.New("--id is required")
			}
			data, err := client.call("GET", "/api/v1/dashboards", nil)
			if err != nil {
				return err
			}
			var dashboards []model.Dashboard
			if err = json.Unmarshal(data, &dashboards); err != nil {
				return err
			}
			for _, d := range dashboards {
				if d.ID == *id {
					if *format == "grafana" {
						if len(d.Grafana) == 0 {
							return errors.New("dashboard has no original Grafana source")
						}
						return output(d.Grafana)
					}
					if *format != "native" {
						return errors.New("--format must be native or grafana")
					}
					return output(d)
				}
			}
			return errors.New("dashboard not found")
		case "delete":
			if *id == "" {
				return errors.New("--id is required")
			}
			return request("DELETE", "/api/v1/dashboards/"+url.PathEscape(*id), nil)
		}
	}
	return fmt.Errorf("unknown command/action: %s %s", command, action)
}

func readFile(path string) ([]byte, error) {
	if path != "-" {
		return os.ReadFile(path)
	}
	return io.ReadAll(io.LimitReader(os.Stdin, 4*1024*1024+1))
}

type apiClient struct{ endpoint, token string }

func (c apiClient) call(method, path string, payload any) (json.RawMessage, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.endpoint+path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, e.Error.Message)
		}
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if !json.Valid(data) {
		return nil, errors.New("server returned invalid JSON")
	}
	return json.RawMessage(data), nil
}

func serve(args []string) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := f.String("listen", "127.0.0.1:7333", "HTTP listen address")
	dbPath := f.String("db", "data/metrics.db", "SQLite database path")
	retention := f.Int("retention-days", 30, "sample retention in days (1–3650)")
	backend := f.String("storage", env("METRICSPANEL_STORAGE", "sqlite"), "metrics backend: sqlite or clickhouse")
	clickhouseURL := f.String("clickhouse-url", env("CLICKHOUSE_URL", "http://127.0.0.1:8123"), "ClickHouse HTTP endpoint")
	token := f.String("token", env("METRICSPANEL_TOKEN", ""), "API token; mandatory on non-loopback interfaces")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *retention < 1 || *retention > 3650 {
		return errors.New("retention-days must be between 1 and 3650")
	}
	host, port, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	if _, err = strconv.Atoi(port); err != nil {
		return errors.New("listen port must be numeric")
	}
	ip := net.ParseIP(host)
	if (ip == nil || !ip.IsLoopback()) && host != "localhost" && len(*token) < 16 {
		return errors.New("non-loopback listen requires METRICSPANEL_TOKEN with at least 16 characters")
	}
	storage, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer storage.DB.Close()
	if *backend == "clickhouse" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		engine, err := store.OpenClickHouse(ctx, *clickhouseURL, env("CLICKHOUSE_DATABASE", "metricspanel"), env("CLICKHOUSE_USER", "default"), env("CLICKHOUSE_PASSWORD", ""), *retention)
		if err != nil {
			return err
		}
		storage.Backend = engine
	} else if *backend != "sqlite" {
		return errors.New("--storage must be sqlite or clickhouse")
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	srv := server.New(storage, *token, *retention)
	backgroundDone := make(chan struct{})
	go func() { srv.RunBackground(ctx); close(backgroundDone) }()
	httpServer := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()
	slog.Info("MetricsPanel233 started", "address", listener.Addr().String(), "database", *dbPath, "retention_days", *retention, "auth", *token != "")
	select {
	case <-ctx.Done():
	case err = <-serveErr:
		cancel()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	shutdownErr := httpServer.Shutdown(shutdownCtx)
	cancel()
	<-backgroundDone
	if shutdownErr != nil {
		return shutdownErr
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func schema() any {
	result := map[string]any{
		"name": "metricspanel", "version": version, "output": "JSON stdout; JSON errors stderr; exit 0 success / 1 failure",
		"environment":    map[string]string{"METRICSPANEL_URL": "http://127.0.0.1:7333", "METRICSPANEL_TOKEN": "Bearer token (optional on loopback)"},
		"commands":       []string{"serve [--listen --db --retention-days]", "health", "stats", "metrics", "query --metric NAME [--range 30m --step 15s --aggregation last --labels '{}']", "ingest --file FILE|-", "targets list", "targets add --name NAME --url URL [--interval 15s --labels '{}']", "targets set --id ID --name NAME --url URL [--enabled=false]", "targets scrape --id ID", "targets delete --id ID", "dashboards list", "dashboards save --file FILE|- [--id ID]", "dashboards export --id ID", "dashboards delete --id ID", "schema", "version"},
		"api":            map[string][]string{"GET": {"/api/v1/health", "/api/v1/stats", "/api/v1/metrics", "/api/v1/query", "/api/v1/targets", "/api/v1/dashboards", "/metrics"}, "POST": {"/api/v1/ingest", "/api/v1/targets", "/api/v1/targets/{id}/scrape", "/api/v1/dashboards"}, "PUT": {"/api/v1/targets/{id}", "/api/v1/dashboards/{id}"}, "DELETE": {"/api/v1/targets/{id}", "/api/v1/dashboards/{id}"}},
		"ingest_example": map[string]any{"samples": []model.Sample{{Name: "app_requests_total", Labels: map[string]string{"service": "api"}, Value: 42}}},
		"timestamps":     "Unix milliseconds; ingest timestamp=0 uses server time",
		"aggregations":   []string{"last", "avg", "sum", "min", "max", "rate"},
		"query_limits":   map[string]int{"max_range_days": 31, "max_series": 200, "max_samples": 250000, "max_buckets": 2000},
	}
	result["commands"] = append(result["commands"].([]string), "patterns capture --metric NAME [--range 30m --start MS --end MS --normalization shape|raw --aggregation last|rate --labels '{}']", "patterns list [--limit 1000]", "patterns get --id ID", "patterns search --id ID [--metric NAME --labels '{}' --limit 10 --exact --include-self]", "patterns delete --id ID")
	routes := result["api"].(map[string][]string)
	routes["GET"] = append(routes["GET"], "/api/v1/patterns", "/api/v1/patterns/{id}")
	routes["POST"] = append(routes["POST"], "/api/v1/patterns/capture", "/api/v1/patterns/search")
	routes["DELETE"] = append(routes["DELETE"], "/api/v1/patterns/{id}")
	result["commands"] = append(result["commands"].([]string), "alerts list", "alerts get --id UID", "alerts save --file FILE|- [--id UID] (update requires version)", "alerts import-grafana --file FILE|- [--id UID]", "alerts evaluate --id UID", "alerts history [--id UID --limit 100]", "alerts delete --id UID")
	routes["GET"] = append(routes["GET"], "/api/v1/alerts/rules", "/api/v1/alerts/rules/{uid}", "/api/v1/alerts/history", "/prometheus/api/v1/rules", "/prometheus/api/v1/alerts", "/api/v1/provisioning/alert-rules")
	routes["POST"] = append(routes["POST"], "/api/v1/alerts/rules", "/api/v1/alerts/rules/{uid}/evaluate", "/api/v1/provisioning/alert-rules")
	routes["PUT"] = append(routes["PUT"], "/api/v1/alerts/rules/{uid}", "/api/v1/provisioning/alert-rules/{uid}")
	routes["DELETE"] = append(routes["DELETE"], "/api/v1/alerts/rules/{uid}", "/api/v1/provisioning/alert-rules/{uid}")
	result["alerting"] = map[string]any{"conditions": []string{"presence (returned samples fire, including zero)", "nonzero (boolean expressions)"}, "states": []string{"Normal", "Pending", "Firing", "Recovering", "NoData", "Error"}, "durability": "SQLite WAL control-plane state, timers and last 100000 transitions survive restarts", "limits": map[string]int{"rules": 1000, "instances_per_query": 1000, "concurrent_evaluations": 4}, "notifications": "external notification delivery is not yet implemented"}
	result["pattern_analysis"] = map[string]any{"dimensions": 64, "normalizations": []string{"shape", "raw"}, "distance": "L2 (smaller is closer; not a probability)", "capture_min_coverage": 0.75, "max_gap_buckets": 8, "capture_max_series": 200, "search_limit": 100, "sqlite": "exact scan, at most 50000 filtered vectors", "clickhouse": "persistent HNSW; --exact disables approximate indexing", "idempotency": "content-addressed immutable windows; explicit start/end make repeat capture reproducible"}
	return result
}
