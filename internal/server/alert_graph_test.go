package server_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sdkAlertGraph = `{"uid":"sdk-graph","title":"Mixed SDK graph","condition":"C","for":"10s","labels":{"severity":"{{ if gt $values.C0.Value 400.0 }}critical{{ else }}warning{{ end }}"},"annotations":{"summary":"SDK multiplied {{ printf \"%.0f\" $values.C0.Value }}","description":"{{ $labels.severity }} / {{ $values.C1.Value }}"},"data":[
{"refId":"A","datasourceUid":"metricspanel","model":{"expr":"vector(233)","instant":true}},
{"refId":"B","datasourceUid":"alert-sdk","model":{"value":2,"requireAlert":true}},
{"refId":"D","datasourceUid":"__expr__","model":{"type":"math","expression":"$A*$B"}},
{"refId":"C","datasourceUid":"__expr__","model":{"type":"classic_conditions","conditions":[{"query":{"params":["D"]},"reducer":{"type":"avg"},"evaluator":{"type":"gt","params":[400]}},{"query":{"params":["A"]},"reducer":{"type":"last"},"operator":{"type":"and"},"evaluator":{"type":"gt","params":[200]}}]}}
]}`

func TestAlertGraphRealSDKHeadersPersistenceAndNativeEdits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "fixture")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	out, err := exec.CommandContext(ctx, "go", "build", "-o", binary, "../plugins/testdata/sdk-backend").CombinedOutput()
	require.NoError(t, err, string(out))
	archive := filepath.Join(dir, "sdk.zip")
	out, err = exec.CommandContext(ctx, binary, "--package", archive).CombinedOutput()
	require.NoError(t, err, string(out))
	zip, err := os.ReadFile(archive)
	require.NoError(t, err)
	database := filepath.Join(dir, "control.db")
	s, err := store.Open(database)
	require.NoError(t, err)
	const token = "alert-graph-test-233233"
	app := server.New(s, token, 30)
	app.Plugins.AllowUnsigned["metricspanel-sdk-datasource"] = true
	t.Cleanup(func() { app.Plugins.Close(); app.Live.Close(); s.DB.Close() })
	_, err = app.Plugins.Install(ctx, zip, "")
	require.NoError(t, err)
	_, err = s.SaveDataSource(ctx, model.DataSourceInput{DataSource: model.DataSource{UID: "alert-sdk", Name: "Alert SDK", Type: "metricspanel-sdk-datasource"}, SecureJSONData: map[string]string{"apiKey": "test-secret-233"}})
	require.NoError(t, err)
	h := app.Handler()
	assert.Equal(t, 401, call(h, "POST", "/api/v1/provisioning/alert-rules", sdkAlertGraph, "", "").Code)
	w := call(h, "POST", "/api/v1/provisioning/alert-rules", sdkAlertGraph, token, "")
	require.Equal(t, 201, w.Code, w.Body.String())
	view, err := s.AlertRule(ctx, "sdk-graph")
	require.NoError(t, err)
	assert.Equal(t, "grafana", view.Execution)
	assert.Empty(t, view.Expr)
	at := time.Now().Truncate(time.Millisecond)
	first, err := app.Alerts.Evaluate(ctx, view.UID, at)
	require.NoError(t, err)
	require.Equal(t, "ok", first.Runtime.Health, first.Runtime.Error)
	require.Len(t, first.Runtime.Instances, 1)
	assert.Equal(t, "Pending", first.Runtime.Instances[0].State)
	assert.Contains(t, string(first.Runtime.Instances[0].Matches), `"value":"466"`)
	assert.Equal(t, "critical", first.Runtime.Instances[0].Labels["severity"])
	assert.Equal(t, "SDK multiplied 466", first.Runtime.Instances[0].Annotations["summary"])
	assert.Equal(t, "[no value] / 233", first.Runtime.Instances[0].Annotations["description"])
	assert.Empty(t, first.Runtime.Instances[0].TemplateErrors)
	activeAt := first.Runtime.Instances[0].ActiveAt
	app.Plugins.Close()
	app.Live.Close()
	require.NoError(t, s.DB.Close())
	s, err = store.Open(database)
	require.NoError(t, err)
	app = server.New(s, token, 30)
	app.Plugins.AllowUnsigned["metricspanel-sdk-datasource"] = true
	second, err := app.Alerts.Evaluate(ctx, view.UID, at.Add(10*time.Second))
	require.NoError(t, err)
	require.Equal(t, "ok", second.Runtime.Health, second.Runtime.Error)
	assert.Equal(t, "Firing", second.Runtime.Instances[0].State)
	assert.Equal(t, activeAt, second.Runtime.Instances[0].ActiveAt)
	assert.Contains(t, string(second.Runtime.Instances[0].Matches), `"value":"466"`)
	assert.Equal(t, first.Runtime.Instances[0].Annotations, second.Runtime.Instances[0].Annotations)
	active := call(app.Handler(), "GET", "/prometheus/api/v1/alerts", "", token, "")
	assert.Contains(t, active.Body.String(), "SDK multiplied 466")
	var samples int
	require.NoError(t, s.DB.QueryRow("SELECT count(*) FROM samples").Scan(&samples))
	assert.Zero(t, samples)
	second.Title = "Renamed graph"
	body, err := json.Marshal(second.AlertRule)
	require.NoError(t, err)
	w = call(app.Handler(), "PUT", "/api/v1/alerts/rules/sdk-graph", string(body), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"execution":"grafana"`)
	assert.Contains(t, call(app.Handler(), "GET", "/api/v1/provisioning/alert-rules/sdk-graph", "", token, "").Body.String(), "Renamed graph")
	current, err := s.AlertRule(ctx, view.UID)
	require.NoError(t, err)
	current.Expr = "vector(0)"
	body, err = json.Marshal(current.AlertRule)
	require.NoError(t, err)
	w = call(app.Handler(), "PUT", "/api/v1/alerts/rules/sdk-graph", string(body), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"execution":"promql"`)
	assert.NotContains(t, w.Body.String(), `"grafana":`)
}

func TestAlertGraphRejectsUnavailableSourcesAndBadDependenciesBeforeSave(t *testing.T) {
	s, h := setup(t, "")
	for _, graph := range []string{
		`{"condition":"A","data":[{"refId":"A","datasourceUid":"missing-sdk","model":{}}]}`,
		`{"condition":"C","data":[{"refId":"C","datasourceUid":"__expr__","model":{"type":"math","expression":"$Missing"}}]}`,
		`{"condition":"C","data":[{"refId":"C","datasourceUid":"__expr__","model":{"type":"math","expression":"$C"}}]}`,
	} {
		body := `{"uid":"bad","title":"Bad","execution":"grafana","grafana":` + graph + `}`
		w := call(h, "POST", "/api/v1/alerts/rules", body, "", "")
		assert.Equal(t, 400, w.Code, w.Body.String())
	}
	rules, err := s.AlertRules(context.Background())
	require.NoError(t, err)
	assert.Empty(t, rules)
}
