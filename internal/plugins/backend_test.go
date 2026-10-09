package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRealGrafanaSDKBackendProcessQueryHealthResourcesRestartAndShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	name := "fixture_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./testdata/sdk-backend")
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))
	executable, err := os.ReadFile(binary)
	require.NoError(t, err)
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	files := map[string][]byte{"plugin.json": []byte(`{"id":"metricspanel-sdk-datasource","name":"SDK Fixture","type":"datasource","backend":true,"executable":"fixture","info":{"version":"1.0.0"},"dependencies":{"grafanaVersion":">=12"}}`), "module.js": []byte(`System.register([],function(){return {execute:function(){}}})`), name: executable}
	for name, body := range files {
		f, err := z.Create(name)
		require.NoError(t, err)
		_, err = f.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, z.Close())
	path := filepath.Join(t.TempDir(), "control.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	manager := New(s, "", []string{"metricspanel-sdk-datasource"})
	defer func() { manager.Close(); s.DB.Close() }()
	_, err = manager.Install(ctx, archive.Bytes(), "")
	require.NoError(t, err)
	ds, err := s.SaveDataSource(ctx, model.DataSourceInput{DataSource: model.DataSource{UID: "sdk-test", Name: "SDK test", Type: "metricspanel-sdk-datasource"}, SecureJSONData: map[string]string{"apiKey": "test-secret-233"}})
	require.NoError(t, err)
	assert.Greater(t, ds.ID, int64(1))
	t.Setenv("METRICSPANEL_TOKEN", "must-not-be-inherited")
	health, err := manager.Health(ctx, ds)
	require.NoError(t, err)
	assert.Equal(t, backend.HealthStatusOk, health.Status)
	resource, err := manager.Resource(ctx, ds, &backend.CallResourceRequest{Path: "example", URL: "example?x=233", Method: "GET"})
	require.NoError(t, err)
	require.Len(t, resource, 1)
	assert.Contains(t, string(resource[0].Body), `"uid":"sdk-test"`)
	host := manager.processes[ds.Type]
	require.NotNil(t, host)
	results, err := manager.Query(ctx, ds, []backend.DataQuery{{RefID: "A", JSON: json.RawMessage(`{"value":233}`), TimeRange: backend.TimeRange{From: time.Now().Add(-time.Minute), To: time.Now()}}, {RefID: "B", JSON: json.RawMessage(`{"fail":true}`)}})
	require.NoError(t, err)
	require.Len(t, results.Responses["A"].Frames, 1)
	assert.Equal(t, float64(233), results.Responses["A"].Frames[0].Fields[1].At(0))
	require.ErrorContains(t, results.Responses["B"].Error, "fixture query failed")
	require.ErrorContains(t, manager.Uninstall(ctx, ds.Type), "datasources")
	manager.Close()
	assert.True(t, host.client.Exited(), "plugin subprocess remained alive after shutdown")
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	manager = New(s, "", []string{ds.Type})
	ds, err = s.DataSource(ctx, ds.UID)
	require.NoError(t, err)
	health, err = manager.Health(ctx, ds)
	require.NoError(t, err)
	assert.Equal(t, backend.HealthStatusOk, health.Status)
	host = manager.processes[ds.Type]
	_, err = manager.SetEnabled(ctx, ds.Type, false)
	require.NoError(t, err)
	assert.True(t, host.client.Exited())
	_, err = manager.Health(ctx, ds)
	require.ErrorContains(t, err, "not enabled")
	require.NoError(t, s.DeleteDataSource(ctx, ds.UID))
	require.NoError(t, manager.Uninstall(ctx, ds.Type))
	entries, err := os.ReadDir(manager.Root)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
