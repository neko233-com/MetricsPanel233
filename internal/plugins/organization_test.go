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
	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/require"
)

func TestOrganizationSDKRealProcessContextAppAndStreamIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := t.TempDir()
	binary := filepath.Join(dir, "fixture")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, "./testdata/sdk-backend").CombinedOutput()
	require.NoError(t, err, string(output))
	archive := filepath.Join(dir, "app.zip")
	output, err = exec.CommandContext(ctx, binary, "--package-app", archive).CombinedOutput()
	require.NoError(t, err, string(output))
	contents, err := os.ReadFile(archive)
	require.NoError(t, err)
	primary, err := store.Open(filepath.Join(dir, "primary", "control.db"))
	require.NoError(t, err)
	defer primary.DB.Close()
	first := New(primary, "", []string{"metricspanel-sdk-app"})
	defer first.Close()
	_, err = first.Install(ctx, contents, "")
	require.NoError(t, err)
	secondary, err := store.OpenForOrganization(filepath.Join(dir, "second", "control.db"), 2)
	require.NoError(t, err)
	defer secondary.DB.Close()
	require.NoError(t, secondary.BindPluginRegistry(primary))
	second := New(secondary, first.Root, []string{"metricspanel-sdk-app"})
	defer second.Close()
	principal := identity.Principal{OrgID: 2, ID: 233, Type: identity.TypeUser, Login: "org-two-editor", Name: "Org two editor", Role: identity.RoleEditor}
	secondCtx := identity.WithPrincipal(ctx, principal)
	var firstDS, secondDS model.DataSource
	for index, manager := range []*Manager{first, second} {
		local := ctx
		if index == 1 {
			local = secondCtx
		}
		data, marshalErr := json.Marshal(map[string]string{"label": manager.Store.Path})
		require.NoError(t, marshalErr)
		_, err = manager.ConfigureApp(local, "metricspanel-sdk-app", model.AppSettingsInput{JSONData: data, SecureJSONData: map[string]string{"apiKey": "app-secret-233"}})
		require.NoError(t, err)
		ds, err := manager.Store.SaveDataSource(local, model.DataSourceInput{DataSource: model.DataSource{UID: "same", Name: "Same", Type: "metricspanel-sdk-datasource"}, SecureJSONData: map[string]string{"apiKey": "test-secret-233"}})
		require.NoError(t, err)
		if index == 0 {
			firstDS = ds
		} else {
			secondDS = ds
		}
	}
	for _, invalid := range []context.Context{ctx, identity.WithPrincipal(ctx, identity.LocalPrincipal()), identity.WithPrincipal(ctx, identity.Principal{}), identity.WithPrincipal(ctx, identity.Principal{OrgID: 2, ID: 233, Type: identity.TypeUser, Login: "none", Role: identity.RoleNone})} {
		_, err = second.Query(invalid, secondDS, []backend.DataQuery{{RefID: "A", JSON: []byte(`{"value":2}`)}})
		require.Error(t, err)
		require.Empty(t, second.processes, "unauthorized calls must not start a subprocess")
	}
	query := backend.DataQuery{RefID: "A", JSON: []byte(`{"value":2,"requireOrgId":2,"requireLogin":"org-two-editor"}`), TimeRange: backend.TimeRange{From: time.Now().Add(-time.Minute), To: time.Now()}}
	result, err := second.Query(secondCtx, secondDS, []backend.DataQuery{query})
	require.NoError(t, err)
	require.Equal(t, 2.0, result.Responses["A"].Frames[0].Fields[1].At(0))
	pc, err := second.PluginContext(secondCtx, secondDS)
	require.NoError(t, err)
	require.Equal(t, int64(2), pc.OrgID)
	require.Equal(t, "org-2", pc.Namespace)
	require.Equal(t, "Editor", pc.User.Role)
	resource, err := second.ResourceContext(secondCtx, pc, &backend.CallResourceRequest{Path: "organization-context", Method: "GET"})
	require.NoError(t, err)
	var received struct {
		Org       int64        `json:"org"`
		Namespace string       `json:"namespace"`
		User      backend.User `json:"user"`
	}
	require.NoError(t, json.Unmarshal(resource[0].Body, &received))
	require.Equal(t, int64(2), received.Org)
	require.Equal(t, "org-2", received.Namespace)
	require.Equal(t, principal.Login, received.User.Login)
	_, err = first.ResourceContext(ctx, pc, &backend.CallResourceRequest{Path: "example"})
	require.Error(t, err, "foreign plugin context cannot reach another organization's process")
	_, err = second.PluginContext(secondCtx, firstDS)
	require.ErrorContains(t, err, "organization")
	appPC, err := second.AppContext(secondCtx, "metricspanel-sdk-app")
	require.NoError(t, err)
	resource, err = second.ResourceContext(secondCtx, appPC, &backend.CallResourceRequest{Path: "example"})
	require.NoError(t, err)
	var appResult struct {
		Org   int64  `json:"org"`
		Label string `json:"label"`
	}
	require.NoError(t, json.Unmarshal(resource[0].Body, &appResult))
	require.Equal(t, int64(2), appResult.Org)
	require.Equal(t, secondary.Path, appResult.Label)
	streamPC, err := second.StreamContext(secondCtx, "ds", "same")
	require.NoError(t, err)
	subscription, err := second.SubscribeStream(secondCtx, streamPC, "counter", nil)
	require.NoError(t, err)
	require.Equal(t, int32(backend.SubscribeStreamStatusOK), int32(subscription.Status))
	_, err = first.SubscribeStream(ctx, streamPC, "counter", nil)
	require.Error(t, err)
	_, err = second.SubscribeStream(ctx, streamPC, "counter", nil)
	require.Error(t, err)
	// Disabling the primary org's app settings must not disable the org2 app.
	disabled := false
	_, err = first.ConfigureApp(ctx, "metricspanel-sdk-app", model.AppSettingsInput{Enabled: &disabled})
	require.NoError(t, err)
	_, err = first.PluginContext(ctx, firstDS)
	require.ErrorContains(t, err, "not enabled")
	_, err = second.Query(secondCtx, secondDS, []backend.DataQuery{query})
	require.NoError(t, err)
	for _, process := range second.processes {
		require.False(t, process.client.Exited())
	}
	second.Close()
	require.Empty(t, second.processes)
}
