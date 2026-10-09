package plugins

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/genproto/pluginv2"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRealAppSDKContextBundledLifecycleAndRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	temp := t.TempDir()
	binary := filepath.Join(temp, "fixture")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, "./testdata/sdk-backend")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	archive := filepath.Join(temp, "app.zip")
	out, err = exec.CommandContext(ctx, binary, "--package-app", archive).CombinedOutput()
	require.NoError(t, err, string(out))
	raw, err := os.ReadFile(archive)
	require.NoError(t, err)
	path := filepath.Join(temp, "control.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	m := New(s, "", []string{"metricspanel-sdk-app"})
	defer func() { m.Close(); s.DB.Close() }()
	parent, err := m.Install(ctx, raw, "")
	require.NoError(t, err)
	assert.Equal(t, "app", parent.Type)
	items, err := s.Plugins(ctx)
	require.NoError(t, err)
	require.Len(t, items, 2)
	pinned := true
	app, err := m.ConfigureApp(ctx, parent.ID, model.AppSettingsInput{Pinned: &pinned, JSONData: json.RawMessage(`{"label":"Operations"}`), SecureJSONData: map[string]string{"apiKey": "app-secret-233"}})
	require.NoError(t, err)
	pc, err := m.AppContext(ctx, parent.ID)
	require.NoError(t, err)
	assert.Equal(t, "app-secret-233", pc.AppInstanceSettings.DecryptedSecureJSONData["apiKey"])
	assert.Equal(t, time.UnixMilli(app.UpdatedAt), pc.AppInstanceSettings.Updated)
	response, err := m.ResourceContext(ctx, pc, &backend.CallResourceRequest{Path: "example", Method: "GET"})
	require.NoError(t, err)
	require.Len(t, response, 1)
	assert.Contains(t, string(response[0].Body), `"configured":true`)
	assert.Contains(t, string(response[0].Body), `"label":"Operations"`)
	assert.NotContains(t, string(response[0].Body), "app-secret-233")
	liveContext, err := m.StreamContext(ctx, "plugin", parent.ID)
	require.NoError(t, err)
	subscription, err := m.SubscribeStream(ctx, liveContext, "counter", nil)
	require.NoError(t, err)
	assert.Equal(t, int32(backend.SubscribeStreamStatusOK), int32(subscription.Status))
	require.NotEmpty(t, subscription.Data)
	streamCtx, stopStream := context.WithTimeout(ctx, 5*time.Second)
	streamStart := time.Now().UnixMilli()
	err = m.RunStream(streamCtx, liveContext, "counter", nil, func(packet []byte) error {
		var frame struct {
			Schema struct {
				Name string `json:"name"`
			} `json:"schema"`
			Data struct {
				Values [][]float64 `json:"values"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(packet, &frame))
		assert.Equal(t, "live-counter", frame.Schema.Name)
		require.Len(t, frame.Data.Values, 2)
		require.Len(t, frame.Data.Values[0], 1)
		require.Len(t, frame.Data.Values[1], 1)
		assert.GreaterOrEqual(t, frame.Data.Values[0][0], float64(streamStart))
		assert.LessOrEqual(t, frame.Data.Values[0][0], float64(time.Now().UnixMilli()))
		assert.Equal(t, 234.0, frame.Data.Values[1][0])
		stopStream()
		return context.Canceled
	})
	require.ErrorIs(t, err, context.Canceled)
	stopStream()
	ds, err := s.SaveDataSource(ctx, model.DataSourceInput{DataSource: model.DataSource{UID: "app-child", Name: "App child", Type: "metricspanel-sdk-datasource"}, SecureJSONData: map[string]string{"apiKey": "test-secret-233"}})
	require.NoError(t, err)
	childContext, err := m.PluginContext(ctx, ds)
	require.NoError(t, err)
	assert.Equal(t, "app-secret-233", childContext.AppInstanceSettings.DecryptedSecureJSONData["apiKey"])
	response, err = m.Resource(ctx, ds, &backend.CallResourceRequest{Path: "example"})
	require.NoError(t, err)
	assert.Contains(t, string(response[0].Body), `"configured":true`)
	rootProcess, childProcess := m.processes[parent.ID], m.processes[ds.Type]
	chunks := 0
	err = m.QueryChunked(ctx, ds, []backend.DataQuery{{RefID: "A", JSON: json.RawMessage(`{"value":233,"chunks":2,"requireApp":true}`)}}, func(chunk *pluginv2.QueryChunkedDataResponse) error {
		assert.Equal(t, "A", chunk.RefId)
		chunks++
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2, chunks)
	require.NotNil(t, rootProcess)
	require.NotNil(t, childProcess)
	_, err = m.SetEnabled(ctx, ds.Type, false)
	require.NoError(t, err)
	assert.True(t, childProcess.client.Exited())
	assert.False(t, rootProcess.client.Exited(), "disabling one child killed the parent")
	_, err = m.SetEnabled(ctx, ds.Type, true)
	require.NoError(t, err)
	_, err = m.Health(ctx, ds)
	require.NoError(t, err)
	childProcess = m.processes[ds.Type]
	_, err = m.SetEnabled(ctx, parent.ID, false)
	require.NoError(t, err)
	assert.True(t, rootProcess.client.Exited())
	assert.True(t, childProcess.client.Exited())
	_, err = m.Asset(ctx, ds.Type, "module.js")
	require.ErrorContains(t, err, "disabled")
	_, err = m.Health(ctx, ds)
	require.ErrorContains(t, err, "not enabled")
	_, err = m.SetEnabled(ctx, ds.Type, true)
	require.ErrorContains(t, err, "owning package")
	_, err = m.SetEnabled(ctx, parent.ID, true)
	require.NoError(t, err)
	_, err = m.Asset(ctx, ds.Type, "module.js")
	require.NoError(t, err)
	m.Close()
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	m = New(s, "", []string{parent.ID})
	pc, err = m.AppContext(ctx, parent.ID)
	require.NoError(t, err)
	response, err = m.ResourceContext(ctx, pc, &backend.CallResourceRequest{Path: "example"})
	require.NoError(t, err)
	assert.Contains(t, string(response[0].Body), `"label":"Operations"`)
	require.NoError(t, s.DeleteDataSource(ctx, ds.UID))
	require.NoError(t, m.Uninstall(ctx, parent.ID))
	entries, err := os.ReadDir(m.Root)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
