package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/grafana/grafana-plugin-sdk-go/genproto/pluginv2"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChunkWriterArrowSchemaAndColumnValidation(t *testing.T) {
	w := httptest.NewRecorder()
	writer := &queryChunkWriter{w: w}
	frame := data.NewFrame("arrow", data.NewField("Value", nil, []float64{233}))
	arrow, err := frame.MarshalArrow()
	require.NoError(t, err)
	require.NoError(t, writer.onChunk(&pluginv2.QueryChunkedDataResponse{RefId: "A", FrameId: "same", Frame: arrow}))
	assert.Equal(t, chunkedContentType, w.Header().Get("Content-Type"))
	assert.True(t, w.Flushed)
	var line queryChunk
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(w.Body.Bytes()), &line))
	assert.Contains(t, string(line.Frame), `"refId":"A"`)
	assert.Contains(t, string(line.Frame), `233`)
	require.NoError(t, writer.onChunk(&pluginv2.QueryChunkedDataResponse{RefId: "A", FrameId: "same", Format: pluginv2.DataFrameFormat_JSON, Frame: []byte(`{"data":{"values":[[234,235]]}}`)}))
	for _, invalid := range []struct {
		id    string
		frame string
	}{
		{"new", `{"data":{"values":[[1]]}}`},
		{"same", `{"data":{"values":[[1],[2]]}}`},
		{"same", `{"schema":{"refId":"B","fields":[]},"data":{"values":[]}}`},
		{"same", `{"schema":{"fields":[{"name":"Changed","type":"number"}]},"data":{"values":[[1]]}}`},
	} {
		require.Error(t, writer.onChunk(&pluginv2.QueryChunkedDataResponse{RefId: "A", FrameId: invalid.id, Format: pluginv2.DataFrameFormat_JSON, Frame: []byte(invalid.frame)}))
	}
	// Resource limits must produce an explicit error line instead of a clean, truncated EOF.
	writer.bytes = (32 << 20) - chunkErrorReserve
	err = writer.onChunk(&pluginv2.QueryChunkedDataResponse{RefId: "A", FrameId: "same", Format: pluginv2.DataFrameFormat_JSON, Frame: []byte(`{"data":{"values":[[236]]}}`)})
	require.ErrorContains(t, err, "32 MiB")
	require.NoError(t, writer.writeErrors([]backend.DataQuery{{RefID: "A"}}, err))
	assert.Contains(t, w.Body.String(), `"error":"chunked query exceeds`)
	queries := make([]backend.DataQuery, 32)
	for i := range queries {
		queries[i].RefID = strings.Repeat("x", 100)
	}
	worstEscaping := fmt.Errorf("%s", strings.Repeat("\x01", 1024))
	require.NoError(t, writer.writeErrors(queries, worstEscaping))
	require.NoError(t, writer.writeErrors(queries, worstEscaping))
	assert.Less(t, writer.bytes, 32<<20)
}

func TestRealSDKChunkedHTTPFlushAppendErrorsCancellationAndAuth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "fixture")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	out, err := exec.CommandContext(ctx, "go", "build", "-o", binary, "../plugins/testdata/sdk-backend").CombinedOutput()
	require.NoError(t, err, string(out))
	archive := filepath.Join(dir, "plugin.zip")
	out, err = exec.CommandContext(ctx, binary, "--package", archive).CombinedOutput()
	require.NoError(t, err, string(out))
	raw, err := os.ReadFile(archive)
	require.NoError(t, err)
	s, err := store.Open(filepath.Join(dir, "control.db"))
	require.NoError(t, err)
	const token = "chunked-http-test-233233"
	app := New(s, token, 30)
	app.Plugins.AllowUnsigned["metricspanel-sdk-datasource"] = true
	_, err = app.Plugins.Install(ctx, raw, "")
	require.NoError(t, err)
	ds, err := s.SaveDataSource(ctx, model.DataSourceInput{DataSource: model.DataSource{UID: "sdk-chunks", Name: "Chunked SDK test", Type: "metricspanel-sdk-datasource"}, SecureJSONData: map[string]string{"apiKey": "test-secret-233"}})
	require.NoError(t, err)
	_, err = app.Plugins.Health(ctx, ds)
	require.NoError(t, err)
	h := app.Handler()
	server := httptest.NewServer(h)
	t.Cleanup(func() { server.Close(); app.Plugins.Close(); app.Live.Close(); s.DB.Close() })
	path := "/apis/metricspanel-sdk-datasource.datasource.grafana.app/v0alpha1/namespaces/default/connections/sdk-chunks/query"
	queryBody := func(queries string) string {
		return fmt.Sprintf(`{"from":%q,"to":%q,"queries":%s}`, fmt.Sprint(time.Now().Add(-time.Minute).UnixMilli()), fmt.Sprint(time.Now().UnixMilli()), queries)
	}
	post := func(ctx context.Context, body, auth string) *http.Response {
		r, err := http.NewRequestWithContext(ctx, "POST", server.URL+path, bytes.NewBufferString(body))
		require.NoError(t, err)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "text/jsonl; charset=utf-8")
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		response, err := server.Client().Do(r)
		require.NoError(t, err)
		return response
	}
	stats := func() map[string]int64 {
		resource, err := app.Plugins.Resource(ctx, ds, &backend.CallResourceRequest{Path: "chunked-stats"})
		require.NoError(t, err)
		var result map[string]int64
		require.NoError(t, json.Unmarshal(resource[0].Body, &result))
		return result
	}
	body := queryBody(`[{"refId":"A","value":233,"chunks":3,"delayMs":300,"secondary":true},{"refId":"B","fail":true}]`)
	response := post(ctx, body, "")
	assert.Equal(t, 401, response.StatusCode)
	response.Body.Close()
	response = post(ctx, body, token)
	require.Equal(t, 200, response.StatusCode)
	assert.Equal(t, chunkedContentType, response.Header.Get("Content-Type"))
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadBytes('\n')
	require.NoError(t, err)
	var first queryChunk
	require.NoError(t, json.Unmarshal(line, &first))
	assert.Equal(t, "A", first.RefID)
	assert.Equal(t, "primary", first.FrameID)
	assert.Contains(t, string(first.Frame), `"schema"`)
	assert.Contains(t, string(first.Frame), "上海🌍")
	assert.Equal(t, int64(1), stats()["active"], "response arrived only after the backend finished")
	lines := []queryChunk{first}
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		var chunk queryChunk
		require.NoError(t, json.Unmarshal(line, &chunk))
		lines = append(lines, chunk)
	}
	response.Body.Close()
	require.Len(t, lines, 7)
	assert.Contains(t, string(lines[1].Frame), `"schema"`)
	assert.NotContains(t, string(lines[2].Frame), `"schema"`)
	assert.Contains(t, string(lines[2].Frame), `234`)
	assert.Equal(t, "B", lines[6].RefID)
	assert.Equal(t, "fixture chunked query failed", lines[6].Error)
	require.Eventually(t, func() bool { return stats()["active"] == 0 }, time.Second, 10*time.Millisecond)
	// Aborting the HTTP reader must propagate through gRPC to the SDK handler.
	before := stats()["cancelled"]
	streamCtx, stop := context.WithCancel(ctx)
	response = post(streamCtx, queryBody(`[{"refId":"A","chunks":1000,"delayMs":100}]`), token)
	reader = bufio.NewReader(response.Body)
	_, err = reader.ReadBytes('\n')
	require.NoError(t, err)
	stop()
	response.Body.Close()
	require.Eventually(t, func() bool { current := stats(); return current["active"] == 0 && current["cancelled"] > before }, 2*time.Second, 10*time.Millisecond)
	for _, invalid := range []string{
		`{"from":"bad","to":"now","queries":[{"refId":"A"}]}`,
		queryBody(`[{"refId":"A","datasource":{"uid":"another"}}]`),
		queryBody(`[{"refId":"A"},{"refId":"A"}]`),
		queryBody(`[{"refId":"A","maxDataPoints":10001}]`),
		queryBody(`[{"refId":"A","intervalMs":1e20}]`),
		queryBody(`[{"refId":"A","intervalMs":1.5}]`),
		queryBody(`[{"refId":"A","intervalMs":-1}]`),
	} {
		response = post(ctx, invalid, token)
		assert.Equal(t, 400, response.StatusCode)
		response.Body.Close()
	}
	r := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Origin", "https://foreign.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, 403, w.Code)
	// The modern API must also accept ordinary JSON queries and per-query ranges.
	r = httptest.NewRequest("POST", path, bytes.NewBufferString(`{"queries":[{"refId":"A","value":233,"timeRange":{"from":"now-1m","to":"now"}}]}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `233`)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	for _, badPath := range []string{
		"/apis/wrong.datasource.grafana.app/v0alpha1/namespaces/default/connections/sdk-chunks/query",
		"/apis/metricspanel-sdk-datasource.datasource.grafana.app/v0alpha1/namespaces/other/connections/sdk-chunks/query",
	} {
		r = httptest.NewRequest("POST", badPath, bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		assert.Equal(t, 404, w.Code)
	}
	// A slow datasource must not hold back another source's first frame.
	_, err = s.SaveDataSource(ctx, model.DataSourceInput{DataSource: model.DataSource{UID: "a-slow", Name: "Slow SDK", Type: ds.Type}, SecureJSONData: map[string]string{"apiKey": "test-secret-233"}})
	require.NoError(t, err)
	path = "/api/ds/query"
	response = post(ctx, queryBody(`[{"refId":"S","value":1,"chunks":1,"firstDelayMs":500,"datasource":{"uid":"a-slow"}},{"refId":"F","value":233,"chunks":1,"datasource":{"uid":"sdk-chunks"}}]`), token)
	require.Equal(t, 200, response.StatusCode)
	reader = bufio.NewReader(response.Body)
	line, err = reader.ReadBytes('\n')
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(line, &first))
	assert.Equal(t, "F", first.RefID)
	assert.Equal(t, int64(1), stats()["active"])
	remaining, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Contains(t, string(remaining), `"refId":"S"`)
	response.Body.Close()
}

func TestModernNativePrometheusChunkedAndJSONQueries(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	app := New(s, "", 30)
	t.Cleanup(func() { app.Plugins.Close(); app.Live.Close(); s.DB.Close() })
	now := time.Now().UnixMilli()
	require.NoError(t, s.Ingest(context.Background(), []model.Sample{{Name: "chunked_native", Value: 233, Timestamp: now}}))
	path := "/apis/prometheus.datasource.grafana.app/v0alpha1/namespaces/default/connections/metricspanel/query"
	for _, accept := range []string{"application/json", "application/json, text/jsonl"} {
		r := httptest.NewRequest("POST", path, bytes.NewBufferString(fmt.Sprintf(`{"from":%q,"to":%q,"queries":[{"refId":"A","expr":"chunked_native","instant":true},{"refId":"B","expr":"bad("}]}`, fmt.Sprint(now-60000), fmt.Sprint(now))))
		r.Header.Set("Accept", accept)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		require.Equal(t, 200, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), "233")
		if requestsChunks(accept) {
			assert.Equal(t, chunkedContentType, w.Header().Get("Content-Type"))
			assert.Equal(t, 2, bytes.Count(w.Body.Bytes(), []byte{'\n'}))
			assert.Contains(t, w.Body.String(), `"refId":"B","error":`)
		} else {
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
			assert.True(t, json.Valid(w.Body.Bytes()))
		}
	}
}
