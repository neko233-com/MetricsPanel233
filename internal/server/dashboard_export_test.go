package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDashboardExportsPreserveRawSourceAndRequireAuthentication(t *testing.T) {
	const token = "export-token-233"
	_, handler := setup(t, token)
	const source = `{
  "uid":"export-precision","title":"Exact export",
  "opaque":{"counter":9007199254740993,"digits":"9007199254740993"},
  "panels":[{"id":1,"type":"text","title":"Text","options":{"content":"Precision"}}]
}`
	imported := call(handler, "POST", "/api/v1/import/grafana", source, token, "")
	require.Equal(t, http.StatusOK, imported.Code, imported.Body.String())
	var result struct {
		Dashboard struct{ ID string } `json:"dashboard"`
	}
	require.NoError(t, json.Unmarshal(imported.Body.Bytes(), &result))
	path := "/api/v1/dashboard-exports/" + result.Dashboard.ID
	var compact bytes.Buffer
	require.NoError(t, json.Compact(&compact, []byte(source)))
	require.Equal(t, http.StatusUnauthorized, call(handler, "GET", path+"?format=grafana", "", "", "").Code)
	for _, format := range []string{"", "native", "grafana"} {
		response := call(handler, "GET", path+"?format="+format, "", token, "")
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Header().Get("Content-Type"), "application/json")
		require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		require.Contains(t, response.Body.String(), `"counter":9007199254740993`)
		require.Contains(t, response.Body.String(), `"digits":"9007199254740993"`)
		if format == "grafana" {
			require.Equal(t, compact.String(), response.Body.String())
		}
	}
	for _, suffix := range []string{"?format=xml", "?format=native&format=grafana", "?other=value", "?format=%zz"} {
		require.Equal(t, http.StatusBadRequest, call(handler, "GET", path+suffix, "", token, "").Code)
	}
	require.Equal(t, http.StatusNotFound, call(handler, "GET", "/api/v1/dashboard-exports/missing", "", token, "").Code)
	nativeExport := call(handler, "GET", "/api/v1/dashboard-exports/system?format=grafana", "", token, "")
	require.Equal(t, http.StatusOK, nativeExport.Code)
	require.Contains(t, nativeExport.Body.String(), "metricspanel_memory_bytes")
	require.Equal(t, http.StatusOK, call(handler, "GET", "/api/v1/dashboard-exports/system?format=native", "", token, "").Code)
}
