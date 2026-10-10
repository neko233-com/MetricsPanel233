//go:build integration

package integration_test

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/neko233-com/MetricsPanel233/internal/analysis"
	"github.com/neko233-com/MetricsPanel233/internal/live"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const project = "metricspanel233-tests"
const token = "metricspanel-integration-test-token"

type environment struct {
	root   string
	t      *testing.T
	client *http.Client
	proxy  string
}

func (e environment) docker(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	cmd.Dir = e.root
	cmd.Env = append(os.Environ(), "METRICSPANEL_GOPROXY="+e.proxy)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}
func (e environment) compose(args ...string) (string, error) {
	return e.docker(append([]string{"compose", "-p", project, "-f", "compose.test.yml"}, args...)...)
}

func (e environment) verifyBuiltinGrafana(address string) {
	var sources []model.DataSource
	require.NoError(e.t, json.Unmarshal(e.must(address, "GET", "/api/datasources", nil), &sources))
	core, defaults := 0, 0
	for _, source := range sources {
		if source.UID == "grafana" {
			core++
			assert.Equal(e.t, int64(-1), source.ID)
			assert.True(e.t, source.ReadOnly)
		}
		if source.IsDefault {
			defaults++
			assert.Equal(e.t, "metricspanel", source.UID)
		}
	}
	require.Equal(e.t, 1, core, "builtin discovery duplicated after restart")
	require.Equal(e.t, 1, defaults)
	var result backend.QueryDataResponse
	raw := e.must(address, "POST", "/api/ds/query", map[string]any{"queries": []any{map[string]any{"refId": "L", "queryType": "list", "path": "", "datasource": map[string]string{"uid": "grafana"}}}})
	require.NoError(e.t, json.Unmarshal(raw, &result))
	require.NoError(e.t, result.Responses["L"].Error)
	assert.Contains(e.t, string(raw), "index.html")
	raw = e.must(address, "POST", "/apis/grafana.datasource.grafana.app/v0alpha1/namespaces/default/connections/grafana/query", map[string]any{"from": "1000", "to": "4000", "queries": []any{map[string]any{"refId": "R", "queryType": "randomWalk", "intervalMs": 1000, "startValue": 233, "spread": 0}}})
	require.NoError(e.t, json.Unmarshal(raw, &result))
	require.NoError(e.t, result.Responses["R"].Error)
	require.Len(e.t, result.Responses["R"].Frames, 1)
	assert.Equal(e.t, 3, result.Responses["R"].Frames[0].Rows())
	assert.Equal(e.t, float64(233), result.Responses["R"].Frames[0].Fields[1].At(2))
}
func (e environment) verifyExpressionGraph(address string) {
	var result backend.QueryDataResponse
	payload := map[string]any{"from": strconv.FormatInt(time.Now().Add(-time.Minute).UnixMilli(), 10), "to": strconv.FormatInt(time.Now().UnixMilli(), 10), "queries": []any{
		map[string]any{"refId": "C", "type": "math", "expression": "$A * $D", "datasource": map[string]string{"uid": "__expr__"}},
		map[string]any{"refId": "A", "hide": true, "expr": "mysql_up", "instant": true, "datasource": map[string]string{"uid": "metricspanel"}},
		map[string]any{"refId": "B", "hide": true, "value": 2, "datasource": map[string]string{"uid": "docker-sdk"}},
		map[string]any{"refId": "D", "hide": true, "type": "reduce", "expression": "B", "reducer": "mean", "datasource": map[string]string{"uid": "__expr__"}},
	}}
	require.NoError(e.t, json.Unmarshal(e.must(address, "POST", "/api/ds/query", payload), &result))
	require.NoError(e.t, result.Responses["C"].Error)
	require.NotEmpty(e.t, result.Responses["C"].Frames)
	assert.Equal(e.t, float64(2), *result.Responses["C"].Frames[0].Fields[0].At(0).(*float64))
}
func (e environment) composeInput(input string, args ...string) (string, error) {
	command := exec.Command("docker", append([]string{"compose", "-p", project, "-f", "compose.test.yml"}, args...)...)
	command.Dir = e.root
	command.Stdin = strings.NewReader(input)
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}
func (e environment) address(service string) string {
	e.t.Helper()
	out, err := e.compose("port", service, "7333")
	require.NoError(e.t, err)
	return "http://" + strings.Split(out, "\n")[0]
}
func (e environment) request(address, method, path string, payload any) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, address+path, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}
func (e environment) must(address, method, path string, payload any) []byte {
	e.t.Helper()
	status, data, err := e.request(address, method, path, payload)
	require.NoError(e.t, err)
	require.Equal(e.t, 200, status, string(data))
	return data
}

func (e environment) installPlugin(address string, raw []byte) []byte {
	e.t.Helper()
	request, err := http.NewRequest("POST", address+"/api/v1/plugins/install", bytes.NewReader(raw))
	require.NoError(e.t, err)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/zip")
	response, err := e.client.Do(request)
	require.NoError(e.t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(e.t, err)
	require.Equal(e.t, 200, response.StatusCode, string(body))
	return body
}

func (e environment) sdkArchive(t *testing.T) []byte {
	t.Helper()
	architecture, err := e.docker("info", "--format", "{{.Architecture}}")
	require.NoError(t, err)
	arch := map[string]string{"x86_64": "amd64", "aarch64": "arm64", "amd64": "amd64", "arm64": "arm64"}[architecture]
	require.NotEmpty(t, arch, "unsupported test Docker architecture")
	name := "fixture_linux_" + arch
	binary := filepath.Join(t.TempDir(), name)
	command := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", binary, "./internal/plugins/testdata/sdk-backend")
	command.Dir = e.root
	command.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	executable, err := os.ReadFile(binary)
	require.NoError(t, err)
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	files := map[string][]byte{"plugin.json": []byte(`{"id":"metricspanel-sdk-datasource","name":"SDK Fixture","type":"datasource","backend":true,"executable":"fixture","info":{"version":"1.0.0"},"dependencies":{"grafanaVersion":">=12"}}`), "module.js": []byte(`System.register([],function(){return {execute:function(){}}})`), name: executable}
	files["child/plugin.json"] = files["plugin.json"]
	files["child/module.js"] = files["module.js"]
	files["child/"+name] = executable
	files["plugin.json"] = []byte(`{"id":"metricspanel-sdk-app","name":"SDK App Fixture","type":"app","backend":true,"executable":"fixture","info":{"version":"1.0.0"},"dependencies":{"grafanaDependency":">=12"}}`)
	for name, body := range files {
		f, err := z.Create(name)
		require.NoError(t, err)
		_, err = f.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, z.Close())
	return archive.Bytes()
}
func (e environment) verifySDKPlugin(address, service string) {
	e.t.Helper()
	assert.Contains(e.t, string(e.must(address, "GET", "/api/plugins/metricspanel-sdk-app/health", nil)), "Grafana SDK backend is working")
	app := e.must(address, "GET", "/api/plugins/metricspanel-sdk-app/resources/example", nil)
	assert.Contains(e.t, string(app), `"configured":true`)
	assert.Contains(e.t, string(app), `"label":"Docker operations"`)
	assert.NotContains(e.t, string(app), "app-secret-233")
	settings := e.must(address, "GET", "/api/v1/plugins/metricspanel-sdk-app/app-settings", nil)
	assert.Contains(e.t, string(settings), `"pinned":true`)
	assert.NotContains(e.t, string(settings), "app-secret-233")
	cliSettings, err := e.compose("exec", "-T", service, "metricspanel", "plugins", "settings", "--id", "metricspanel-sdk-app")
	require.NoError(e.t, err, cliSettings)
	assert.Contains(e.t, cliSettings, "Docker operations")
	assert.NotContains(e.t, cliSettings, "app-secret-233")
	assert.Contains(e.t, string(e.must(address, "GET", "/api/datasources/uid/docker-sdk/health", nil)), "Grafana SDK backend is working")
	assert.Contains(e.t, string(e.must(address, "GET", "/api/datasources/uid/docker-sdk/resources/check?round=233", nil)), `"uid":"docker-sdk"`)
	now := time.Now().UnixMilli()
	raw := e.must(address, "POST", "/api/ds/query", map[string]any{"from": strconv.FormatInt(now-60000, 10), "to": strconv.FormatInt(now, 10), "queries": []any{map[string]any{"refId": "A", "value": 233, "datasource": map[string]string{"uid": "docker-sdk", "type": "metricspanel-sdk-datasource"}}}})
	var result backend.QueryDataResponse
	require.NoError(e.t, json.Unmarshal(raw, &result))
	require.Len(e.t, result.Responses["A"].Frames, 1)
	assert.Equal(e.t, float64(233), result.Responses["A"].Frames[0].Fields[1].At(0))
	chunkQuery := `{"from":"now-1m","to":"now","queries":[{"refId":"A","value":233,"chunks":3,"delayMs":100,"requireApp":true}]}`
	chunkOutput, err := e.composeInput(chunkQuery, "exec", "-T", service, "metricspanel", "datasources", "query", "--id", "docker-sdk", "--stream", "--file", "-")
	require.NoError(e.t, err, chunkOutput)
	chunkLines := strings.Split(chunkOutput, "\n")
	require.Len(e.t, chunkLines, 3)
	assert.Contains(e.t, chunkLines[0], `"schema"`)
	assert.Contains(e.t, chunkLines[0], "上海🌍")
	assert.NotContains(e.t, chunkLines[1], `"schema"`)
	assert.Contains(e.t, chunkLines[2], "235")
	failedOutput, err := e.composeInput(`{"from":"now-1m","to":"now","queries":[{"refId":"A","value":233,"chunks":1},{"refId":"B","fail":true}]}`, "exec", "-T", service, "metricspanel", "datasources", "query", "--id", "docker-sdk", "--stream", "--file", "-")
	require.Error(e.t, err)
	assert.Contains(e.t, failedOutput, `"frame"`)
	assert.Contains(e.t, failedOutput, "fixture chunked query failed")
	assert.Contains(e.t, failedOutput, "1 datasource queries failed")
	// Real Linux SDK cancellation must work after the first HTTP flush.
	chunkCtx, stopChunk := context.WithCancel(context.Background())
	defer stopChunk()
	chunkRequest, err := http.NewRequestWithContext(chunkCtx, "POST", address+"/apis/metricspanel-sdk-datasource.datasource.grafana.app/v0alpha1/namespaces/default/connections/docker-sdk/query", strings.NewReader(`{"from":"now-1m","to":"now","queries":[{"refId":"A","value":233,"chunks":1000,"delayMs":100}]}`))
	require.NoError(e.t, err)
	chunkRequest.Header.Set("Authorization", "Bearer "+token)
	chunkRequest.Header.Set("Content-Type", "application/json")
	chunkRequest.Header.Set("Accept", "text/jsonl")
	chunkResponse, err := e.client.Do(chunkRequest)
	require.NoError(e.t, err)
	defer chunkResponse.Body.Close()
	require.Equal(e.t, 200, chunkResponse.StatusCode)
	_, err = bufio.NewReader(chunkResponse.Body).ReadBytes('\n')
	require.NoError(e.t, err)
	var chunkCounters map[string]int
	raw = e.must(address, "GET", "/api/datasources/uid/docker-sdk/resources/chunked-stats", nil)
	require.NoError(e.t, json.Unmarshal(raw, &chunkCounters))
	assert.Equal(e.t, 1, chunkCounters["active"], "chunked HTTP buffered until backend completion")
	beforeChunkCancellation := chunkCounters["cancelled"]
	stopChunk()
	chunkResponse.Body.Close()
	require.Eventually(e.t, func() bool {
		status, raw, err := e.request(address, "GET", "/api/datasources/uid/docker-sdk/resources/chunked-stats", nil)
		return err == nil && status == 200 && json.Unmarshal(raw, &chunkCounters) == nil && chunkCounters["active"] == 0 && chunkCounters["cancelled"] > beforeChunkCancellation
	}, 5*time.Second, 20*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	events := []live.Event{}
	require.NoError(e.t, live.Watch(ctx, live.WatchOptions{Endpoint: address, Token: token, Channel: "ds/docker-sdk/counter", Limit: 3}, func(event live.Event) error { events = append(events, event); return nil }))
	require.Len(e.t, events, 3)
	assert.Equal(e.t, "initial", events[0].Type)
	assert.Equal(e.t, "publication", events[2].Type)
	output, err := e.compose("exec", "-T", service, "metricspanel", "live", "watch", "--channel", "ds/docker-sdk/counter", "--limit", "3", "--duration", "10s")
	require.NoError(e.t, err, output)
	lines := strings.Split(output, "\n")
	require.Len(e.t, lines, 3)
	for _, line := range lines {
		var event live.Event
		require.NoError(e.t, json.Unmarshal([]byte(line), &event))
		assert.Equal(e.t, "ds/docker-sdk/counter", event.Channel)
		assert.True(e.t, json.Valid(event.Data))
	}
	require.Eventually(e.t, func() bool {
		var state live.Snapshot
		status, raw, err := e.request(address, "GET", "/api/live/channels", nil)
		return err == nil && status == 200 && json.Unmarshal(raw, &state) == nil && len(state.Channels) == 0 && state.Connections == 0
	}, 5*time.Second, 20*time.Millisecond, "Live watch limit left subscriptions or connections")
	raw = e.must(address, "GET", "/api/datasources/uid/docker-sdk/resources/stream-stats", nil)
	var counters map[string]int
	require.NoError(e.t, json.Unmarshal(raw, &counters))
	assert.Zero(e.t, counters["active"])
	assert.Equal(e.t, counters["started"], counters["cancelled"])
}
func (e environment) value(address, metric string) (float64, bool) {
	status, data, err := e.request(address, "GET", "/api/v1/query?"+url.Values{"metric": {metric}, "range": {"15m"}, "aggregation": {"last"}}.Encode(), nil)
	if err != nil || status != 200 {
		return 0, false
	}
	var result model.QueryResult
	if json.Unmarshal(data, &result) != nil || len(result.Series) == 0 {
		return 0, false
	}
	points := result.Series[0].Points
	if len(points) == 0 {
		return 0, false
	}
	return points[len(points)-1].Value, true
}

func (e environment) loadAndAnalyze(address string, batches int) {
	e.t.Helper()
	type result struct {
		elapsed time.Duration
		err     error
	}
	jobs := make(chan int, batches)
	results := make(chan result, batches)
	for i := 0; i < batches; i++ {
		jobs <- i
	}
	close(jobs)
	now := time.Now().UnixMilli()
	started := time.Now()
	for worker := 0; worker < 4; worker++ {
		go func() {
			for index := range jobs {
				samples := make([]model.Sample, 10000)
				for i := range samples {
					samples[i] = model.Sample{Name: "concurrent_analysis", Labels: map[string]string{"shard": strconv.Itoa(i % 20)}, Timestamp: now - int64(index*500+i/20)*1000, Value: float64(index*10000 + i)}
				}
				sent := time.Now()
				status, data, err := e.request(address, "POST", "/api/v1/ingest", map[string]any{"samples": samples})
				if err == nil && status != 200 {
					err = fmt.Errorf("ingest HTTP %d: %s", status, data)
				}
				results <- result{time.Since(sent), err}
			}
		}()
	}
	latencies := []time.Duration{}
	failures := []error{}
	for i := 0; i < batches; i++ {
		r := <-results
		latencies = append(latencies, r.elapsed)
		if r.err != nil {
			failures = append(failures, r.err)
		}
	}
	require.Empty(e.t, failures)
	elapsed := time.Since(started)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	e.t.Logf("concurrent durable ingestion: %d samples, 4 writers, %s, %.0f samples/s, batch p50=%s p95=%s", batches*10000, elapsed, float64(batches*10000)/elapsed.Seconds(), latencies[len(latencies)/2], latencies[(len(latencies)-1)*95/100])
	queryStarted := time.Now()
	data := e.must(address, "GET", "/api/v1/query?metric=concurrent_analysis&aggregation=sum&range=24h", nil)
	var query model.QueryResult
	require.NoError(e.t, json.Unmarshal(data, &query))
	require.NotEmpty(e.t, query.Series)
	require.NotEmpty(e.t, query.Series[0].Points)
	e.t.Logf("24h vectorized aggregation over %d acknowledged samples: %s", batches*10000, time.Since(queryStarted))
}

func (e environment) capturePatternFixture(address string) string {
	e.t.Helper()
	start := time.Now().Add(-10 * time.Minute).UnixMilli()
	end := start + 320000
	samples := []model.Sample{}
	for i := 0; i < 64; i++ {
		for _, example := range []struct {
			job           string
			scale, offset float64
		}{{"go", 1, 0}, {"mysql", 20, 233}} {
			samples = append(samples, model.Sample{Name: "pattern_fixture", Labels: map[string]string{"job": example.job}, Timestamp: start + int64(i)*5000, Value: float64(i)*example.scale + example.offset})
		}
	}
	e.must(address, "POST", "/api/v1/ingest", map[string]any{"samples": samples})
	request := map[string]any{"metric": "pattern_fixture", "start": start, "end": end, "normalization": "shape"}
	var result struct {
		Patterns []model.Pattern `json:"patterns"`
	}
	data := e.must(address, "POST", "/api/v1/patterns/capture", request)
	require.NoError(e.t, json.Unmarshal(data, &result))
	require.Len(e.t, result.Patterns, 2)
	for i := 0; i < 3; i++ {
		e.must(address, "POST", "/api/v1/patterns/capture", request)
	}
	data = e.must(address, "GET", "/api/v1/patterns", nil)
	require.NoError(e.t, json.Unmarshal(data, &result))
	require.Len(e.t, result.Patterns, 2, "repeated capture created duplicate vectors")
	id := result.Patterns[0].ID
	e.verifyPatternNeighbor(address, id)
	return id
}
func (e environment) verifyPatternNeighbor(address, id string) {
	e.t.Helper()
	data := e.must(address, "GET", "/api/v1/patterns/"+id, nil)
	var pattern model.Pattern
	require.NoError(e.t, json.Unmarshal(data, &pattern))
	require.Len(e.t, pattern.Values, 64)
	data = e.must(address, "POST", "/api/v1/patterns/search", map[string]any{"id": id, "metric": "pattern_fixture", "limit": 1})
	var result struct {
		Hits []model.PatternHit `json:"hits"`
	}
	require.NoError(e.t, json.Unmarshal(data, &result))
	require.Len(e.t, result.Hits, 1)
	assert.InDelta(e.t, 0, result.Hits[0].Distance, 1e-4)
}
func (e environment) verifyHNSW() string {
	e.t.Helper()
	out, err := e.compose("port", "clickhouse", "8123")
	require.NoError(e.t, err)
	ctx := context.Background()
	backend, err := store.OpenClickHouse(ctx, "http://"+strings.Split(out, "\n")[0], "metricspanel", "metricspanel", "integration-only-password", 30)
	require.NoError(e.t, err)
	s, err := store.Open(filepath.Join(e.t.TempDir(), "vector-control.db"))
	require.NoError(e.t, err)
	defer s.DB.Close()
	s.Backend = backend
	random := rand.New(rand.NewSource(233))
	start := time.Now().Add(-10 * time.Minute).UnixMilli()
	patterns := make([]model.Pattern, 10000)
	for i := range patterns {
		series := model.Series{Labels: map[string]string{"case": strconv.Itoa(i)}}
		for j := 0; j < 64; j++ {
			series.Points = append(series.Points, model.Point{Timestamp: start + int64(j)*5000, Value: random.Float64() * 100})
		}
		p, err := analysis.Embed("vector_benchmark", series, start, start+320000, "last", "raw")
		require.NoError(e.t, err)
		patterns[i] = p
	}
	started := time.Now()
	for i := 0; i < len(patterns); i += 200 {
		require.NoError(e.t, s.SavePatterns(ctx, patterns[i:i+200]))
	}
	e.t.Logf("durable vector library: 10000 x 64 dimensions in %s", time.Since(started))
	out, err = e.compose("exec", "-T", "clickhouse", "clickhouse-client", "--user", "metricspanel", "--password", "integration-only-password", "--query", "OPTIMIZE TABLE metricspanel.patterns FINAL")
	require.NoError(e.t, err, out)
	query := model.PatternSearch{Reference: patterns[233], Metric: "vector_benchmark", Limit: 10, IncludeSelf: true}
	plan, err := backend.PatternIndexPlan(ctx, query)
	require.NoError(e.t, err)
	assert.Contains(e.t, plan, "patterns_hnsw", "query planner did not use the HNSW index")
	exact := query
	exact.Exact = true
	truth, err := s.SearchPatterns(ctx, exact)
	require.NoError(e.t, err)
	require.Len(e.t, truth, 10)
	started = time.Now()
	hits, err := s.SearchPatterns(ctx, query)
	require.NoError(e.t, err)
	require.Len(e.t, hits, 10)
	assert.Equal(e.t, patterns[233].ID, hits[0].Pattern.ID)
	assert.InDelta(e.t, 0, hits[0].Distance, 1e-4)
	expected := map[string]bool{}
	for _, hit := range truth {
		expected[hit.Pattern.ID] = true
	}
	recall := 0
	for _, hit := range hits {
		if expected[hit.Pattern.ID] {
			recall++
		}
	}
	assert.GreaterOrEqual(e.t, recall, 8)
	e.t.Logf("HNSW selected by EXPLAIN: search over 10000 vectors in %s, recall@10=%d/10", time.Since(started), recall)
	deletable := patterns[9999]
	deletable.Metric = "vector_delete_fixture"
	deletable.Labels = map[string]string{"test": "delete"}
	deletable.SetID()
	require.NoError(e.t, s.SavePatterns(ctx, []model.Pattern{deletable}))
	require.NoError(e.t, s.DeletePattern(ctx, deletable.ID))
	_, err = s.Pattern(ctx, deletable.ID)
	require.Error(e.t, err, "ClickHouse vector deletion did not persist")
	return query.Reference.ID
}

func (e environment) verifyHNSWAfterRestart(referenceID string) {
	e.t.Helper()
	out, err := e.compose("port", "clickhouse", "8123")
	require.NoError(e.t, err)
	ctx := context.Background()
	backend, err := store.OpenClickHouse(ctx, "http://"+strings.Split(out, "\n")[0], "metricspanel", "metricspanel", "integration-only-password", 30)
	require.NoError(e.t, err)
	pattern, err := backend.Pattern(ctx, referenceID)
	require.NoError(e.t, err)
	query := model.PatternSearch{Reference: pattern, Metric: "vector_benchmark", Limit: 10, IncludeSelf: true}
	plan, err := backend.PatternIndexPlan(ctx, query)
	require.NoError(e.t, err)
	assert.Contains(e.t, plan, "patterns_hnsw", "restart lost the HNSW index")
	hits, err := backend.SearchPatterns(ctx, query)
	require.NoError(e.t, err)
	require.Len(e.t, hits, 10)
	assert.Equal(e.t, referenceID, hits[0].Pattern.ID)
}

func TestDockerEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("Docker integration requires full mode")
	}
	_, filename, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "../.."))
	proxy, err := exec.Command("go", "env", "GOPROXY").Output()
	require.NoError(t, err)
	e := environment{root: root, t: t, client: &http.Client{Timeout: 30 * time.Second}, proxy: strings.TrimSpace(string(proxy))}
	lock, err := net.Listen("tcp", "127.0.0.1:37332")
	require.NoError(t, err, "Docker integration slot is occupied; do not run this fixed project concurrently")
	t.Cleanup(func() {
		_, err := e.compose("down", "--volumes", "--remove-orphans", "--timeout", "10")
		if err != nil {
			t.Errorf("cleanup: %v", err)
		} else {
			t.Log("cleaned test containers, network and volumes")
		}
		_, _ = e.docker("image", "rm", "metricspanel233-test:local", "metricspanel233-go-test:local")
		for _, kind := range []string{"container", "network", "volume"} {
			out, err := e.docker(kind, "ls", "--quiet", "--filter", "label=com.docker.compose.project="+project)
			assert.NoError(t, err)
			assert.Empty(t, out, "test left %s resources", kind)
		}
		assert.NoError(t, lock.Close())
	})
	_, err = e.compose("down", "--volumes", "--remove-orphans")
	require.NoError(t, err)
	sdkArchive := e.sdkArchive(t)
	clockArchive, err := os.ReadFile(filepath.Join(root, "internal/plugins/testdata/grafana-clock-panel-3.2.4.zip"))
	require.NoError(t, err)
	var out string
	for attempt := 1; attempt <= 3; attempt++ {
		out, err = e.compose("build", "sqlite", "go-sqlite")
		if err == nil {
			break
		}
		if !strings.Contains(out, "EOF") && !strings.Contains(out, "TLS") && !strings.Contains(out, "timeout") {
			break
		}
		t.Logf("transient registry network error, build attempt %d/3", attempt)
	}
	require.NoError(t, err, out)
	out, err = e.compose("up", "--detach", "--no-build", "--wait", "--wait-timeout", "180")
	if err != nil {
		logs, _ := e.compose("logs", "--tail", "60")
		t.Log(logs)
	}
	require.NoError(t, err, out)
	for _, backend := range []struct{ service, goService string }{{"sqlite", "go-sqlite"}, {"clickhouse-app", "go-clickhouse"}} {
		t.Run(backend.service, func(t *testing.T) {
			e := e
			e.t = t
			address := e.address(backend.service)
			e.must(address, "GET", "/api/v1/health", nil)
			e.verifyBuiltinGrafana(address)
			first := e.installPlugin(address, clockArchive)
			assert.JSONEq(t, string(first), string(e.installPlugin(address, clockArchive)))
			first = e.installPlugin(address, sdkArchive)
			assert.JSONEq(t, string(first), string(e.installPlugin(address, sdkArchive)))
			e.must(address, "POST", "/api/plugins/metricspanel-sdk-app/settings", map[string]any{"enabled": true, "pinned": true, "jsonData": map[string]string{"label": "Docker operations"}, "secureJsonData": map[string]string{"apiKey": "app-secret-233"}})
			e.must(address, "POST", "/api/datasources", map[string]any{"uid": "docker-sdk", "name": "Docker SDK datasource", "type": "metricspanel-sdk-datasource", "secureJsonData": map[string]string{"apiKey": "test-secret-233"}})
			e.verifySDKPlugin(address, backend.service)
			for _, target := range []model.Target{{Name: "mysql", URL: "http://mysql-exporter:9104/metrics", IntervalSeconds: 5, Enabled: true}, {Name: "go", URL: "http://" + backend.goService + ":8080/metrics", IntervalSeconds: 5, Enabled: true}} {
				e.must(address, "POST", "/api/v1/targets", target)
			}
			require.Eventually(t, func() bool { v, ok := e.value(address, "mysql_up"); return ok && v == 1 }, 30*time.Second, 500*time.Millisecond, "real MySQL exporter did not report mysql_up=1")
			require.Eventually(t, func() bool { v, ok := e.value(address, "business_http_requests_total"); return ok && v > 0 }, 30*time.Second, 500*time.Millisecond, "Go exporter not scraped")
			require.Eventually(t, func() bool { v, ok := e.value(address, "business_push_total"); return ok && v > 0 }, 30*time.Second, 500*time.Millisecond, "Go JSON push not ingested")
			e.verifyExpressionGraph(address)
			// Duplicate writes replace a sample, including across a full restart.
			ts := time.Now().UnixMilli()
			for _, v := range []float64{233, 234} {
				e.must(address, "POST", "/api/v1/ingest", map[string]any{"samples": []model.Sample{{Name: "restart_marker", Timestamp: ts, Labels: map[string]string{"test": "persistence"}, Value: v}}})
			}
			batch := make([]model.Sample, 10000)
			for i := range batch {
				batch[i] = model.Sample{Name: "bulk_analysis", Labels: map[string]string{"shard": strconv.Itoa(i % 20)}, Timestamp: ts - int64(i/20)*1000, Value: float64(i)}
			}
			started := time.Now()
			e.must(address, "POST", "/api/v1/ingest", map[string]any{"samples": batch})
			elapsed := time.Since(started)
			t.Logf("acknowledged durable batch: 10000 samples in %s (%.0f samples/s)", elapsed, 10000/elapsed.Seconds())
			queryStarted := time.Now()
			e.must(address, "GET", "/api/v1/query?metric=bulk_analysis&aggregation=sum&range=15m", nil)
			t.Logf("vectorized sum query: %s", time.Since(queryStarted))
			batches := 10
			if backend.service == "clickhouse-app" {
				batches = 100
			}
			e.loadAndAnalyze(address, batches)
			patternID := e.capturePatternFixture(address)
			alert := model.AlertRule{UID: "docker-alert", Title: "MySQL is online", Expr: "mysql_up == bool 1", Condition: "nonzero", IntervalSeconds: 86400, ForSeconds: 30}
			data := e.must(address, "POST", "/api/v1/alerts/rules", alert)
			data = e.must(address, "POST", "/api/v1/alerts/rules/docker-alert/evaluate", nil)
			var before model.AlertRuleView
			require.NoError(t, json.Unmarshal(data, &before))
			require.NotEmpty(t, before.Runtime.Instances)
			assert.Equal(t, "Pending", before.Runtime.Instances[0].State)
			var beforeAlertAnnotations []model.Annotation
			data = e.must(address, "GET", "/api/annotations?type=alert&alertUID=docker-alert", nil)
			require.NoError(t, json.Unmarshal(data, &beforeAlertAnnotations))
			require.Len(t, beforeAlertAnnotations, 1)
			assert.Equal(t, "Pending", beforeAlertAnnotations[0].NewState)
			assert.Contains(t, beforeAlertAnnotations[0].Tags, "job:mysql")
			record := model.AlertRule{UID: "docker-record", Title: "MySQL availability recording", Expr: "mysql_up", Record: "mysql:availability", IntervalSeconds: 86400}
			e.must(address, "POST", "/api/v1/alerts/rules", record)
			e.must(address, "POST", "/api/v1/alerts/rules/docker-record/evaluate", nil)
			v, ok := e.value(address, "mysql:availability")
			require.True(t, ok)
			assert.Equal(t, 1.0, v)
			benchmarkID := ""
			if backend.service == "clickhouse-app" {
				benchmarkID = e.verifyHNSW()
			}
			params := url.Values{"query": {"sum(rate(business_http_requests_total[1m]))"}, "start": {strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)}, "end": {strconv.FormatInt(time.Now().Unix(), 10)}, "step": {"5"}}
			data = e.must(address, "GET", "/prometheus/api/v1/query_range?"+params.Encode(), nil)
			assert.Contains(t, string(data), `"resultType":"matrix"`)
			data = e.must(address, "GET", "/prometheus/api/v1/query?query=mysql_up", nil)
			assert.Contains(t, string(data), `"1"`)
			data = e.must(address, "GET", "/prometheus/api/v1/label/job/values", nil)
			assert.Contains(t, string(data), `"mysql"`)
			original := map[string]any{"title": "Docker MySQL", "panels": []any{map[string]any{"title": "Connections", "type": "timeseries", "targets": []any{map[string]string{"expr": "mysql_global_status_threads_connected"}}}}}
			data = e.must(address, "POST", "/api/v1/import/grafana", original)
			assert.Contains(t, string(data), "Docker MySQL")
			data = e.must(address, "POST", "/api/dashboards/db", map[string]any{"dashboard": map[string]any{"uid": "docker-mysql", "title": "MySQL API", "panels": []any{map[string]any{"id": 1, "title": "Health", "type": "table", "gridPos": map[string]int{"x": 0, "y": 0, "w": 24, "h": 8}, "targets": []any{map[string]any{"refId": "A", "expr": "mysql_up", "instant": true, "format": "table"}}, "transformations": []any{map[string]any{"id": "organize", "options": map[string]any{"excludeByName": map[string]bool{"Time": true}}}}}}}})
			assert.Contains(t, string(data), `"status":"success"`)
			annotation := map[string]any{"dashboardUID": "docker-mysql", "panelId": 1, "time": ts - 10000, "timeEnd": ts + 10000, "text": "MySQL deployment", "tags": []string{"docker", "mysql"}, "idempotencyKey": "mysql-deploy"}
			firstAnnotation := e.must(address, "POST", "/api/annotations", annotation)
			assert.JSONEq(t, string(firstAnnotation), string(e.must(address, "POST", "/api/annotations", annotation)))
			regionConfig := json.RawMessage(`[{"name":"Office hours","datasource":{"type":"grafana","uid":"-- Grafana --"},"target":{"queryType":"timeRegions","timeRegion":{"mode":"cron","cronExpr":"0 9 * * MON-FRI","duration":"8h","timezone":"Asia/Shanghai","unknown":233}}}]`)
			var nativeRegions model.Dashboard
			require.NoError(t, json.Unmarshal(e.must(address, "POST", "/api/v1/dashboards", model.Dashboard{Name: "Docker time regions", Panels: []model.Panel{}, Annotations: regionConfig}), &nativeRegions))
			_, err = e.compose("restart", backend.service)
			require.NoError(t, err)
			address = e.address(backend.service)
			require.Eventually(t, func() bool { v, ok := e.value(address, "restart_marker"); return ok && v == 234 }, 40*time.Second, 500*time.Millisecond, "restart lost or duplicated an acknowledged sample")
			e.verifyBuiltinGrafana(address)
			e.verifySDKPlugin(address, backend.service)
			e.verifyExpressionGraph(address)
			var regionDashboards []model.Dashboard
			require.NoError(t, json.Unmarshal(e.must(address, "GET", "/api/v1/dashboards", nil), &regionDashboards))
			regionFound := false
			for _, dashboard := range regionDashboards {
				if dashboard.ID == nativeRegions.ID {
					regionFound = true
					assert.JSONEq(t, string(regionConfig), string(dashboard.Annotations), "restart lost native recurrence configuration")
					assert.JSONEq(t, `null`, string(dashboard.Grafana), "native recurrence converted the dashboard format")
				}
			}
			require.True(t, regionFound)
			assert.JSONEq(t, `[]`, string(e.must(address, "GET", "/api/annotations?dashboardUID="+nativeRegions.ID, nil)), "recurrence configuration must not create stored annotations")
			assert.JSONEq(t, string(firstAnnotation), string(e.must(address, "POST", "/api/annotations", annotation)), "restart lost annotation retry identity")
			annotationData := e.must(address, "GET", "/api/annotations?dashboardUID=docker-mysql&tags=docker&tags=mysql", nil)
			var storedAnnotations []model.Annotation
			require.NoError(t, json.Unmarshal(annotationData, &storedAnnotations))
			require.Len(t, storedAnnotations, 1)
			assert.Equal(t, "MySQL deployment", storedAnnotations[0].Text)
			assert.Equal(t, ts+10000, storedAnnotations[0].TimeEnd)
			data = e.must(address, "GET", "/api/v1/plugins/grafana-clock-panel", nil)
			assert.Contains(t, string(data), `"signature":"grafana"`)
			data = e.must(address, "GET", "/public/plugins/grafana-clock-panel/module.js", nil)
			assert.Contains(t, string(data), "define(")
			e.must(address, "DELETE", "/api/datasources/uid/docker-sdk", nil)
			e.must(address, "DELETE", "/api/v1/plugins/metricspanel-sdk-app", nil)
			e.must(address, "DELETE", "/api/v1/plugins/grafana-clock-panel", nil)
			directories, err := e.compose("exec", "-T", backend.service, "find", "/data/plugins", "-mindepth", "1", "-maxdepth", "1")
			require.NoError(t, err)
			assert.Empty(t, directories, "uninstall left plugin directories")
			data = e.must(address, "GET", "/api/v1/alerts/rules/docker-alert", nil)
			var after model.AlertRuleView
			require.NoError(t, json.Unmarshal(data, &after))
			require.Len(t, after.Runtime.Instances, len(before.Runtime.Instances))
			assert.Equal(t, before.Runtime.Instances[0].ActiveAt, after.Runtime.Instances[0].ActiveAt, "restart reset pending timer")
			assert.Equal(t, before.Runtime.LastEvaluation, after.Runtime.LastEvaluation)
			assert.Contains(t, string(e.must(address, "GET", "/api/v1/alerts/history?uid=docker-alert", nil)), `"to":"Pending"`)
			var afterAlertAnnotations []model.Annotation
			data = e.must(address, "GET", "/api/annotations?type=alert&alertUID=docker-alert", nil)
			require.NoError(t, json.Unmarshal(data, &afterAlertAnnotations))
			assert.Equal(t, beforeAlertAnnotations, afterAlertAnnotations, "restart lost or duplicated automatic alert annotations")
			assert.JSONEq(t, "[]", string(e.must(address, "GET", "/api/annotations?alertUID=docker-record", nil)))
			v, ok = e.value(address, "mysql:availability")
			require.True(t, ok, "recording rule sample did not survive app restart")
			assert.Equal(t, 1.0, v)
			data = e.must(address, "GET", "/api/v1/targets", nil)
			assert.Contains(t, string(data), "mysql-exporter")
			data = e.must(address, "GET", "/api/v1/dashboards", nil)
			assert.Contains(t, string(data), "Docker MySQL")
			data = e.must(address, "GET", "/api/dashboards/uid/docker-mysql", nil)
			assert.Contains(t, string(data), `"organize"`)
			assert.Contains(t, string(data), `"gridPos"`)
			data = e.must(address, "GET", "/api/v1/stats", nil)
			var stats struct {
				Samples int64 `json:"samples"`
			}
			require.NoError(t, json.Unmarshal(data, &stats))
			assert.GreaterOrEqual(t, stats.Samples, int64(batches*10000))
			e.verifyPatternNeighbor(address, patternID)
			if backend.service == "clickhouse-app" {
				_, err = e.compose("restart", "clickhouse")
				require.NoError(t, err)
				require.Eventually(t, func() bool { v, ok := e.value(address, "restart_marker"); return ok && v == 234 }, 60*time.Second, 500*time.Millisecond, "ClickHouse restart lost acknowledged sample")
				e.verifyPatternNeighbor(address, patternID)
				e.verifyHNSWAfterRestart(benchmarkID)
			}
		})
	}
}
