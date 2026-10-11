package server_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reviewHistory(t *testing.T, db *store.Store, id string) model.DashboardVersionList {
	t.Helper()
	list, err := db.DashboardVersions(context.Background(), id, 1000, 0, "")
	require.NoError(t, err)
	return list
}

func TestDashboardHistoryReviewRestoreKeepsCurrentResourceMetadataAndIdentity(t *testing.T) {
	for _, schema := range []string{"v1beta1", "v2beta1"} {
		for _, native := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/native=%t", schema, native), func(t *testing.T) {
				db, handler := setup(t, "")
				ctx := context.Background()
				for _, uid := range []string{"historical-folder", "current-folder"} {
					_, err := db.CreateFolder(ctx, model.Folder{UID: uid, Title: uid})
					require.NoError(t, err)
				}
				oldSpec, currentSpec := `{"title":"Original","uid":"historical-uid","id":991,"version":1,"panels":[],"opaque":900719925474099312345}`, `{"title":"Changed","uid":"current-uid","id":992,"version":2,"panels":[],"opaque":900719925474099312346}`
				if schema == "v2beta1" {
					oldSpec, currentSpec = `{"title":"Original","elements":{},"layout":{"kind":"GridLayout","spec":{"items":[]}},"opaque":900719925474099312345}`, `{"title":"Changed","elements":{},"layout":{"kind":"GridLayout","spec":{"items":[]}},"opaque":900719925474099312346}`
				}
				old := fmt.Sprintf(`{"apiVersion":"dashboard.grafana.app/%s","kind":"Dashboard","metadata":{"name":"historical-uid","uid":"old-resource-uuid","generation":1,"opaque":{"owner":"historical","big":900719925474099312345},"annotations":{"grafana.app/folder":"historical-folder","grafana.app/managedBy":"plugin","grafana.app/managerId":"historical-plugin","custom.example/owner":"historical"}},"spec":%s}`, schema, oldSpec)
				currentAPIVersion := "v1"
				if schema == "v2beta1" {
					currentAPIVersion = "v2"
				}
				current := fmt.Sprintf(`{"apiVersion":"dashboard.grafana.app/%s","kind":"Dashboard","metadata":{"name":"current-uid","uid":"current-resource-uuid","generation":2,"opaque":{"owner":"current","big":18446744073709551615},"annotations":{"grafana.app/folder":"current-folder","grafana.app/managedBy":"plugin","grafana.app/managerId":"current-plugin","custom.example/owner":"current"}},"spec":%s}`, currentAPIVersion, currentSpec)
				d := model.Dashboard{ID: "restore-review", Name: "Original", FolderUID: "historical-folder", PluginID: "historical-plugin", PluginPath: "old.json", Grafana: json.RawMessage(old), Panels: []model.Panel{}}
				var err error
				d, err = db.SaveDashboard(ctx, d)
				require.NoError(t, err)
				stableID := d.GrafanaID
				d.Name, d.FolderUID, d.PluginID, d.PluginPath, d.Grafana = "Changed", "current-folder", "current-plugin", "current.json", json.RawMessage(current)
				d, err = db.SaveDashboard(ctx, d)
				require.NoError(t, err)
				path, body := "/api/dashboards/uid/current-uid/restore", `{"version":1}`
				if native {
					path, body = "/api/v1/dashboard-restore/"+d.ID, fmt.Sprintf(`{"version":"1","revision":%d}`, d.UpdatedAt)
				}
				w := call(handler, "POST", path, body, "", "")
				require.Equal(t, 200, w.Code, w.Body.String())
				restored, err := db.Dashboard(ctx, d.ID)
				require.NoError(t, err)
				assert.Equal(t, "current-uid", restored.UID())
				assert.Equal(t, stableID, restored.GrafanaID)
				assert.Equal(t, d.ID, restored.ID)
				assert.Equal(t, "current-folder", restored.FolderUID)
				assert.Equal(t, "current-plugin", restored.PluginID)
				assert.Equal(t, "current.json", restored.PluginPath)
				assert.Equal(t, "Original", restored.Name)
				assert.Equal(t, int64(3), restored.GrafanaVersion)
				var resource struct {
					APIVersion string `json:"apiVersion"`
					Metadata   struct {
						Name        string            `json:"name"`
						UID         string            `json:"uid"`
						Generation  int64             `json:"generation"`
						Opaque      json.RawMessage   `json:"opaque"`
						Annotations map[string]string `json:"annotations"`
					} `json:"metadata"`
					Spec json.RawMessage `json:"spec"`
				}
				require.NoError(t, json.Unmarshal(restored.Grafana, &resource))
				assert.Equal(t, "dashboard.grafana.app/"+schema, resource.APIVersion, "restore must retain the historical spec's actual schema version")
				assert.Equal(t, "current-uid", resource.Metadata.Name)
				assert.Equal(t, "current-resource-uuid", resource.Metadata.UID, "restore spec, retaining current resource identity and ownership")
				assert.Equal(t, int64(3), resource.Metadata.Generation)
				assert.JSONEq(t, `{"owner":"current","big":18446744073709551615}`, string(resource.Metadata.Opaque))
				assert.Contains(t, string(resource.Metadata.Opaque), "18446744073709551615", "unknown metadata numbers must retain their exact JSON value")
				assert.Equal(t, "current", resource.Metadata.Annotations["custom.example/owner"])
				assert.Equal(t, restored.FolderUID, resource.Metadata.Annotations["grafana.app/folder"])
				assert.Equal(t, restored.PluginID, resource.Metadata.Annotations["grafana.app/managerId"])
				assert.Contains(t, string(resource.Spec), "900719925474099312345")
				assert.NotContains(t, string(resource.Spec), "900719925474099312346")
				if schema == "v1beta1" {
					var spec struct {
						ID      int64  `json:"id"`
						UID     string `json:"uid"`
						Version int64  `json:"version"`
					}
					require.NoError(t, json.Unmarshal(resource.Spec, &spec))
					assert.Equal(t, stableID, spec.ID)
					assert.Equal(t, "current-uid", spec.UID)
					assert.Equal(t, int64(3), spec.Version)
				}
				versions := reviewHistory(t, db, d.ID)
				require.Len(t, versions.Versions, 3)
				assert.Equal(t, int64(1), versions.Versions[0].RestoredFrom)
				assert.Equal(t, int64(2), versions.Versions[0].ParentVersion)
				historical, err := db.DashboardVersion(ctx, d.ID, 1)
				require.NoError(t, err)
				assert.JSONEq(t, old, string(historical.Snapshot.Grafana), "restore must not overwrite history")
				w = call(handler, "GET", "/api/dashboards/uid/current-uid", "", "", "")
				require.Equal(t, 200, w.Code, w.Body.String())
				assert.Contains(t, w.Body.String(), `"folderUid":"current-folder"`)
				assert.Contains(t, w.Body.String(), `"pluginId":"current-plugin"`)
			})
		}
	}
}

func TestDashboardHistoryReviewLegacyIdenticalRestoreReturns400WithoutSave(t *testing.T) {
	db, handler := setup(t, "")
	ctx := context.Background()
	d := model.Dashboard{ID: "identical-review", Name: "Same", Panels: []model.Panel{}, Grafana: json.RawMessage(`{"apiVersion":"dashboard.grafana.app/v1beta1","metadata":{"name":"same-review","generation":1,"annotations":{"owner":"old"}},"spec":{"uid":"same-review","id":111,"version":1,"title":"Same","panels":[],"opaque":1e10000}}`)}
	var err error
	d, err = db.SaveDashboard(ctx, d)
	require.NoError(t, err)
	d.Grafana = json.RawMessage(`{"apiVersion":"dashboard.grafana.app/v1beta1","metadata":{"name":"same-review","generation":2,"annotations":{"owner":"current"}},"spec":{"uid":"same-review","id":222,"version":2,"title":"Same","panels":[],"opaque":10e9999}}`)
	d, err = db.SaveDashboard(ctx, d)
	require.NoError(t, err)
	w := call(handler, "POST", "/api/dashboards/uid/same-review/restore", `{"version":1}`, "", "")
	require.Equal(t, 400, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "identical")
	after, err := db.Dashboard(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, d, after)
	assert.Len(t, reviewHistory(t, db, d.ID).Versions, 2)
}

func TestDashboardHistoryReviewMissingLibraryRestoreIsAtomic(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native=%t", native), func(t *testing.T) {
			db, handler := setup(t, "")
			ctx := context.Background()
			shared, err := db.SaveLibraryElement(ctx, model.LibraryElement{UID: "shared-review", Name: "Shared", Kind: 1, Model: json.RawMessage(`{"type":"text","title":"Current shared model","options":{"content":"current"},"opaque":18446744073709551615}`)}, true)
			require.NoError(t, err)
			_, err = db.SaveLibraryElement(ctx, model.LibraryElement{UID: "missing-review", Name: "Later deleted", Kind: 1, Model: json.RawMessage(`{"type":"text","title":"Deleted model"}`)}, true)
			require.NoError(t, err)
			source := `{"uid":"library-history-review","title":"Historical linked","version":1,"__elements":[{"uid":"shared-review","name":"Shared","kind":1,"model":{"type":"text","title":"Historical replacement","options":{"content":"must not overwrite"}}},{"uid":"unused-review","name":"Unused","kind":1,"model":{"type":"text","title":"Must not leak"}}],"panels":[{"id":1,"type":"text","title":"Shared","libraryPanel":{"uid":"shared-review"}},{"id":2,"type":"text","title":"Deleted","libraryPanel":{"uid":"missing-review"}}]}`
			d := model.Dashboard{ID: "missing-library-review", Name: "Historical linked", Grafana: json.RawMessage(source), Panels: []model.Panel{
				{ID: "shared-panel", Title: "Shared", Visualization: "text", Aggregation: "last", Config: json.RawMessage(`{"id":1,"type":"text","title":"Shared","libraryPanel":{"uid":"shared-review"}}`)},
				{ID: "deleted-panel", Title: "Deleted", Visualization: "text", Aggregation: "last", Config: json.RawMessage(`{"id":2,"type":"text","title":"Deleted","libraryPanel":{"uid":"missing-review"}}`)},
			}}
			d, err = db.SaveDashboard(ctx, d)
			require.NoError(t, err)
			d.Name, d.Panels, d.Grafana = "Current detached", []model.Panel{}, json.RawMessage(`{"uid":"library-history-review","title":"Current detached","version":2,"panels":[]}`)
			d, err = db.SaveDashboard(ctx, d)
			require.NoError(t, err)
			require.NoError(t, db.DeleteLibraryElement(ctx, "missing-review"))
			path, body := "/api/dashboards/uid/library-history-review/restore", `{"version":1}`
			if native {
				path, body = "/api/v1/dashboard-restore/"+d.ID, fmt.Sprintf(`{"version":"1","revision":%d}`, d.UpdatedAt)
			}
			w := call(handler, "POST", path, body, "", "")
			require.Equal(t, 404, w.Code, w.Body.String())
			after, err := db.Dashboard(ctx, d.ID)
			require.NoError(t, err)
			assert.Equal(t, d, after)
			assert.Len(t, reviewHistory(t, db, d.ID).Versions, 2)
			currentShared, err := db.LibraryElement(ctx, shared.UID)
			require.NoError(t, err)
			assert.Equal(t, shared, currentShared, "failed restore cannot overwrite a shared model or change its version")
			_, err = db.LibraryElement(ctx, "unused-review")
			assert.ErrorIs(t, err, sql.ErrNoRows, "failed restore must not leave newly defined library rows")
			connections, err := db.LibraryConnections(ctx, shared.UID)
			require.NoError(t, err)
			assert.Empty(t, connections)
			// Once the missing definition is available again, the historical
			// declaration must still not replace an existing shared library model.
			_, err = db.SaveLibraryElement(ctx, model.LibraryElement{UID: "missing-review", Name: "Available again", Kind: 1, Model: json.RawMessage(`{"type":"text","title":"Current available model"}`)}, true)
			require.NoError(t, err)
			w = call(handler, "POST", path, body, "", "")
			require.Equal(t, 200, w.Code, w.Body.String())
			currentShared, err = db.LibraryElement(ctx, shared.UID)
			require.NoError(t, err)
			assert.Equal(t, shared.Version, currentShared.Version)
			assert.Equal(t, shared.Model, currentShared.Model, "successful restore resolves the current shared model rather than replaying its historical declaration")
			assert.Equal(t, shared.Meta.Updated, currentShared.Meta.Updated)
			recovered, err := db.Dashboard(ctx, d.ID)
			require.NoError(t, err)
			require.Len(t, recovered.Panels, 2)
			assert.Equal(t, "Current shared model", recovered.Panels[0].Title)
			assert.Equal(t, "Current available model", recovered.Panels[1].Title)
			assert.Len(t, reviewHistory(t, db, d.ID).Versions, 3)
		})
	}
}

func TestDashboardHistoryReviewWideVersionKeysAndExactDiff(t *testing.T) {
	db, handler := setup(t, "")
	ctx := context.Background()
	const firstVersion int64 = 9007199254740993
	d := model.Dashboard{ID: "wide-review", Name: "Wide", Panels: []model.Panel{}, Grafana: json.RawMessage(`{"uid":"wide-review","title":"Wide","version":9007199254740993,"panels":[],"a/b~c":9007199254740993,"array":[null,1],"equal":1e10000,"zero":-0}`)}
	var err error
	d, err = db.SaveDashboard(ctx, d)
	require.NoError(t, err)
	assert.Equal(t, firstVersion, d.GrafanaVersion)
	d.Grafana = json.RawMessage(`{"uid":"wide-review","title":"Wide","version":9007199254740994,"panels":[],"a/b~c":9007199254740994,"array":[233],"equal":10e9999,"zero":0.00,"added":null}`)
	d, err = db.SaveDashboard(ctx, d)
	require.NoError(t, err)
	w := call(handler, "GET", "/api/v1/dashboard-versions/"+d.ID, "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"currentVersionKey":"9007199254740994"`)
	assert.Contains(t, w.Body.String(), `"versionKey":"9007199254740993"`)
	w = call(handler, "GET", "/api/dashboards/uid/wide-review/versions/"+strconv.FormatInt(firstVersion, 10), "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"version":9007199254740993`)
	w = call(handler, "POST", "/api/v1/dashboard-compare/"+d.ID, `{"baseVersion":"9007199254740993","newVersion":"9007199254740994"}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var comparison struct {
		BaseVersion string `json:"baseVersionKey"`
		NewVersion  string `json:"newVersionKey"`
		Changes     []struct {
			Path      string          `json:"path"`
			Operation string          `json:"operation"`
			Before    json.RawMessage `json:"before"`
			After     json.RawMessage `json:"after"`
		} `json:"changes"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &comparison))
	assert.Equal(t, "9007199254740993", comparison.BaseVersion)
	assert.Equal(t, "9007199254740994", comparison.NewVersion)
	require.Len(t, comparison.Changes, 4)
	assert.Equal(t, "/a~1b~0c", comparison.Changes[0].Path)
	assert.Equal(t, "9007199254740993", string(comparison.Changes[0].Before))
	assert.Equal(t, "9007199254740994", string(comparison.Changes[0].After))
	assert.Equal(t, "add", comparison.Changes[1].Operation)
	assert.Equal(t, "null", string(comparison.Changes[1].After))
	assert.Equal(t, "null", string(comparison.Changes[2].Before))
	assert.Equal(t, "remove", comparison.Changes[3].Operation)
	assert.Equal(t, "1", string(comparison.Changes[3].Before))
	w = call(handler, "POST", "/api/v1/dashboard-restore/"+d.ID, fmt.Sprintf(`{"version":"9007199254740993","revision":%d}`, d.UpdatedAt), "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	restored, err := db.Dashboard(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, firstVersion+2, restored.GrafanaVersion)
	assert.Contains(t, string(restored.Grafana), `"version":9007199254740995`)
	assert.Contains(t, string(restored.Grafana), `"a/b~c":9007199254740993`)
	latest := reviewHistory(t, db, d.ID).Versions[0]
	assert.Equal(t, firstVersion, latest.RestoredFrom)
	assert.Equal(t, firstVersion+1, latest.ParentVersion)
}

func TestDashboardHistoryReviewImportedRuntimeEditsRemainRestorable(t *testing.T) {
	for _, field := range []string{"panels", "variables", "annotations"} {
		t.Run(field, func(t *testing.T) {
			db, handler := setup(t, "")
			w := call(handler, "POST", "/api/v1/import/grafana", `{"uid":"runtime-review","title":"Runtime","version":1,"panels":[{"id":1,"type":"text","title":"Original panel","options":{"content":"original"}}],"templating":{"list":[{"name":"region","type":"custom","query":"east,west","current":{"text":"east","value":"east"}}]},"annotations":{"list":[{"name":"Events","enable":true,"datasource":{"type":"grafana","uid":"grafana"},"query":{"type":"tags","tags":["original"]}}]}}`, "", "")
			require.Equal(t, 200, w.Code, w.Body.String())
			var imported struct {
				Dashboard model.Dashboard `json:"dashboard"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &imported))
			d := imported.Dashboard
			switch field {
			case "panels":
				require.Len(t, d.Panels, 1)
				d.Panels[0].Title = "Changed panel"
				d.Panels[0].Config = json.RawMessage(`{"id":1,"type":"text","title":"Changed panel","options":{"content":"changed"}}`)
			case "variables":
				require.Len(t, d.Variables, 1)
				d.Variables[0].Current = "west"
				d.Variables[0].Config = json.RawMessage(`{"name":"region","type":"custom","query":"east,west","current":{"text":"west","value":"west"}}`)
			case "annotations":
				d.Annotations = json.RawMessage(`[{"name":"Events","enable":true,"datasource":{"type":"grafana","uid":"grafana"},"query":{"type":"tags","tags":["changed"]}}]`)
			}
			body, err := json.Marshal(d)
			require.NoError(t, err)
			w = call(handler, "PUT", "/api/v1/dashboards/"+d.ID, string(body), "", "")
			require.Equal(t, 200, w.Code, w.Body.String())
			current, err := db.Dashboard(context.Background(), d.ID)
			require.NoError(t, err)
			w = call(handler, "POST", "/api/v1/dashboard-compare/"+d.ID, `{"baseVersion":"1","newVersion":"2"}`, "", "")
			require.Equal(t, 200, w.Code, w.Body.String())
			var comparison struct {
				Changes []json.RawMessage `json:"changes"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &comparison))
			assert.NotEmpty(t, comparison.Changes, "accepted native runtime changes cannot disappear from version comparison")
			w = call(handler, "POST", "/api/v1/dashboard-restore/"+d.ID, fmt.Sprintf(`{"version":"1","revision":%d}`, current.UpdatedAt), "", "")
			require.Equal(t, 200, w.Code, w.Body.String())
			restored, err := db.Dashboard(context.Background(), d.ID)
			require.NoError(t, err)
			assert.Equal(t, "Original panel", restored.Panels[0].Title)
			assert.Equal(t, "east", restored.Variables[0].Current)
			assert.Equal(t, imported.Dashboard.Annotations, restored.Annotations)
		})
	}
}

func TestDashboardHistoryReviewIdenticalClassicSaveIgnoresGeneratedPanelIDs(t *testing.T) {
	db, handler := setup(t, "")
	for version := 0; version < 2; version++ {
		w := call(handler, "POST", "/api/dashboards/db", fmt.Sprintf(`{"dashboard":{"uid":"same-panel-review","title":"Same panel","version":%d,"panels":[{"id":1,"type":"text","title":"Same","options":{"content":"same"}}]}}`, version), "", "")
		require.Equal(t, 200, w.Code, w.Body.String())
	}
	var current model.Dashboard
	dashboards, err := db.Dashboards(context.Background())
	require.NoError(t, err)
	for _, d := range dashboards {
		if d.UID() == "same-panel-review" {
			current = d
		}
	}
	require.NotEmpty(t, current.ID)
	first, err := db.DashboardVersion(context.Background(), current.ID, 1)
	require.NoError(t, err)
	assert.NotEqual(t, first.Snapshot.Panels[0].ID, current.Panels[0].ID, "fixture proves repeated importer generated new native panel IDs")
	w := call(handler, "POST", "/api/v1/dashboard-compare/"+current.ID, `{"baseVersion":"1","newVersion":"2"}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"changes":[]`)
	w = call(handler, "POST", "/api/dashboards/uid/same-panel-review/restore", `{"version":1}`, "", "")
	require.Equal(t, 400, w.Code, w.Body.String())
	assert.Len(t, reviewHistory(t, db, current.ID).Versions, 2)
}

func TestDashboardHistoryReviewLibraryCachedModelChangeIsNotRestorableSpecChange(t *testing.T) {
	db, handler := setup(t, "")
	w := call(handler, "POST", "/api/library-elements", `{"uid":"cache-review","name":"Old shared name","kind":1,"model":{"type":"text","title":"Old shared title","options":{"content":"old"}}}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = call(handler, "POST", "/api/dashboards/db", `{"dashboard":{"uid":"cache-history-review","title":"Shared dashboard","version":0,"panels":[{"id":1,"type":"text","title":"Old shared title","options":{"content":"old"},"gridPos":{"x":0,"y":0,"w":24,"h":8},"libraryPanel":{"uid":"cache-review","name":"Old shared name"}}]}}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = call(handler, "PATCH", "/api/library-elements/cache-review", `{"version":1,"name":"New shared name","model":{"type":"text","title":"New shared title","options":{"content":"new"}}}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = call(handler, "GET", "/api/dashboards/uid/cache-history-review", "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var read struct {
		Dashboard json.RawMessage `json:"dashboard"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &read))
	assert.Contains(t, string(read.Dashboard), "New shared title")
	w = call(handler, "POST", "/api/dashboards/db", `{"dashboard":`+string(read.Dashboard)+`}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var current model.Dashboard
	dashboards, err := db.Dashboards(context.Background())
	require.NoError(t, err)
	for _, d := range dashboards {
		if d.UID() == "cache-history-review" {
			current = d
		}
	}
	require.NotEmpty(t, current.ID)
	w = call(handler, "POST", "/api/v1/dashboard-compare/"+current.ID, `{"baseVersion":"1","newVersion":"2"}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"changes":[]`, "library title/options/name are hydrated shared-model caches, while instance placement and UID are unchanged")
	w = call(handler, "POST", "/api/dashboards/uid/cache-history-review/restore", `{"version":1}`, "", "")
	require.Equal(t, 400, w.Code, w.Body.String())
	assert.Len(t, reviewHistory(t, db, current.ID).Versions, 2)
}

func TestDashboardHistoryReviewV2LibraryKeepsInstanceOpaqueChanges(t *testing.T) {
	db, handler := setup(t, "")
	w := call(handler, "POST", "/api/v1/import/grafana", `{"apiVersion":"dashboard.grafana.app/v2beta1","kind":"Dashboard","metadata":{"name":"v2-instance-review","generation":1},"spec":{"title":"V2 instance","__elements":[{"uid":"v2-shared-review","name":"Shared","kind":1,"model":{"type":"text","title":"Shared title","options":{"content":"shared"}}}],"elements":{"p":{"kind":"LibraryPanel","spec":{"id":1,"title":"Shared title","libraryPanel":{"uid":"v2-shared-review","name":"Old display name"},"opaque":900719925474099312345}}},"layout":{"kind":"GridLayout","spec":{"items":[{"kind":"GridLayoutItem","spec":{"x":0,"y":0,"width":24,"height":8,"element":{"kind":"ElementReference","name":"p"}}}]}}}}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var imported struct {
		Dashboard model.Dashboard `json:"dashboard"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &imported))
	d := imported.Dashboard
	setInstance := func(opaque string) {
		var root, spec, elements, node, instance map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(d.Grafana, &root))
		require.NoError(t, json.Unmarshal(root["spec"], &spec))
		require.NoError(t, json.Unmarshal(spec["elements"], &elements))
		require.NoError(t, json.Unmarshal(elements["p"], &node))
		require.NoError(t, json.Unmarshal(node["spec"], &instance))
		instance["title"] = json.RawMessage(`"Fresh cached title"`)
		instance["libraryPanel"] = json.RawMessage(`{"uid":"v2-shared-review","name":"Fresh cached name"}`)
		instance["opaque"] = json.RawMessage(opaque)
		var err error
		node["spec"], err = json.Marshal(instance)
		require.NoError(t, err)
		elements["p"], err = json.Marshal(node)
		require.NoError(t, err)
		spec["elements"], err = json.Marshal(elements)
		require.NoError(t, err)
		root["spec"], err = json.Marshal(spec)
		require.NoError(t, err)
		d.Grafana, err = json.Marshal(root)
		require.NoError(t, err)
		d.Panels[0].Title = "Fresh cached title"
		body, err := json.Marshal(d)
		require.NoError(t, err)
		w = call(handler, "PUT", "/api/v1/dashboards/"+d.ID, string(body), "", "")
		require.Equal(t, 200, w.Code, w.Body.String())
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &d))
	}
	setInstance("900719925474099312345")
	w = call(handler, "POST", "/api/v1/dashboard-compare/"+d.ID, `{"baseVersion":"1","newVersion":"2"}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"changes":[]`, "V2 shared title/name cache does not change the instance")
	setInstance("900719925474099312346")
	w = call(handler, "POST", "/api/v1/dashboard-compare/"+d.ID, `{"baseVersion":"2","newVersion":"3"}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var comparison struct {
		Changes []struct {
			Path   string          `json:"path"`
			Before json.RawMessage `json:"before"`
			After  json.RawMessage `json:"after"`
		} `json:"changes"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &comparison))
	require.Len(t, comparison.Changes, 1)
	assert.Equal(t, "/elements/p/spec/opaque", comparison.Changes[0].Path)
	assert.Equal(t, "900719925474099312345", string(comparison.Changes[0].Before))
	assert.Equal(t, "900719925474099312346", string(comparison.Changes[0].After))
	w = call(handler, "POST", "/api/v1/dashboard-restore/"+d.ID, fmt.Sprintf(`{"version":"2","revision":%d}`, d.UpdatedAt), "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	restored, err := db.Dashboard(context.Background(), d.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(4), restored.GrafanaVersion)
	assert.Contains(t, string(restored.Grafana), "900719925474099312345")
	assert.NotContains(t, string(restored.Grafana), "900719925474099312346")
}
