package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentSQLQueriesStreamingAndRulesAfterRestart(t *testing.T) {
	t.Setenv("METRICSPANEL_TOKEN", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "sql.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	app := server.New(s, "", 30)
	host := httptest.NewServer(app.Handler())
	t.Cleanup(func() { host.Close(); app.Plugins.Close(); app.Live.Close(); s.DB.Close() })
	payload := []byte(`{"from":"1000","to":"4000","queries":[{"refId":"A","hide":true,"expr":"vector(233)","instant":true,"datasource":{"uid":"metricspanel"}},{"refId":"Q","type":"sql","expression":"SELECT __value__ * 2 AS total FROM A"}]}`)
	file := filepath.Join(dir, "query.json")
	require.NoError(t, os.WriteFile(file, payload, 0600))
	raw, err := capture(t, "datasources", "query", "--id", "__expr__", "--file", file, "--server", host.URL)
	require.NoError(t, err)
	var result backend.QueryDataResponse
	require.NoError(t, json.Unmarshal(raw, &result))
	require.NoError(t, result.Responses["Q"].Error)
	n, err := result.Responses["Q"].Frames[0].Fields[0].FloatAt(0)
	require.NoError(t, err)
	assert.Equal(t, float64(466), n)
	client := apiClient{endpoint: host.URL}
	var stream bytes.Buffer
	require.NoError(t, client.queryDatasource(context.Background(), "__expr__", payload, true, &stream))
	assert.Contains(t, stream.String(), `"refId":"Q"`)
	assert.Contains(t, stream.String(), "466")
	file = filepath.Join(dir, "rule.json")
	rule := `{"uid":"agent-sql","title":"SQL alert","execution":"grafana","annotations":{"summary":"SQL {{ $values.C.Value }}"},"grafana":{"condition":"C","data":[{"refId":"A","datasourceUid":"metricspanel","model":{"expr":"vector(233)","instant":true}},{"refId":"C","datasourceUid":"__expr__","model":{"type":"sql","expression":"SELECT CAST(__value__ > 200 AS SIGNED) AS firing FROM A","large":14695981039346656037}}]}}`
	require.NoError(t, os.WriteFile(file, []byte(rule), 0600))
	_, err = capture(t, "alerts", "save", "--file", file, "--server", host.URL)
	require.NoError(t, err)
	raw, err = capture(t, "alerts", "evaluate", "--id", "agent-sql", "--server", host.URL)
	require.NoError(t, err)
	var before model.AlertRuleView
	require.NoError(t, json.Unmarshal(raw, &before))
	require.Len(t, before.Runtime.Instances, 1)
	assert.Equal(t, "Firing", before.Runtime.Instances[0].State)
	assert.Equal(t, "SQL 1", before.Runtime.Instances[0].Annotations["summary"])
	host.Close()
	app.Plugins.Close()
	app.Live.Close()
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	app = server.New(s, "", 30)
	host = httptest.NewServer(app.Handler())
	raw, err = capture(t, "alerts", "get", "--id", "agent-sql", "--server", host.URL)
	require.NoError(t, err)
	var reopened model.AlertRuleView
	require.NoError(t, json.Unmarshal(raw, &reopened))
	assert.Equal(t, before.Runtime, reopened.Runtime)
	assert.Contains(t, string(reopened.Grafana), `14695981039346656037`)
	time.Sleep(time.Until(time.UnixMilli(before.Runtime.LastEvaluation + 2)))
	_, err = capture(t, "alerts", "evaluate", "--id", "agent-sql", "--server", host.URL)
	require.NoError(t, err)
	history, err := s.AlertHistory(context.Background(), "agent-sql", 100)
	require.NoError(t, err)
	assert.Len(t, history, 1)
	raw, err = capture(t, "schema")
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"sql"`)
}
