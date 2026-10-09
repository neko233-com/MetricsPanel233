//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
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
			for _, target := range []model.Target{{Name: "mysql", URL: "http://mysql-exporter:9104/metrics", IntervalSeconds: 5, Enabled: true}, {Name: "go", URL: "http://" + backend.goService + ":8080/metrics", IntervalSeconds: 5, Enabled: true}} {
				e.must(address, "POST", "/api/v1/targets", target)
			}
			require.Eventually(t, func() bool { v, ok := e.value(address, "mysql_up"); return ok && v == 1 }, 30*time.Second, 500*time.Millisecond, "real MySQL exporter did not report mysql_up=1")
			require.Eventually(t, func() bool { v, ok := e.value(address, "business_http_requests_total"); return ok && v > 0 }, 30*time.Second, 500*time.Millisecond, "Go exporter not scraped")
			require.Eventually(t, func() bool { v, ok := e.value(address, "business_push_total"); return ok && v > 0 }, 30*time.Second, 500*time.Millisecond, "Go JSON push not ingested")
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
			params := url.Values{"query": {"sum(rate(business_http_requests_total[1m]))"}, "start": {strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)}, "end": {strconv.FormatInt(time.Now().Unix(), 10)}, "step": {"5"}}
			data := e.must(address, "GET", "/prometheus/api/v1/query_range?"+params.Encode(), nil)
			assert.Contains(t, string(data), `"resultType":"matrix"`)
			data = e.must(address, "GET", "/prometheus/api/v1/query?query=mysql_up", nil)
			assert.Contains(t, string(data), `"1"`)
			data = e.must(address, "GET", "/prometheus/api/v1/label/job/values", nil)
			assert.Contains(t, string(data), `"mysql"`)
			original := map[string]any{"title": "Docker MySQL", "panels": []any{map[string]any{"title": "Connections", "type": "timeseries", "targets": []any{map[string]string{"expr": "mysql_global_status_threads_connected"}}}}}
			data = e.must(address, "POST", "/api/v1/import/grafana", original)
			assert.Contains(t, string(data), "Docker MySQL")
			_, err = e.compose("restart", backend.service)
			require.NoError(t, err)
			address = e.address(backend.service)
			require.Eventually(t, func() bool { v, ok := e.value(address, "restart_marker"); return ok && v == 234 }, 40*time.Second, 500*time.Millisecond, "restart lost or duplicated an acknowledged sample")
			data = e.must(address, "GET", "/api/v1/targets", nil)
			assert.Contains(t, string(data), "mysql-exporter")
			data = e.must(address, "GET", "/api/v1/dashboards", nil)
			assert.Contains(t, string(data), "Docker MySQL")
			if backend.service == "clickhouse-app" {
				_, err = e.compose("restart", "clickhouse")
				require.NoError(t, err)
				require.Eventually(t, func() bool { v, ok := e.value(address, "restart_marker"); return ok && v == 234 }, 60*time.Second, 500*time.Millisecond, "ClickHouse restart lost acknowledged sample")
			}
		})
	}
}
