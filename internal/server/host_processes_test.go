package server_test

import (
	"bytes"
	"encoding/json"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostProcessAPINativeListDetailAuthAndOpaqueIcon(t *testing.T) {
	const token = "host-process-contract"
	_, handler := setup(t, token)
	assert.Equal(t, 401, call(handler, "GET", "/api/v1/host/processes", "", "", "").Code)
	assert.Equal(t, 401, call(handler, "GET", "/api/v1/host/process-icons/generic", "", "", "").Code)
	assert.Equal(t, 403, call(handler, "GET", "/api/v1/host/processes", "", token, "https://other.invalid").Code)
	w := call(handler, "GET", "/api/v1/host/processes?limit=1", "", token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var first model.HostProcessList
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &first))
	assert.Equal(t, "name_asc_pid_asc", first.Order)
	require.Len(t, first.Items, 1)
	require.NotEmpty(t, first.Source.Hostname)
	require.NotEmpty(t, first.SnapshotID)
	assert.Equal(t, int64(60000), first.ExpiresAt-first.SampledAt)
	assert.NotContains(t, w.Body.String(), "commandLine")
	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &decoded))
	var rawItems []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(decoded["items"], &rawItems))
	for _, item := range rawItems {
		assert.NotContains(t, item, "executable")
		assert.NotContains(t, item, "commandLine")
		assert.NotContains(t, item, "path")
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	encodedExecutable, err := json.Marshal(executable)
	require.NoError(t, err)
	assert.NotContains(t, w.Body.String(), string(encodedExecutable))
	if first.Total > 1 {
		require.NotEmpty(t, first.ContinueToken)
		w = call(handler, "GET", "/api/v1/host/processes?continue="+url.QueryEscape(first.ContinueToken), "", token, "")
		require.Equal(t, 200, w.Code, w.Body.String())
		var next model.HostProcessList
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &next))
		assert.Equal(t, first.SnapshotID, next.SnapshotID)
		assert.Equal(t, first.SampledAt, next.SampledAt)
		assert.Equal(t, 400, call(handler, "GET", "/api/v1/host/processes?continue="+url.QueryEscape(first.ContinueToken)+"&limit=2", "", token, "").Code)
	}
	w = call(handler, "GET", "/api/v1/host/processes/"+strconv.Itoa(os.Getpid()), "", token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var detail model.HostProcessDetail
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &detail))
	assert.Equal(t, os.Getpid(), detail.Process.PID)
	require.NotEmpty(t, detail.Process.Name)
	require.NotEmpty(t, detail.Process.StartToken)
	w = call(handler, "GET", "/api/v1/host/processes/"+strconv.Itoa(os.Getpid())+"?startToken="+url.QueryEscape(detail.Process.StartToken), "", token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, 409, call(handler, "GET", "/api/v1/host/processes/"+strconv.Itoa(os.Getpid())+"?startToken=old-process", "", token, "").Code)
	w = call(handler, "GET", "/api/v1/host/process-icons/"+detail.Process.IconKey, "", token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, "image/png", w.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	_, err = png.Decode(bytes.NewReader(w.Body.Bytes()))
	require.NoError(t, err)
	assert.Contains(t, []string{"generic", "native", "desktop"}, w.Header().Get("X-Process-Icon-Source"))
	for _, path := range []string{"/api/v1/host/processes?limit=0", "/api/v1/host/processes?limit=1001", "/api/v1/host/processes?prefix=a&prefix=b", "/api/v1/host/processes?path=C:/Windows", "/api/v1/host/processes?continue=invalid", "/api/v1/host/processes/-1", "/api/v1/host/processes/4294967296", "/api/v1/host/processes/1?path=/etc/passwd", "/api/v1/host/process-icons/generic?path=/etc/passwd"} {
		assert.Equal(t, 400, call(handler, "GET", path, "", token, "").Code, path)
	}
	assert.Equal(t, 404, call(handler, "GET", "/api/v1/host/process-icons/unknown-icon-key", "", token, "").Code)
	assert.Equal(t, 404, call(handler, "GET", "/api/v1/host/processes/4294967295", "", token, "").Code)
	assert.Equal(t, http.StatusMethodNotAllowed, call(handler, "DELETE", "/api/v1/host/processes/"+strconv.Itoa(os.Getpid()), "", token, "").Code)
}
