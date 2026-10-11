package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/require"
)

func TestLibraryPanelsPropagateAndUnlinkWithoutRounding(t *testing.T) {
	s, h := setup(t, "resources")
	token := "resources"
	for _, path := range []string{"/api/v1/folders", "/api/folders", "/api/library-elements", "/api/v1/library-models/load"} {
		require.Equal(t, 401, call(h, "GET", path, "", "", "").Code)
	}
	w := call(h, "POST", "/api/folders", `{"uid":"team","title":"Team"}`, token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = call(h, "POST", "/api/library-elements", `{"uid":"load","name":"Load","kind":1,"folderUid":"team","model":{"type":"text","title":"Shared","options":{"content":"old"},"opaque":900719925474099312345}}`, token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	for _, path := range []string{"/api/library-elements/name/Load", "/api/library-elements/name/connections", "/api/library-elements/load/connections"} {
		require.Equal(t, 200, call(h, "GET", path, "", token, "").Code, path)
	}
	importBody := `{"dashboard":{"uid":"source","title":"Source","panels":[{"id":1,"title":"Text","type":"text","options":{"content":"original"},"opaque":900719925474099312345}],"opaque":900719925474099312345},"uid":"source","folderUid":"team"}`
	w = call(h, "POST", "/api/v1/import/grafana-bound", importBody, token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var result struct {
		Dashboard model.Dashboard `json:"dashboard"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	d := result.Dashboard
	w = call(h, "POST", "/api/v1/dashboard-library/"+d.ID, fmt.Sprintf(`{"libraryUid":"load","revision":%d}`, d.UpdatedAt), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &d))
	require.Len(t, d.Panels, 2)
	linkedID := d.Panels[1].ID
	w = call(h, "GET", "/api/library-elements/load/connections", "", token, "")
	require.Contains(t, w.Body.String(), d.ID)
	w = call(h, "PATCH", "/api/library-elements/load", `{"version":1,"model":{"type":"text","title":"Changed","options":{"content":"new"},"opaque":900719925474099312345}}`, token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = call(h, "GET", "/api/v1/dashboards", "", token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var dashboards []model.Dashboard
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &dashboards))
	for _, current := range dashboards {
		if current.ID == d.ID {
			require.Equal(t, "Changed", current.Panels[1].Title)
			require.Contains(t, string(current.Panels[1].Config), "900719925474099312345")
		}
	}
	w = call(h, "GET", "/api/dashboards/uid/source", "", token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "Changed")
	require.Contains(t, w.Body.String(), "900719925474099312345")
	require.Equal(t, 409, call(h, "DELETE", "/api/library-elements/load", "", token, "").Code)
	w = call(h, "PUT", "/api/v1/dashboard-folders/"+d.ID, fmt.Sprintf(`{"folderUid":"general","revision":%d}`, d.UpdatedAt), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	d = model.Dashboard{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &d))
	require.Empty(t, d.FolderUID)
	require.Equal(t, 409, call(h, "POST", "/api/v1/dashboard-library/"+d.ID+"/unlink", fmt.Sprintf(`{"panelId":%q,"revision":1}`, linkedID), token, "").Code)
	w = call(h, "POST", "/api/v1/dashboard-library/"+d.ID+"/unlink", fmt.Sprintf(`{"panelId":%q,"revision":%d}`, linkedID, d.UpdatedAt), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &d))
	require.NotContains(t, string(d.Panels[1].Config), "libraryPanel")
	require.Equal(t, "Changed", d.Panels[1].Title)
	w = call(h, "GET", "/api/v1/dashboard-exports/"+d.ID+"?format=grafana", "", token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "900719925474099312345")
	require.NotContains(t, w.Body.String(), "libraryPanel")
	require.Equal(t, 200, call(h, "DELETE", "/api/library-elements/load", "", token, "").Code)
	loaded, err := s.Dashboard(context.Background(), d.ID)
	require.NoError(t, err)
	require.Equal(t, d.GrafanaID, loaded.GrafanaID)
	require.Contains(t, string(loaded.Panels[1].Config), "new")
	w = call(h, "POST", "/api/annotations", fmt.Sprintf(`{"dashboardId":%d,"panelId":1,"time":233,"text":"Stable dashboard identity"}`, d.GrafanaID), token, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	for _, numericID := range []int64{d.GrafanaID, int64(crc32.ChecksumIEEE([]byte("source")))} {
		w = call(h, "GET", "/api/annotations?dashboardId="+strconv.FormatInt(numericID, 10), "", token, "")
		require.Equal(t, 200, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), "Stable dashboard identity")
		require.Contains(t, w.Body.String(), `"dashboardUID":"source"`)
	}
}

func TestV2LibraryImportIsAtomicAndRetainsSource(t *testing.T) {
	s, h := setup(t, "")
	body := `{"kind":"Dashboard","metadata":{"name":"v2"},"spec":{"title":"V2 library","__elements":{"lib":{"uid":"lib","name":"Library","kind":1,"model":{"type":"text","title":"Shared V2","options":{"content":"first"},"opaque":900719925474099312345}}},"elements":{"p":{"kind":"LibraryPanel","opaque":900719925474099312345,"spec":{"id":8,"title":"Instance","libraryPanel":{"uid":"lib","name":"Library"},"opaque":900719925474099312345}}},"layout":{"kind":"GridLayout","spec":{"items":[{"kind":"GridLayoutItem","spec":{"x":0,"y":0,"width":24,"height":8,"element":{"kind":"ElementReference","name":"p"}}}]}}}}`
	w := call(h, "POST", "/api/v1/import/grafana", body, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var result struct {
		Dashboard model.Dashboard `json:"dashboard"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	d := result.Dashboard
	require.Len(t, d.Panels, 1)
	e, err := s.LibraryElement(context.Background(), "lib")
	require.NoError(t, err)
	require.Equal(t, int64(1), e.Meta.ConnectedDashboards)
	w = call(h, "GET", "/api/v1/dashboard-exports/"+d.ID+"?format=grafana", "", "", "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"kind":"LibraryPanel"`)
	require.Contains(t, w.Body.String(), `"opaque":900719925474099312345`)
	require.NotContains(t, w.Body.String(), "_metricspanelLibrarySource")
	require.NotContains(t, w.Body.String(), "__elements")
	w = call(h, "PATCH", "/api/library-elements/lib", `{"version":1,"model":{"type":"text","title":"Updated V2","options":{"content":"second"}}}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = call(h, "GET", "/api/v1/dashboards", "", "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "Updated V2")
	w = call(h, "POST", "/api/v1/dashboard-library/"+d.ID+"/unlink", fmt.Sprintf(`{"panelId":%q,"revision":%d}`, d.Panels[0].ID, d.UpdatedAt), "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = call(h, "GET", "/api/v1/dashboard-exports/"+d.ID+"?format=grafana", "", "", "")
	require.NotContains(t, w.Body.String(), "LibraryPanel")
	require.Contains(t, w.Body.String(), `"kind":"Panel"`)
	require.Contains(t, w.Body.String(), "second")
	require.GreaterOrEqual(t, strings.Count(w.Body.String(), `"opaque":900719925474099312345`), 2, "unlink must retain both library-instance and node unknown fields")
	require.Equal(t, 200, call(h, "DELETE", "/api/library-elements/lib", "", "", "").Code)
}

func TestNativeLibraryInsertAllocatesUniquePanelsAndRejectsStaleClassicSave(t *testing.T) {
	_, h := setup(t, "")
	w := call(h, "POST", "/api/library-elements", `{"uid":"reuse","name":"Reuse","kind":1,"model":{"type":"text","title":"Shared","options":{"content":"shared"}}}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = call(h, "POST", "/api/v1/dashboards", `{"name":"Native null config","panels":[{"id":"metric","title":"Metric","metric":"up","aggregation":"last","config":null}]}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var native model.Dashboard
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &native))
	for _, id := range []string{native.ID, "system"} {
		w = call(h, "POST", "/api/v1/dashboard-library/"+id, `{"libraryUid":"reuse"}`, "", "")
		require.Equal(t, 200, w.Code, w.Body.String())
		w = call(h, "GET", "/api/dashboards/uid/"+id, "", "", "")
		require.Equal(t, 200, w.Code, w.Body.String())
		var classic struct {
			Dashboard struct {
				Panels []struct {
					ID   int64 `json:"id"`
					Grid struct {
						Y int64 `json:"y"`
					} `json:"gridPos"`
				} `json:"panels"`
			} `json:"dashboard"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &classic))
		seen := map[int64]bool{}
		for _, p := range classic.Dashboard.Panels {
			require.False(t, seen[p.ID], "classic panel IDs must be unique")
			seen[p.ID] = true
		}
		if id == "system" {
			require.Len(t, classic.Dashboard.Panels, 4)
			require.GreaterOrEqual(t, classic.Dashboard.Panels[3].Grid.Y, int64(16))
		}
	}
	w = call(h, "POST", "/api/dashboards/db", `{"dashboard":{"uid":"versioned","title":"Versioned","panels":[{"id":1,"type":"text","title":"Original","options":{"content":"original"}}]}}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = call(h, "GET", "/api/dashboards/uid/versioned", "", "", "")
	var old struct {
		Dashboard json.RawMessage `json:"dashboard"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &old))
	var oldDoc struct {
		Version int64 `json:"version"`
	}
	require.NoError(t, json.Unmarshal(old.Dashboard, &oldDoc))
	w = call(h, "GET", "/api/v1/dashboards", "", "", "")
	var dashboards []model.Dashboard
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &dashboards))
	var id string
	for _, d := range dashboards {
		if d.UID() == "versioned" {
			id = d.ID
		}
	}
	require.NotEmpty(t, id)
	w = call(h, "POST", "/api/v1/dashboard-library/"+id, `{"libraryUid":"reuse"}`, "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	w = call(h, "GET", "/api/dashboards/uid/versioned", "", "", "")
	var current struct {
		Dashboard struct {
			Version int64             `json:"version"`
			Panels  []json.RawMessage `json:"panels"`
		} `json:"dashboard"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &current))
	require.Equal(t, oldDoc.Version+1, current.Dashboard.Version)
	require.Len(t, current.Dashboard.Panels, 2)
	w = call(h, "POST", "/api/dashboards/db", `{"dashboard":`+string(old.Dashboard)+`}`, "", "")
	require.Equal(t, 412, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "version-mismatch")
}

func TestFolderAPIHierarchyAndVersions(t *testing.T) {
	_, h := setup(t, "")
	for _, body := range []string{`{"uid":"parent","title":"Parent"}`, `{"uid":"child","title":"Child","parentUid":"parent"}`} {
		w := call(h, "POST", "/api/folders", body, "", "")
		require.Equal(t, 200, w.Code, w.Body.String())
	}
	w := call(h, "GET", "/api/folders", "", "", "")
	require.Contains(t, w.Body.String(), "Parent")
	require.NotContains(t, w.Body.String(), "Child")
	w = call(h, "GET", "/api/v1/folders", "", "", "")
	require.Contains(t, w.Body.String(), "Parent")
	require.Contains(t, w.Body.String(), "Child")
	w = call(h, "GET", "/api/folders/child/parents", "", "", "")
	require.Contains(t, w.Body.String(), "Parent")
	require.Equal(t, 400, call(h, "POST", "/api/folders/parent/move", `{"parentUid":"child"}`, "", "").Code)
	require.Equal(t, 412, call(h, "PUT", "/api/folders/child", `{"title":"Changed","version":0}`, "", "").Code)
	require.Equal(t, 200, call(h, "PUT", "/api/folders/child", `{"title":"Changed","version":1}`, "", "").Code)
	require.Equal(t, 200, call(h, "DELETE", "/api/folders/parent", "", "", "").Code)
	require.Equal(t, 404, call(h, "GET", "/api/folders/child", "", "", "").Code)
}
