package server_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/grafana"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nativeImportDescriptor struct {
	UID              string `json:"uid"`
	PluginID         string `json:"pluginId"`
	Title            string `json:"title"`
	Imported         bool   `json:"imported"`
	DashboardID      int64  `json:"dashboardId"`
	FolderID         int64  `json:"folderId"`
	FolderUID        string `json:"folderUid"`
	Path             string `json:"path"`
	Revision         int64  `json:"revision"`
	ImportedRevision int64  `json:"importedRevision"`
	Removed          bool   `json:"removed"`
}

func TestNativeGrafanaInterpolateReadonlyAndOfficialGnetShape(t *testing.T) {
	db, app, handler := newCatalogServer(t)
	metadata := `{"id":1860,"name":"Official shape","orgName":"Community","updatedAt":"2026-10-01T01:02:03Z","revision":45,"json":{"title":"Official shape","future":9007199254740993},"unknown":{"kept":true}}`
	app.Community.HTTPClient.Transport = catalogTransport(func(r *http.Request) (*http.Response, error) {
		assert.Equal(t, "https://grafana.com/api/dashboards/1860", r.URL.String())
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(metadata)), Request: r}, nil
	})
	const token = "catalog-token-233"
	assert.Equal(t, 401, call(handler, "GET", "/api/gnet/dashboards/1860", "", "", "").Code)
	w := call(handler, "GET", "/api/gnet/dashboards/1860", "", token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.JSONEq(t, metadata, w.Body.String())
	assert.Contains(t, w.Body.String(), "9007199254740993")
	before, err := db.Dashboards(context.Background())
	require.NoError(t, err)
	request := `{"dashboard":{"uid":"readonly","title":"Readonly","__inputs":[{"name":"DS","type":"datasource","pluginId":"prometheus"},{"name":"EXPR","type":"datasource","pluginId":"__expr__"}],"__requires":[{"id":"prometheus"}],"__elements":[{"uid":"readonly-lib","name":"Readonly","kind":1,"model":{"type":"stat","title":"Readonly"}}],"panels":[{"id":1,"type":"stat","title":"Panel","datasource":{"type":"prometheus","uid":"${DS}"},"targets":[{"expr":"vector(233)","datasource":"${EXPR}"}]}],"future":9007199254740993},"inputs":[{"name":"*","type":"datasource","pluginId":"different-type-is-descriptive","value":"metricspanel"},{"name":"DS","type":"datasource","value":"unused-later-input"}]}`
	w = call(handler, "POST", "/api/dashboards/interpolate", request, token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"title":"Readonly"`)
	assert.Contains(t, w.Body.String(), `"uid":"metricspanel"`)
	assert.Contains(t, w.Body.String(), `"datasource":"__expr__"`)
	assert.Contains(t, w.Body.String(), "9007199254740993")
	for _, key := range []string{"__inputs", "__requires", "__elements", "dashboardId"} {
		assert.NotContains(t, w.Body.String(), `"`+key+`"`)
	}
	assert.Equal(t, 422, call(handler, "POST", "/api/dashboards/import", `{}`, token, "").Code)
	assert.Equal(t, 422, call(handler, "POST", "/api/dashboards/interpolate", `{}`, token, "").Code)
	missing := `{"dashboard":{"title":"Missing","__inputs":[{"name":"X","type":"constant"}]}}`
	assert.Equal(t, 500, call(handler, "POST", "/api/dashboards/interpolate", missing, token, "").Code)
	assert.Equal(t, 400, call(handler, "POST", "/api/dashboards/import", missing, token, "").Code)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	app.Community.HTTPClient.Transport = catalogTransport(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })
	req := httptest.NewRequest("GET", "/api/gnet/dashboards/1860", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	canceled := httptest.NewRecorder()
	handler.ServeHTTP(canceled, req)
	assert.Equal(t, 408, canceled.Code, canceled.Body.String())
	after, err := db.Dashboards(context.Background())
	require.NoError(t, err)
	assert.Len(t, after, len(before))
	libraries, err := db.LibraryElements(context.Background())
	require.NoError(t, err)
	assert.Empty(t, libraries)
}

func TestNativeGrafanaImportStableIdentityFolderVersionsAndAtomicConflicts(t *testing.T) {
	db, handler := setup(t, "")
	w := call(handler, "POST", "/api/folders", `{"uid":"ops","title":"Operations"}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var folder model.Folder
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &folder))
	request := `{"dashboard":{"uid":"  stable-native  ","title":"  ` + strings.Repeat("监控", 35) + `  ","refresh":"1s","__inputs":[{"name":"DS","type":"datasource","pluginId":"prometheus"}],"__requires":[],"panels":[{"id":1,"type":"stat","title":"Panel","datasource":{"type":"prometheus","uid":"${DS}"},"targets":[{"expr":"vector(233)"}]}],"future":9007199254740993},"inputs":[{"name":"DS","type":"datasource","value":"metricspanel"}],"folderUid":"ops","folderId":999999}`
	w = call(handler, "POST", "/api/dashboards/import", request, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var imported nativeImportDescriptor
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &imported))
	assert.Equal(t, "stable-native", imported.UID)
	assert.Equal(t, strings.Repeat("监控", 35), imported.Title)
	assert.True(t, imported.Imported)
	assert.Equal(t, "ops", imported.FolderUID)
	assert.Equal(t, folder.ID, imported.FolderID)
	assert.Positive(t, imported.DashboardID)
	stored, err := db.DashboardByGrafanaID(context.Background(), imported.DashboardID)
	require.NoError(t, err)
	assert.Equal(t, imported.DashboardID, stored.GrafanaID)
	assert.Equal(t, "ops", stored.FolderUID)
	assert.Contains(t, string(stored.Grafana), `9007199254740993`)
	assert.NotContains(t, string(stored.Grafana), `"__requires"`)
	assert.NotContains(t, string(stored.Grafana), `"__inputs"`)
	w = call(handler, "GET", "/api/dashboards/uid/stable-native", "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"id":`+strconv.FormatInt(imported.DashboardID, 10))
	assert.Contains(t, w.Body.String(), `"folderUid":"ops"`)
	assert.Contains(t, w.Body.String(), `"refresh":"5s"`)
	assert.Equal(t, 412, call(handler, "POST", "/api/dashboards/import", request, "", "").Code)
	updateByID := `{"dashboard":{"id":` + strconv.FormatInt(imported.DashboardID, 10) + `,"version":1,"title":"By numeric ID","panels":[]},"folderUid":"ops"}`
	w = call(handler, "POST", "/api/dashboards/db", updateByID, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"uid":"stable-native"`)
	assert.Contains(t, w.Body.String(), `"version":2`)
	assert.Equal(t, 412, call(handler, "POST", "/api/dashboards/db", updateByID, "", "").Code)
	other := call(handler, "POST", "/api/dashboards/db", `{"dashboard":{"uid":"other-native","title":"Other","panels":[]},"folderUid":"general"}`, "", "")
	require.Equal(t, 200, other.Code, other.Body.String())
	w = call(handler, "POST", "/api/dashboards/db", `{"dashboard":{"id":`+strconv.FormatInt(imported.DashboardID, 10)+`,"uid":"other-native","version":2,"title":"Wrong record","panels":[]},"overwrite":true}`, "", "")
	assert.Equal(t, 400, w.Code, w.Body.String())
	assert.Equal(t, 404, call(handler, "POST", "/api/dashboards/db", `{"dashboard":{"id":999999,"uid":"new-with-unknown-id","title":"Unknown ID","panels":[]},"overwrite":true}`, "", "").Code)
	versioned := `{"dashboard":{"uid":"stable-native","version":2,"title":"Concurrent edit","panels":[]},"folderUid":"ops"}`
	statuses := make(chan int, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			statuses <- call(handler, "POST", "/api/dashboards/db", versioned, "", "").Code
		}()
	}
	workers.Wait()
	close(statuses)
	counts := map[int]int{}
	for code := range statuses {
		counts[code]++
	}
	assert.Equal(t, 1, counts[200])
	assert.Equal(t, 7, counts[412])
	assert.Len(t, counts, 2)
	stored, err = db.DashboardByGrafanaID(context.Background(), imported.DashboardID)
	require.NoError(t, err)
	assert.Contains(t, string(stored.Grafana), `"version":3`)
}

func nativeDashboardPluginArchive(t *testing.T, version string, include bool) []byte {
	t.Helper()
	includes := `[]`
	if include {
		includes = `[{"type":"dashboard","name":"Overview","path":"dashboards/overview.json"}]`
	}
	files := map[string]string{
		"native-dashboards-panel/plugin.json":              `{"id":"native-dashboards-panel","name":"Native dashboards","type":"panel","info":{"version":"` + version + `"},"dependencies":{"grafanaDependency":">=13"},"includes":` + includes + `}`,
		"native-dashboards-panel/module.js":                "System.register([],function(){return{execute:function(){}}})",
		"native-dashboards-panel/dashboards/overview.json": `{"uid":"plugin-overview","title":"Plugin overview","revision":7,"__inputs":[{"name":"DS","type":"datasource","pluginId":"prometheus"}],"panels":[{"id":1,"type":"stat","title":"Panel","datasource":{"type":"prometheus","uid":"${DS}"},"targets":[{"expr":"vector(233)"}]}],"future":9007199254740993}`,
		"native-dashboards-panel/private.json":             `{"title":"Undeclared private dashboard","panels":[]}`,
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for name, value := range files {
		file, err := archive.Create(name)
		require.NoError(t, err)
		_, err = io.WriteString(file, value)
		require.NoError(t, err)
	}
	require.NoError(t, archive.Close())
	return buffer.Bytes()
}

func TestNativeGrafanaVerifiedPluginIncludesImportAndPluginProtection(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "plugins.db"))
	require.NoError(t, err)
	app := server.New(db, "", 30)
	t.Cleanup(func() { app.Plugins.Close(); app.Live.Close(); db.DB.Close() })
	app.Plugins.AllowUnsigned["native-dashboards-panel"] = true
	_, err = app.Plugins.Install(context.Background(), nativeDashboardPluginArchive(t, "1.0.0", true), "")
	require.NoError(t, err)
	handler := app.Handler()
	w := call(handler, "GET", "/api/plugins/native-dashboards-panel/dashboards", "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var list []nativeImportDescriptor
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list, 1)
	assert.False(t, list[0].Imported)
	assert.Equal(t, int64(7), list[0].Revision)
	request := `{"pluginId":"native-dashboards-panel","path":"dashboards/overview.json","dashboard":{"title":"Ignored by plugin precedence"},"inputs":[{"name":"*","type":"datasource","pluginId":"prometheus","value":"metricspanel"}]}`
	w = call(handler, "POST", "/api/dashboards/interpolate", request, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"title":"Plugin overview"`)
	assert.NotContains(t, w.Body.String(), "Ignored by plugin")
	w = call(handler, "POST", "/api/dashboards/import", request, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var imported nativeImportDescriptor
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &imported))
	assert.Equal(t, "native-dashboards-panel", imported.PluginID)
	assert.Equal(t, "dashboards/overview.json", imported.Path)
	assert.Equal(t, int64(7), imported.ImportedRevision)
	assert.Positive(t, imported.DashboardID)
	stored, err := db.DashboardByGrafanaID(context.Background(), imported.DashboardID)
	require.NoError(t, err)
	assert.Equal(t, imported.PluginID, stored.PluginID)
	assert.Equal(t, imported.Path, stored.PluginPath)
	assert.Contains(t, string(stored.Grafana), "9007199254740993")
	manual := `{"dashboard":{"uid":"plugin-overview","version":1,"title":"Manual edit","panels":[]}}`
	w = call(handler, "POST", "/api/dashboards/db", manual, "", "")
	assert.Equal(t, 412, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"status":"plugin-dashboard"`)
	assert.Contains(t, w.Body.String(), `"pluginId":"native-dashboards-panel"`)
	assert.Equal(t, 412, call(handler, "POST", "/api/dashboards/import", request, "", "").Code)
	w = call(handler, "POST", "/api/dashboards/import", strings.TrimSuffix(request, "}")+`,"overwrite":true}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = call(handler, "GET", "/api/plugins/native-dashboards-panel/dashboards", "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	assert.True(t, list[0].Imported)
	assert.Equal(t, imported.DashboardID, list[0].DashboardID)
	assert.Equal(t, int64(7), list[0].ImportedRevision)
	for _, path := range []string{"private.json", "../plugins.db", "/dashboards/overview.json"} {
		payload, err := json.Marshal(map[string]any{"pluginId": "native-dashboards-panel", "path": path})
		require.NoError(t, err)
		assert.Equal(t, 404, call(handler, "POST", "/api/dashboards/import", string(payload), "", "").Code)
	}
	_, err = app.Plugins.SetEnabled(context.Background(), "native-dashboards-panel", false)
	require.NoError(t, err)
	assert.Equal(t, 500, call(handler, "GET", "/api/plugins/native-dashboards-panel/dashboards", "", "", "").Code)
	_, err = app.Plugins.SetEnabled(context.Background(), "native-dashboards-panel", true)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(app.Plugins.Root, "native-dashboards-panel", "dashboards", "overview.json"), []byte(`{"title":"Changed"}`), 0600))
	w = call(handler, "GET", "/api/plugins/native-dashboards-panel/dashboards", "", "", "")
	assert.Equal(t, 500, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "modified")
	assert.Equal(t, 500, call(handler, "POST", "/api/dashboards/import", request, "", "").Code)
	_, err = app.Plugins.Install(context.Background(), nativeDashboardPluginArchive(t, "1.1.0", false), "")
	require.NoError(t, err)
	w = call(handler, "GET", "/api/plugins/native-dashboards-panel/dashboards", "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list, 1)
	assert.True(t, list[0].Removed)
	assert.Equal(t, imported.DashboardID, list[0].DashboardID)
}

func TestNativeGrafanaLibraryImportAtomicAndReadsCurrentModel(t *testing.T) {
	db, handler := setup(t, "")
	document := `{"uid":"with-library","title":"Shared dashboard","__elements":[{"uid":"native-library","name":"Native library","kind":1,"model":{"id":1,"type":"stat","title":"Library v1","targets":[{"expr":"vector(1)"}]}}],"panels":[{"id":11,"type":"stat","title":"Instance","libraryPanel":{"uid":"native-library","name":"Native library"},"gridPos":{"x":0,"y":0,"w":6,"h":4}}]}`
	w := call(handler, "POST", "/api/dashboards/import", `{"dashboard":`+document+`,"folderUid":"general"}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	library, err := db.LibraryElement(context.Background(), "native-library")
	require.NoError(t, err)
	connections, err := db.LibraryConnections(context.Background(), "native-library")
	require.NoError(t, err)
	require.Len(t, connections, 1)
	assert.Positive(t, connections[0].ConnectionID)
	w = call(handler, "PATCH", "/api/library-elements/native-library", `{"version":`+strconv.FormatInt(library.Version, 10)+`,"model":{"type":"stat","title":"Library v2","targets":[{"expr":"vector(777)"}]}}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = call(handler, "GET", "/api/dashboards/uid/with-library", "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"title":"Library v2"`)
	assert.Contains(t, w.Body.String(), `"expr":"vector(777)"`)
	assert.Contains(t, w.Body.String(), `"id":11`)
	assert.Contains(t, w.Body.String(), `"gridPos":{"h":4,"w":6,"x":0,"y":0}`)
	bad := `{"dashboard":{"uid":"invalid-new","title":"Invalid","__elements":[{"uid":"must-not-leak","name":"Uncommitted","kind":1,"model":{"type":"stat","title":"Uncommitted"}}],"panels":[{"id":1,"title":"Missing type"}]}}`
	assert.Equal(t, 400, call(handler, "POST", "/api/dashboards/import", bad, "", "").Code)
	stale := strings.ReplaceAll(document, "native-library", "must-not-leak")
	assert.Equal(t, 412, call(handler, "POST", "/api/dashboards/import", `{"dashboard":`+stale+`}`, "", "").Code)
	libraries, err := db.LibraryElements(context.Background())
	require.NoError(t, err)
	require.Len(t, libraries, 1)
	assert.Equal(t, "native-library", libraries[0].UID)
}

func TestBoundCommunityImportsChooseRealFolder(t *testing.T) {
	db, _, handler := newCatalogServer(t)
	const token = "catalog-token-233"
	w := call(handler, "POST", "/api/folders", `{"uid":"community-ops","title":"Community operations"}`, token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = call(handler, "POST", "/api/v1/import/grafana-community", `{"id":7362,"folderUid":"community-ops","folderId":999999,"inputs":{"DS_PROM":"metricspanel","PREFIX":"mysql_"}}`, token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var result grafana.ImportResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	assert.Equal(t, "community-ops", result.Dashboard.FolderUID)
	assert.Positive(t, result.Dashboard.GrafanaID)
	stored, err := db.DashboardByGrafanaID(context.Background(), result.Dashboard.GrafanaID)
	require.NoError(t, err)
	assert.Equal(t, "community-ops", stored.FolderUID)
	assert.Contains(t, string(stored.Grafana), `"__inputs"`, "the native bound importer retains input declarations for re-export")
}

func TestNativeGrafanaV2LibrarySourcePreservesEnvelopeAndVersion(t *testing.T) {
	db, handler := setup(t, "")
	source := `{"apiVersion":"dashboard.grafana.app/v2beta1","kind":"Dashboard","metadata":{"name":"v2-library-source","future":{"integer":9007199254740993}},"spec":{"title":"V2 source","future":{"retained":true},"__elements":{"v2-native-library":{"uid":"v2-native-library","name":"V2 native library","kind":1,"model":{"type":"stat","title":"Shared value","targets":[{"expr":"vector(233)"}]}}},"elements":{"shared":{"kind":"LibraryPanel","future":"node retained","spec":{"id":21,"title":"Instance","libraryPanel":{"uid":"v2-native-library","name":"V2 native library"},"opaque":{"instance":233}}}},"layout":{"kind":"GridLayout","spec":{"items":[{"kind":"GridLayoutItem","spec":{"x":0,"y":0,"width":6,"height":4,"element":{"kind":"ElementReference","name":"shared"}}}]}}}}`
	w := call(handler, "POST", "/api/dashboards/import", `{"dashboard":`+source+`}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var descriptor nativeImportDescriptor
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &descriptor))
	stored, err := db.DashboardByGrafanaID(context.Background(), descriptor.DashboardID)
	require.NoError(t, err)
	for _, field := range []string{`"apiVersion":"dashboard.grafana.app/v2beta1"`, `"future":"node retained"`, `"opaque":{"instance":233}`, `"integer":9007199254740993`, `"kind":"LibraryPanel"`, `"generation":1`} {
		assert.Contains(t, string(stored.Grafana), field)
	}
	assert.NotContains(t, string(stored.Grafana), `"__elements"`)
	w = call(handler, "GET", "/api/dashboards/uid/v2-library-source", "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"version":1`)
	assert.Contains(t, w.Body.String(), `"expr":"vector(233)"`)
	w = call(handler, "POST", "/api/dashboards/db", `{"dashboard":`+string(stored.Grafana)+`}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"version":2`)
	stored, err = db.DashboardByGrafanaID(context.Background(), descriptor.DashboardID)
	require.NoError(t, err)
	assert.Contains(t, string(stored.Grafana), `"generation":2`)
	assert.Contains(t, string(stored.Grafana), `"future":"node retained"`)
	w = call(handler, "GET", "/api/dashboards/uid/v2-library-source", "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"version":2`)
}

func TestNativeGrafanaVersionNumbersKeepIntegerPrecision(t *testing.T) {
	_, handler := setup(t, "")
	imported := call(handler, "POST", "/api/v1/import/grafana", `{"uid":"wide-version","version":9007199254740993,"title":"Wide version","panels":[{"type":"text","title":"Panel"}]}`, "", "")
	require.Equal(t, 200, imported.Code, imported.Body.String())
	w := call(handler, "POST", "/api/dashboards/db", `{"dashboard":{"uid":"wide-version","version":9007199254740993,"title":"Next version","panels":[]}}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"version":9007199254740994`)
	w = call(handler, "GET", "/api/dashboards/uid/wide-version", "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"version":9007199254740994`)
}

func TestNativeGrafanaSearchIncludesFoldersAndNativeDashboards(t *testing.T) {
	db, handler := setup(t, "")
	ctx := context.Background()
	parent, err := db.CreateFolder(ctx, model.Folder{UID: "search-ops", Title: "Search Operations"})
	require.NoError(t, err)
	child, err := db.CreateFolder(ctx, model.Folder{UID: "search-child", Title: "Search Child", ParentUID: parent.UID})
	require.NoError(t, err)
	native, err := db.SaveDashboard(ctx, model.Dashboard{ID: "search-native", Name: "Search Native", FolderUID: parent.UID, Panels: []model.Panel{{ID: "native-panel", Title: "Requests", Metric: "http_requests_total", Aggregation: "last"}}})
	require.NoError(t, err)
	_, err = db.SaveDashboard(ctx, model.Dashboard{ID: "search-nested", Name: "Search Nested", FolderUID: child.UID, Panels: []model.Panel{}})
	require.NoError(t, err)
	var hits []struct {
		ID        int64  `json:"id"`
		UID       string `json:"uid"`
		Title     string `json:"title"`
		Type      string `json:"type"`
		FolderID  int64  `json:"folderId"`
		FolderUID string `json:"folderUid"`
	}
	search := func(path string) {
		t.Helper()
		w := call(handler, "GET", path, "", "", "")
		require.Equal(t, 200, w.Code, w.Body.String())
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &hits))
	}
	search("/api/search?query=SEARCH&folderUIDs=" + parent.UID)
	require.Len(t, hits, 2)
	assert.Equal(t, child.UID, hits[0].UID)
	assert.Equal(t, "dash-folder", hits[0].Type)
	assert.Equal(t, child.ID, hits[0].ID)
	assert.Equal(t, parent.ID, hits[0].FolderID)
	assert.Equal(t, native.ID, hits[1].UID)
	assert.Equal(t, "dash-db", hits[1].Type)
	assert.Equal(t, native.GrafanaID, hits[1].ID)
	search("/api/search?type=dash-folder&folderUIDs=general")
	require.Len(t, hits, 1)
	assert.Equal(t, parent.UID, hits[0].UID)
	search("/api/search?type=dash-db&dashboardUIDs=" + native.ID)
	require.Len(t, hits, 1)
	assert.Equal(t, native.GrafanaID, hits[0].ID)
	search("/api/search?type=dash-db&dashboardUIDs=system")
	require.Len(t, hits, 1)
	assert.Positive(t, hits[0].ID)
	search("/api/search?query=search&type=dash-folder&limit=1&page=2")
	require.Len(t, hits, 1)
	assert.Equal(t, parent.UID, hits[0].UID)
	w := call(handler, "GET", "/api/dashboards/uid/"+native.ID, "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"title":"Search Native"`)
	assert.Contains(t, w.Body.String(), `"expr":"http_requests_total"`)
	assert.Equal(t, 422, call(handler, "GET", "/api/search?limit=5001", "", "", "").Code)
	assert.Equal(t, 400, call(handler, "GET", "/api/search?dashboardIds=1&dashboardUIDs=system", "", "", "").Code)
}

func TestNativeGrafanaReadsAndSavesNativePrometheusPanels(t *testing.T) {
	db, handler := setup(t, "")
	w := call(handler, "GET", "/api/dashboards/uid/system", "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var response struct {
		Dashboard json.RawMessage `json:"dashboard"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	var classic struct {
		ID     int64 `json:"id"`
		Panels []struct {
			ID         int    `json:"id"`
			Type       string `json:"type"`
			Datasource struct {
				UID  string `json:"uid"`
				Type string `json:"type"`
			} `json:"datasource"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
			FieldConfig struct {
				Defaults struct {
					Unit string `json:"unit"`
				} `json:"defaults"`
			} `json:"fieldConfig"`
		} `json:"panels"`
	}
	require.NoError(t, json.Unmarshal(response.Dashboard, &classic))
	require.Len(t, classic.Panels, 4)
	assert.Positive(t, classic.ID)
	assert.Equal(t, "metricspanel_memory_bytes", classic.Panels[0].Targets[0].Expr)
	assert.Equal(t, "bytes", classic.Panels[0].FieldConfig.Defaults.Unit)
	assert.Equal(t, "rate(metricspanel_http_requests_total[$__rate_interval])", classic.Panels[2].Targets[0].Expr)
	assert.Equal(t, "ops", classic.Panels[2].FieldConfig.Defaults.Unit)
	for i, panel := range classic.Panels {
		assert.Equal(t, i+1, panel.ID)
		assert.Equal(t, "timeseries", panel.Type)
		assert.Equal(t, "metricspanel", panel.Datasource.UID)
		assert.Equal(t, "prometheus", panel.Datasource.Type)
	}
	panels := []model.Panel{}
	for _, aggregation := range []string{"last", "rate", "avg", "sum", "min", "max"} {
		panels = append(panels, model.Panel{ID: aggregation, Title: aggregation, Metric: "http_requests_total", Labels: map[string]string{"job": "go\"service", "instance": "one"}, Aggregation: aggregation})
	}
	panels = append(panels, model.Panel{ID: "expression", Title: "expression", Metric: "unused", Expr: "vector(233)", Expressions: []string{"vector(1)", "vector(2)"}, Aggregation: "last", Unit: "seconds", Visualization: "stat"})
	d, err := db.SaveDashboard(context.Background(), model.Dashboard{ID: "native-roundtrip", Name: "Native roundtrip", Panels: panels})
	require.NoError(t, err)
	w = call(handler, "GET", "/api/dashboards/uid/"+d.ID, "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.NoError(t, json.Unmarshal(response.Dashboard, &classic))
	require.Len(t, classic.Panels, 7)
	selector := `http_requests_total{instance="one",job="go\"service"}`
	assert.Equal(t, selector, classic.Panels[0].Targets[0].Expr)
	assert.Equal(t, "rate("+selector+"[$__rate_interval])", classic.Panels[1].Targets[0].Expr)
	for i, aggregation := range []string{"avg", "sum", "min", "max"} {
		assert.Equal(t, aggregation+"("+selector+")", classic.Panels[i+2].Targets[0].Expr)
	}
	assert.Equal(t, "vector(1)", classic.Panels[6].Targets[0].Expr)
	assert.Equal(t, "vector(2)", classic.Panels[6].Targets[1].Expr)
	assert.Equal(t, "stat", classic.Panels[6].Type)
	assert.Equal(t, "s", classic.Panels[6].FieldConfig.Defaults.Unit)
	w = call(handler, "POST", "/api/dashboards/db", `{"dashboard":`+string(response.Dashboard)+`}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"version":2`)
	stored, err := db.DashboardByGrafanaID(context.Background(), d.GrafanaID)
	require.NoError(t, err)
	assert.Equal(t, d.ID, stored.ID)
	require.Len(t, stored.Panels, 7)
	assert.Equal(t, selector, stored.Panels[0].Expr)
	assert.Equal(t, "vector(1)", stored.Panels[6].Expressions[0])
	assert.Equal(t, "vector(2)", stored.Panels[6].Expressions[1])
}
