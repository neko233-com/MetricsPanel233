package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuiltinGrafanaAgentQueriesJSONAndNDJSON(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "")
	s, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	srv := server.New(s, "agent-token-233", 30)
	defer srv.Plugins.Close()
	defer srv.Live.Close()
	h := httptest.NewServer(srv.Handler())
	defer h.Close()
	file := filepath.Join(t.TempDir(), "query.json")
	require.NoError(t, os.WriteFile(file, []byte(`{"from":"1000","to":"4000","queries":[{"refId":"R","intervalMs":1000,"queryType":"randomWalk","startValue":233,"spread":0}]}`), 0600))
	raw, err := capture(t, "datasources", "query", "--id", "grafana", "--file", file, "--server", h.URL, "--token", "agent-token-233")
	require.NoError(t, err)
	var result backend.QueryDataResponse
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Len(t, result.Responses["R"].Frames, 1)
	assert.Equal(t, 3, result.Responses["R"].Frames[0].Rows())
	assert.Equal(t, float64(233), result.Responses["R"].Frames[0].Fields[1].At(2))
	client := apiClient{endpoint: h.URL, token: "agent-token-233"}
	var output bytes.Buffer
	err = client.queryDatasource(context.Background(), "grafana", []byte(`{"queries":[{"refId":"L","queryType":"list","path":""},{"refId":"Bad","queryType":"list","path":"../data"}]}`), true, &output)
	require.ErrorContains(t, err, "1 datasource queries failed")
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	require.Len(t, lines, 2)
	var good, failed map[string]any
	require.NoError(t, json.Unmarshal(lines[0], &good))
	require.NoError(t, json.Unmarshal(lines[1], &failed))
	// Independent query groups may flush in either order.
	if good["refId"] == "Bad" {
		good, failed = failed, good
	}
	assert.Equal(t, "L", good["refId"])
	assert.Contains(t, output.String(), "index.html")
	assert.Equal(t, "Bad", failed["refId"])
	assert.Contains(t, failed["error"], "relative public asset folder")
	var generated int
	require.NoError(t, s.DB.QueryRow("SELECT count(*) FROM series").Scan(&generated))
	assert.Zero(t, generated)
}
