package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/neko233-com/MetricsPanel233/internal/live"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/plugins"
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
  plugins      list | get | install | catalog | enable | disable | delete
               settings | configure (Grafana application settings)
  datasources  list | get | save | query | health | delete
  live         channels | watch | publish (watch emits NDJSON)
  schema       Print machine-readable command and API discovery
  version      Print version

Client commands return JSON on stdout; live watch and query --stream emit NDJSON.
Errors are JSON on stderr (exit 1).
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
	if command == "targets" || command == "dashboards" || command == "patterns" || command == "alerts" || command == "plugins" || command == "datasources" || command == "live" {
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
	limit := f.Int("limit", 10, "pattern search limit (1–100), list limit (1–1000), Live event limit (0 unlimited)")
	exact := f.Bool("exact", false, "exact vector scan instead of approximate HNSW search")
	includeSelf := f.Bool("include-self", false, "include the reference pattern in search results")
	pluginVersion := f.String("plugin-version", "", "exact Grafana catalog plugin version")
	channel := f.String("channel", "", "Grafana Live channel: ds/UID/path or plugin/ID/path")
	metadata := f.String("metadata", "null", "Live subscription metadata (JSON)")
	duration := f.Duration("duration", time.Minute, "Live watch / datasource query duration (0 disables client deadline)")
	stream := f.Bool("stream", false, "datasource query: emit Grafana text/jsonl chunks as NDJSON")
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
	case "live":
		switch action {
		case "channels":
			return request("GET", "/api/live/channels", nil)
		case "publish":
			if _, _, _, err := live.ParseChannel(*channel); err != nil {
				return err
			}
			data, err := readFile(*file)
			if err != nil {
				return err
			}
			if len(data) > 1<<20 || !json.Valid(data) {
				return errors.New("publication must be JSON under 1 MiB")
			}
			return request("POST", "/api/live/publish", map[string]any{"channel": *channel, "data": json.RawMessage(data)})
		case "watch":
			if *duration < 0 {
				return errors.New("--duration must be nonnegative")
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if *duration > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, *duration)
				defer cancel()
			}
			encoder := json.NewEncoder(os.Stdout)
			err := live.Watch(ctx, live.WatchOptions{Endpoint: client.endpoint, Token: client.token, Channel: *channel, Metadata: json.RawMessage(*metadata), Limit: *limit}, func(event live.Event) error { return encoder.Encode(event) })
			if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				return nil
			}
			return err
		default:
			return errors.New("live requires channels, watch or publish")
		}
	case "plugins":
		switch action {
		case "settings":
			if *id == "" {
				return errors.New("--id required")
			}
			return request("GET", "/api/v1/plugins/"+url.PathEscape(*id)+"/app-settings", nil)
		case "configure":
			if *id == "" {
				return errors.New("--id required")
			}
			data, err := readFile(*file)
			if err != nil {
				return err
			}
			var payload model.AppSettingsInput
			if err = json.Unmarshal(data, &payload); err != nil {
				return err
			}
			return request("PUT", "/api/v1/plugins/"+url.PathEscape(*id)+"/app-settings", payload)
		case "list":
			return request("GET", "/api/v1/plugins", nil)
		case "get", "enable", "disable", "delete":
			if *id == "" {
				return errors.New("--id required")
			}
			method := "GET"
			var payload any
			if action == "delete" {
				method = "DELETE"
			}
			if action == "enable" || action == "disable" {
				method = "PUT"
				payload = map[string]bool{"enabled": action == "enable"}
			}
			return request(method, "/api/v1/plugins/"+url.PathEscape(*id), payload)
		case "catalog":
			if *id == "" || *pluginVersion == "" {
				return errors.New("--id and --plugin-version required")
			}
			return request("POST", "/api/v1/plugins/catalog", map[string]string{"id": *id, "version": *pluginVersion})
		case "install":
			if *file == "-" {
				return errors.New("plugin installation needs --file ZIP")
			}
			f, err := os.Open(*file)
			if err != nil {
				return err
			}
			defer f.Close()
			data, err := io.ReadAll(io.LimitReader(f, plugins.MaxArchiveBytes+1))
			if err != nil {
				return err
			}
			if len(data) > plugins.MaxArchiveBytes {
				return errors.New("plugin ZIP exceeds 64 MiB")
			}
			result, err := client.uploadPlugin(data)
			if err != nil {
				return err
			}
			return output(result)
		default:
			return errors.New("plugins requires list, get, install, catalog, enable, disable or delete")
		}
	case "datasources":
		switch action {
		case "query":
			if *id == "" {
				return errors.New("--id required")
			}
			if *duration < 0 {
				return errors.New("--duration must be nonnegative")
			}
			data, err := readFile(*file)
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if *duration > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, *duration)
				defer cancel()
			}
			return client.queryDatasource(ctx, *id, data, *stream, os.Stdout)
		case "list":
			return request("GET", "/api/datasources", nil)
		case "get", "health", "delete":
			if *id == "" {
				return errors.New("--id required")
			}
			method := "GET"
			path := "/api/datasources/uid/" + url.PathEscape(*id)
			if action == "health" {
				path += "/health"
			}
			if action == "delete" {
				method = "DELETE"
			}
			return request(method, path, nil)
		case "save":
			data, err := readFile(*file)
			if err != nil {
				return err
			}
			var payload any
			if err = json.Unmarshal(data, &payload); err != nil {
				return err
			}
			method := "POST"
			path := "/api/datasources"
			if *id != "" {
				method = "PUT"
				path += "/uid/" + url.PathEscape(*id)
			}
			return request(method, path, payload)
		default:
			return errors.New("datasources requires list, get, save, query, health or delete")
		}
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
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		return io.ReadAll(io.LimitReader(file, 4*1024*1024+1))
	}
	return io.ReadAll(io.LimitReader(os.Stdin, 4*1024*1024+1))
}

type apiClient struct{ endpoint, token string }

func (c apiClient) uploadPlugin(data []byte) (json.RawMessage, error) {
	req, err := http.NewRequest("POST", c.endpoint+"/api/v1/plugins/install", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	req.Header.Set("Content-Type", "application/zip")
	req.Header.Set("X-Archive-SHA256", hex.EncodeToString(sum[:]))
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	if !json.Valid(raw) {
		return nil, errors.New("server returned invalid JSON")
	}
	return raw, nil
}

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
	timeout := 20 * time.Second
	if path == "/api/v1/plugins/catalog" {
		timeout = 60 * time.Second
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
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
	pluginDir := f.String("plugins-dir", env("METRICSPANEL_PLUGINS_DIR", ""), "plugin packages directory; default beside the control database")
	allowUnsigned := f.String("allow-unsigned-plugin", env("METRICSPANEL_ALLOW_UNSIGNED_PLUGINS", ""), "comma-separated development plugin IDs explicitly allowed without signatures")
	rootURL := f.String("root-url", env("METRICSPANEL_ROOT_URL", "http://127.0.0.1:7333"), "public application URL; used for private plugin signatures")
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
	allowed := []string{}
	for _, id := range strings.Split(*allowUnsigned, ",") {
		if id = strings.TrimSpace(id); id != "" {
			if !model.PluginID.MatchString(id) {
				listener.Close()
				return errors.New("invalid unsigned plugin ID")
			}
			allowed = append(allowed, id)
		}
	}
	srv.Plugins = plugins.New(storage, *pluginDir, allowed)
	srv.Plugins.RootURL = *rootURL
	backgroundDone := make(chan struct{})
	go func() { srv.RunBackground(ctx); close(backgroundDone) }()
	httpServer := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 75 * time.Second, IdleTimeout: 60 * time.Second}
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
	result["commands"] = append(result["commands"].([]string), "plugins list", "plugins get --id ID", "plugins install --file PACKAGE.zip", "plugins catalog --id ID --plugin-version EXACT", "plugins enable --id ID", "plugins disable --id ID", "plugins delete --id PACKAGE_ID", "datasources list", "datasources get --id UID", "datasources save --file FILE|- [--id UID] (update requires version)", "datasources health --id UID", "datasources delete --id UID")
	routes["GET"] = append(routes["GET"], "/api/v1/plugins", "/api/v1/plugins/{id}", "/api/plugins", "/api/plugins/{id}/settings", "/api/datasources", "/api/datasources/uid/{uid}", "/api/datasources/uid/{uid}/health", "/public/plugins/{id}/{asset}")
	routes["POST"] = append(routes["POST"], "/api/v1/plugins/install", "/api/v1/plugins/catalog", "/api/v1/plugins/assets-session", "/api/datasources", "/api/ds/query")
	routes["PUT"] = append(routes["PUT"], "/api/v1/plugins/{id}", "/api/datasources/uid/{uid}")
	routes["DELETE"] = append(routes["DELETE"], "/api/v1/plugins/{id}", "/api/datasources/uid/{uid}")
	result["plugins"] = map[string]any{
		"frontend_runtime":     "Grafana 13.2.3 public data/runtime/ui SDK; AMD and SystemJS",
		"backend_protocol":     "Grafana plugin SDK gRPC protocol 2: QueryData, QueryChunkedData, CheckHealth, CallResource, SubscribeStream, RunStream, PublishStream",
		"installation":         "original ZIP with verified Grafana PGP signature and every file SHA-256; exact catalog version; idempotent identical archive; upgrades preserve enabled preferences",
		"secrets":              "AES-256-GCM encrypted datasource and application secrets; back up secrets.key beside the control DB",
		"unsigned":             "only package IDs explicitly allowed by --allow-unsigned-plugin for development",
		"limits":               map[string]int{"archive_MiB": 64, "expanded_MiB": 256, "queries": 32, "response_MiB": 32},
		"pending_capabilities": []string{"UI extension points and some app core services", "Angular legacy plugins", "full Grafana core services"},
	}
	result["commands"] = append(result["commands"].([]string), "live channels", "live watch --channel ds/UID/path [--metadata JSON --limit 10 --duration 1m] (NDJSON)", "live publish --channel ds/UID/path --file FILE|-")
	routes["GET"] = append(routes["GET"], "/api/live/channels", "/api/live/ws (Centrifuge WebSocket)")
	routes["POST"] = append(routes["POST"], "/api/live/session", "/api/live/publish")
	result["live"] = map[string]any{"protocol": "Centrifuge JSON WebSocket", "channels": []string{"ds/UID/path", "plugin/ID/path"}, "multiplexing": "one SDK RunStream per channel; cancellation after last subscriber", "watch_output": "NDJSON: type, channel, timestamp, data; initial frame counts toward limit; 0 means unlimited", "limits": map[string]int{"channels": 256, "channels_per_connection": 128, "packet_MiB": 1, "frontend_buffer_rows": 10000}, "durability": "live transport is transient; collection and SQLite/ClickHouse storage remain durable"}
	result["commands"] = append(result["commands"].([]string), "plugins settings --id APP_ID", "plugins configure --id APP_ID --file FILE|- (version detects concurrent changes)")
	routes["GET"] = append(routes["GET"], "/api/v1/plugins/{id}/app-settings", "/api/plugins/{id}/resources/{path}", "/api/plugins/{id}/health")
	routes["POST"] = append(routes["POST"], "/api/plugins/{id}/settings")
	routes["PUT"] = append(routes["PUT"], "/api/v1/plugins/{id}/app-settings")
	result["apps"] = map[string]any{"frontend": "AppPlugin root and React configuration pages at /a/PLUGIN_ID/; pinned navigation", "configuration": "durable JSON and AES-256-GCM secrets; native writes require current version (initially 0), conflict HTTP 409", "backend": "official AppInstanceSettings on resources, health and Live; bundled datasource contexts inherit app settings", "organization": 1}
	result["commands"] = append(result["commands"].([]string), "datasources query --id UID --file FILE|- [--stream --duration 1m] (stream emits NDJSON until EOF; errors preserve partial data and exit 1)")
	routes["POST"] = append(routes["POST"], "/apis/{pluginId}.datasource.grafana.app/v0alpha1/namespaces/default/connections/{uid}/query")
	result["chunked_queries"] = map[string]any{"accept": "text/jsonl", "record": []string{"refId", "frameId", "frame (DataFrame JSON; schema in first chunk, later data appends)", "error", "errorSource"}, "legacy_route": "/api/ds/query also accepts text/jsonl", "fallback": "unary QueryData only when streaming RPC is unimplemented before any chunk", "frontend": "public BackendSrv.chunked emits raw Uint8Array chunks and final undefined; plugin owns parsing/append", "limits": map[string]int{"chunk_MiB": 8, "request_MiB": 32, "frames": 1024, "queries": 32, "duration_seconds": 60, "concurrent_sources": 4}, "example": map[string]any{"from": "now-5m", "to": "now", "queries": []any{map[string]any{"refId": "A", "expr": "sum(up)", "instant": true}}}}
	return result
}
