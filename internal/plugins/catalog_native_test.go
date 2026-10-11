package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nativeClockMetadata = `{"id":31,"slug":"grafana-clock-panel","name":"Clock","typeCode":"panel","version":"3.2.4","orgName":"Grafana Labs","downloads":233,"signatureType":"grafana","json":{"dependencies":{"grafanaDependency":">=11.0.0-0","plugins":[]},"info":{"links":[],"screenshots":[]}},"readme":"<h2>Clock</h2>","changelog":"<h2>3.2.4</h2>","packages":{"any":{"downloadUrl":"/api/plugins/grafana-clock-panel/versions/3.2.4/download"}},"future":{"fingerprint":18446744073709551615}}`
const nativeClockVersion = `{"id":10701,"pluginId":31,"pluginSlug":"grafana-clock-panel","version":"3.2.4","createdAt":"2026-08-25T13:03:45Z","grafanaDependency":">=11.0.0-0","isCompatible":true,"status":"active","packages":{"any":{}},"future":18446744073709551615}`

func TestNativeCatalogRetainsOfficialJSONAndSharesVerifiedCache(t *testing.T) {
	var requests atomic.Int32
	catalog := catalogTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/plugins":
			assert.Equal(t, "true", r.URL.Query().Get("includeDeprecated"))
			fmt.Fprint(w, `{"items":[`+nativeClockMetadata+`],"opaque":{"fingerprint":18446744073709551615}}`)
		case "/api/plugins/grafana-clock-panel":
			fmt.Fprint(w, nativeClockMetadata)
		case "/api/plugins/grafana-clock-panel/versions":
			fmt.Fprint(w, `{"items":[`+nativeClockVersion+`],"pagination":{"next":null}}`)
		case "/api/plugins/grafana-clock-panel/versions/3.2.4":
			fmt.Fprint(w, nativeClockVersion)
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	list, err := catalog.NativeList(ctx, true)
	require.NoError(t, err)
	assert.Contains(t, string(list), `"id":31`)
	assert.Contains(t, string(list), `"fingerprint":18446744073709551615`)
	assert.Contains(t, string(list), `"typeCode":"panel"`)
	normalized, err := catalog.List(ctx, "clock", "panel")
	require.NoError(t, err)
	require.Len(t, normalized.Items, 1)
	assert.Equal(t, "grafana-clock-panel", normalized.Items[0].ID)
	assert.Equal(t, int32(1), requests.Load(), "raw and normalized reads share the successful upstream cache")

	detail, err := catalog.NativeDetail(ctx, "grafana-clock-panel")
	require.NoError(t, err)
	assert.JSONEq(t, nativeClockMetadata, string(detail))
	assert.Contains(t, string(detail), `18446744073709551615`)
	latest, err := catalog.LatestVersion(ctx, "grafana-clock-panel")
	require.NoError(t, err)
	assert.Equal(t, "3.2.4", latest)
	assert.Equal(t, "3.2.4", catalog.CachedLatestVersion("grafana-clock-panel"))
	assert.Empty(t, catalog.CachedLatestVersion("unknown"))
	versions, err := catalog.NativeVersions(ctx, "grafana-clock-panel")
	require.NoError(t, err)
	assert.Contains(t, string(versions), `"createdAt":"2026-08-25T13:03:45Z"`)
	assert.Contains(t, string(versions), `"isCompatible":true`)
	assert.Contains(t, string(versions), `18446744073709551615`)
	exact, err := catalog.NativeVersion(ctx, "grafana-clock-panel", "3.2.4")
	require.NoError(t, err)
	assert.JSONEq(t, nativeClockVersion, string(exact))
	// Returned raw bodies cannot be used to corrupt subsequent cached reads.
	detail[0] = '!'
	copy, err := catalog.NativeDetail(ctx, "grafana-clock-panel")
	require.NoError(t, err)
	assert.True(t, json.Valid(copy))
	assert.Equal(t, int32(4), requests.Load())
}

func TestNativeCatalogDefaultDeprecatedFlagAndInvalidVersion(t *testing.T) {
	var requests atomic.Int32
	catalog := catalogTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Equal(t, "false", r.URL.Query().Get("includeDeprecated"))
		fmt.Fprint(w, `{"items":[],"pagination":{"count":0}}`)
	})
	raw, err := catalog.NativeList(context.Background(), false)
	require.NoError(t, err)
	assert.JSONEq(t, `{"items":[],"pagination":{"count":0}}`, string(raw))
	for _, version := range []string{"latest", "../download", "3.2", strings.Repeat("1", 101)} {
		_, err := catalog.NativeVersion(context.Background(), "clock", version)
		require.ErrorIs(t, err, ErrCatalogInput)
	}
	assert.Equal(t, int32(1), requests.Load())
	for _, response := range []string{`{"version":"3.2.3","pluginSlug":"grafana-clock-panel"}`, `{"version":"3.2.4","pluginSlug":"other-panel"}`} {
		bad := catalogTestClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, response) })
		_, err := bad.NativeVersion(context.Background(), "grafana-clock-panel", "3.2.4")
		require.ErrorContains(t, err, "different plugin version")
		assert.Empty(t, bad.cache)
	}
}

func TestNativePluginMarkdownSignatureAndObservedErrors(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	manager := New(database, "", nil)
	t.Cleanup(func() { manager.Close(); database.DB.Close() })
	raw, err := os.ReadFile("testdata/grafana-clock-panel-3.2.4.zip")
	require.NoError(t, err)
	plugin, err := manager.Install(context.Background(), raw, clockHash)
	require.NoError(t, err)
	signature, err := manager.SignatureInfo(context.Background(), plugin.ID)
	require.NoError(t, err)
	assert.Equal(t, "valid", signature.Status)
	assert.Equal(t, "grafana", signature.Type)
	assert.Equal(t, "grafana", signature.Org)
	readme, err := manager.Markdown(context.Background(), plugin.ID, "README")
	require.NoError(t, err)
	assert.Contains(t, string(readme), "Clock")
	changelog, err := manager.Markdown(context.Background(), plugin.ID, "CHANGELOG.md")
	require.NoError(t, err)
	assert.Contains(t, string(changelog), "3.2.4")
	_, err = manager.Markdown(context.Background(), plugin.ID, "../../module.js")
	require.ErrorIs(t, err, ErrCatalogInput)
	_, err = manager.SetEnabled(context.Background(), plugin.ID, false)
	require.NoError(t, err)
	_, err = manager.Markdown(context.Background(), plugin.ID, "readme")
	require.NoError(t, err, "disabled plugins remain inspectable without serving their executable assets")
	_, err = manager.Asset(context.Background(), plugin.ID, "module.js")
	require.ErrorContains(t, err, "disabled")
	errors, err := manager.InspectionErrors(context.Background())
	require.NoError(t, err)
	assert.Empty(t, errors)
	require.NoError(t, os.WriteFile(filepath.Join(manager.Root, plugin.PackageID, "module.js"), []byte("modified"), 0600))
	errors, err = manager.InspectionErrors(context.Background())
	require.NoError(t, err)
	require.Len(t, errors, 1)
	assert.Equal(t, "signatureModified", errors[0].ErrorCode)
	assert.Equal(t, plugin.ID, errors[0].PluginID)
	signature, err = manager.SignatureInfo(context.Background(), plugin.ID)
	require.NoError(t, err)
	assert.Equal(t, "modified", signature.Status)
	_, err = manager.Markdown(context.Background(), plugin.ID, "README")
	require.ErrorContains(t, err, "checksum changed")
}

func TestNativePluginStartupErrorsPersistUntilRealRecoveryOrUninstall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fixtureDir := t.TempDir()
	source := filepath.Join(fixtureDir, "startup.go")
	require.NoError(t, os.WriteFile(source, []byte(`package main
import (
 "os"
 "github.com/grafana/grafana-plugin-sdk-go/backend"
)
func main() {
 if _, err := os.Stat(".fixture-fail-start"); err == nil { os.Exit(1) }
 if err := backend.Serve(backend.ServeOpts{}); err != nil { os.Exit(2) }
}`), 0600))
	executable := "startup_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	binary := filepath.Join(fixtureDir, executable)
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, source)
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))
	program, err := os.ReadFile(binary)
	require.NoError(t, err)
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	for name, body := range map[string][]byte{
		"plugin.json": []byte(`{"id":"native-startup-source","name":"Startup fixture","type":"datasource","backend":true,"executable":"startup","info":{"version":"1.0.0"}}`),
		"module.js":   []byte(`System.register([],function(){return{execute:function(){}}})`),
		executable:    program,
	} {
		entry, err := z.Create(name)
		require.NoError(t, err)
		_, err = entry.Write(body)
		require.NoError(t, err)
	}
	require.NoError(t, z.Close())
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	manager := New(database, "", []string{"native-startup-source"})
	t.Cleanup(func() { manager.Close(); database.DB.Close() })
	p, err := manager.Install(ctx, archive.Bytes(), "")
	require.NoError(t, err)
	errors, err := manager.InspectionErrors(ctx)
	require.NoError(t, err)
	assert.Empty(t, errors)
	// The file inventory and executable are valid. This failure can only be
	// observed by actually starting the subprocess, rather than inspecting files.
	marker := filepath.Join(manager.directory(p), ".fixture-fail-start")
	require.NoError(t, os.WriteFile(marker, nil, 0600))
	_, err = manager.getProcess(ctx, p.ID)
	require.Error(t, err)
	errors, err = manager.InspectionErrors(ctx)
	require.NoError(t, err)
	require.Len(t, errors, 1)
	assert.Equal(t, "failedBackendStart", errors[0].ErrorCode)
	assert.Equal(t, p.ID, errors[0].PluginID)
	assert.Empty(t, manager.processes, "the failed child must be reaped without losing its current error")
	require.NoError(t, os.Remove(marker))
	errors, err = manager.InspectionErrors(ctx)
	require.NoError(t, err)
	require.Len(t, errors, 1, "files alone do not prove startup recovered")
	host, err := manager.getProcess(ctx, p.ID)
	require.NoError(t, err)
	require.NotNil(t, host.data, "recovery requires the real SDK handshake and clients")
	errors, err = manager.InspectionErrors(ctx)
	require.NoError(t, err)
	assert.Empty(t, errors)
	assert.Empty(t, manager.failures)
	manager.Close()
	assert.True(t, host.client.Exited())
	// Package verification failures are also retained from the actual attempt,
	// but restored files must not keep reporting a stale signature failure.
	installedBinary := filepath.Join(manager.directory(p), executable)
	require.NoError(t, os.WriteFile(installedBinary, []byte("modified"), 0750))
	_, err = manager.getProcess(ctx, p.ID)
	require.ErrorContains(t, err, "checksum changed")
	assert.Equal(t, "signatureModified", manager.failures[p.ID].code)
	require.NoError(t, os.WriteFile(installedBinary, program, 0750))
	errors, err = manager.InspectionErrors(ctx)
	require.NoError(t, err)
	assert.Empty(t, errors)
	require.NoError(t, os.WriteFile(marker, nil, 0600))
	_, err = manager.getProcess(ctx, p.ID)
	require.Error(t, err)
	errors, err = manager.InspectionErrors(ctx)
	require.NoError(t, err)
	require.Len(t, errors, 1)
	require.NoError(t, manager.Uninstall(ctx, p.ID))
	errors, err = manager.InspectionErrors(ctx)
	require.NoError(t, err)
	assert.Empty(t, errors)
	assert.Empty(t, manager.failures, "uninstall clears root and bundled startup failures")
}
