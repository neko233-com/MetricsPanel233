package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setup(t *testing.T, token string) (*store.Store, http.Handler) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "data.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.DB.Close() })
	return s, server.New(s, token, 30).Handler()
}
func call(handler http.Handler, method, path, body, token, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}
func TestAuthOriginAndInputLimits(t *testing.T) {
	_, handler := setup(t, "test-token-233233")
	assert.Equal(t, 401, call(handler, "GET", "/api/v1/health", "", "", "").Code)
	assert.Equal(t, 200, call(handler, "GET", "/api/v1/health", "", "test-token-233233", "").Code)
	assert.Equal(t, 403, call(handler, "POST", "/api/v1/ingest", `{"samples":[{"name":"ok","value":1}]}`, "test-token-233233", "https://evil.example").Code)
	for _, body := range []string{`{}`, `{"samples":[{"name":"bad-name","value":1}]}`, `{"samples":[{"name":"ok","value":1}],"unknown":true}`, `{"samples":[{"name":"ok","value":1}]} {}`} {
		assert.Equal(t, 400, call(handler, "POST", "/api/v1/ingest", body, "test-token-233233", "").Code)
	}
}
func TestCRUDAndPushToQuery(t *testing.T) {
	_, h := setup(t, "")
	assert.Equal(t, 200, call(h, "POST", "/api/v1/ingest", `{"samples":[{"name":"business_orders","labels":{"service":"go"},"value":233}]}`, "", "").Code)
	w := call(h, "GET", "/api/v1/query?metric=business_orders&range=30m", "", "", "")
	require.Equal(t, 200, w.Code)
	var q model.QueryResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &q))
	require.Len(t, q.Series, 1)
	assert.Equal(t, 233.0, q.Series[0].Points[0].Value)
	w = call(h, "POST", "/api/v1/targets", `{"name":"mysql","url":"http://mysql-exporter:9104/metrics","interval_seconds":5,"enabled":true}`, "", "")
	require.Equal(t, 200, w.Code)
	var target model.Target
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &target))
	require.NotZero(t, target.ID)
	assert.Equal(t, 200, call(h, "DELETE", "/api/v1/targets/"+strconv.FormatInt(target.ID, 10), "", "", "").Code)
	assert.Equal(t, 404, call(h, "DELETE", "/api/v1/targets/999", "", "", "").Code)
	w = call(h, "POST", "/api/v1/dashboards", `{"name":"Business","panels":[{"id":"one","title":"Orders","metric":"business_orders","aggregation":"last","unit":"count"}]}`, "", "")
	require.Equal(t, 200, w.Code)
	var d model.Dashboard
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &d))
	assert.Equal(t, 200, call(h, "DELETE", "/api/v1/dashboards/"+d.ID, "", "", "").Code)
	assert.Equal(t, 400, call(h, "DELETE", "/api/v1/dashboards/system", "", "", "").Code)
	assert.Equal(t, 200, call(h, "GET", "/", "", "", "").Code)
	assert.Equal(t, 404, call(h, "GET", "/not-found.js", "", "", "").Code)
}
func TestOfficialPromQLEngineAndGrafanaProtocol(t *testing.T) {
	s, h := setup(t, "")
	now := time.Now().Truncate(time.Second)
	samples := []model.Sample{}
	for i := 0; i < 12; i++ {
		samples = append(samples, model.Sample{Name: "go_requests_total", Labels: map[string]string{"job": "go", "instance": "one"}, Timestamp: now.Add(time.Duration(i-11) * 5 * time.Second).UnixMilli(), Value: float64(i * 10)})
	}
	require.NoError(t, s.Ingest(context.Background(), samples))
	for _, query := range []string{`sum(rate(go_requests_total[1m]))`, `go_requests_total{job=~"g.*"}`, `sum_over_time(go_requests_total[1m])`, `histogram_quantile(0.9, rate(missing_bucket[5m]))`} {
		r := httptest.NewRequest("GET", "/prometheus/api/v1/query", nil)
		params := r.URL.Query()
		params.Set("query", query)
		params.Set("time", strconv.FormatInt(now.Unix(), 10))
		r.URL.RawQuery = params.Encode()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		require.Equal(t, 200, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), `"status":"success"`)
	}
	w := call(h, "GET", "/prometheus/api/v1/label/job/values", "", "", "")
	require.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), `"go"`)
	w = call(h, "GET", "/prometheus/api/v1/labels", "", "", "")
	assert.Contains(t, w.Body.String(), `"__name__"`)
	assert.Equal(t, 400, call(h, "GET", "/prometheus/api/v1/query?query=bad%28", "", "", "").Code)
	w = call(h, "POST", "/api/v1/import/grafana", `{"title":"Go backend","panels":[{"type":"timeseries","title":"RPS","targets":[{"expr":"sum(rate(go_requests_total[1m]))"}]}]}`, "", "")
	require.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), `"grafana"`)
}
