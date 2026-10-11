package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type historyResourceItem struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name            string            `json:"name"`
		Namespace       string            `json:"namespace"`
		Generation      int64             `json:"generation"`
		ResourceVersion string            `json:"resourceVersion"`
		Annotations     map[string]string `json:"annotations"`
		Opaque          json.RawMessage   `json:"opaque"`
	} `json:"metadata"`
	Spec   json.RawMessage `json:"spec"`
	Status struct {
		Conversion struct {
			Failed        bool   `json:"failed"`
			StoredVersion string `json:"storedVersion"`
		} `json:"conversion"`
	} `json:"status"`
}

type historyResourceList struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Continue string `json:"continue"`
	} `json:"metadata"`
	Items []historyResourceItem `json:"items"`
}

func historyResourcePath(version, uid string, limit int, token string) string {
	q := url.Values{"labelSelector": {"grafana.app/get-history=true"}, "fieldSelector": {"metadata.name=" + uid}, "limit": {strconv.Itoa(limit)}}
	if token != "" {
		q.Set("continue", token)
	}
	return "/apis/dashboard.grafana.app/" + version + "/namespaces/default/dashboards?" + q.Encode()
}

func readHistoryResource(t *testing.T, handler http.Handler, version, uid string, limit int, token string) historyResourceList {
	t.Helper()
	w := call(handler, "GET", historyResourcePath(version, uid, limit, token), "", "catalog-token-233", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var list historyResourceList
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	assert.Equal(t, "dashboard.grafana.app/"+version, list.APIVersion)
	assert.Equal(t, "DashboardList", list.Kind)
	return list
}

func TestDashboardHistoryResourcesMixedSchemasPreserveOriginalDocuments(t *testing.T) {
	db, _, handler := newCatalogServer(t)
	ctx := context.Background()
	d := model.Dashboard{ID: "native-history", Name: "History", Panels: []model.Panel{}}
	sources := []string{
		`{"dashboard":{"uid":"resource-history","title":"Classic","version":1,"panels":[],"opaque":900719925474099312345},"metadata":{"name":"outer-must-not-leak"}}`,
		`{"apiVersion":"dashboard.grafana.app/v1beta1","kind":"Dashboard","metadata":{"name":"resource-history","generation":999,"opaque":18446744073709551615,"annotations":{"opaque.example/key":"preserved"}},"spec":{"title":"V1","version":2,"panels":[],"opaque":900719925474099312345}}`,
		`{"apiVersion":"dashboard.grafana.app/v2beta1","kind":"Dashboard","metadata":{"name":"resource-history","generation":2,"opaque":18446744073709551615},"spec":{"title":"V2","elements":{},"layout":{"kind":"GridLayout","spec":{"items":[]}},"opaque":900719925474099312345}}`,
		`{"uid":"resource-history","title":"Classic again","version":200,"panels":[],"opaque":900719925474099312345}`,
	}
	for i, source := range sources {
		d.Grafana = json.RawMessage(source)
		var err error
		d, err = db.SaveDashboard(store.WithDashboardHistoryMetadata(ctx, model.DashboardHistoryMetadata{Message: fmt.Sprintf("Save %d", i+1), CreatedBy: "agent-cli"}), d)
		require.NoError(t, err)
	}
	history, err := db.DashboardVersions(ctx, d.ID, 1000, 0, "")
	require.NoError(t, err)
	v1 := readHistoryResource(t, handler, "v1beta1", d.UID(), 1000, "")
	v2 := readHistoryResource(t, handler, "v2beta1", d.UID(), 1000, "")
	require.Len(t, v1.Items, 4)
	require.Len(t, v2.Items, 4)
	var reconstructed []int64
	for i, item := range v1.Items {
		assert.Equal(t, int64(4-i), item.Metadata.Generation)
		assert.Equal(t, "resource-history", item.Metadata.Name)
		assert.Equal(t, "default", item.Metadata.Namespace)
		assert.Equal(t, strconv.FormatInt(history.Versions[i].ID, 10), item.Metadata.ResourceVersion)
		assert.Equal(t, "agent-cli", item.Metadata.Annotations["grafana.app/updatedBy"])
		assert.Equal(t, fmt.Sprintf("Save %d", 4-i), item.Metadata.Annotations["grafana.app/message"])
		assert.NotEmpty(t, item.Metadata.Annotations["grafana.app/updatedTimestamp"])
		assert.Equal(t, strconv.FormatInt(history.Versions[i].ParentVersion, 10), item.Metadata.Annotations["metricspanel.app/parent-version"])
		assert.Contains(t, string(item.Spec), "900719925474099312345")
		assert.JSONEq(t, string(item.Spec), string(v2.Items[i].Spec), "both streams must preserve one immutable spec")
		assert.Equal(t, item.Metadata.ResourceVersion, v2.Items[i].Metadata.ResourceVersion)
		if !item.Status.Conversion.Failed {
			reconstructed = append(reconstructed, item.Metadata.Generation)
		}
		if !v2.Items[i].Status.Conversion.Failed {
			reconstructed = append(reconstructed, v2.Items[i].Metadata.Generation)
		}
	}
	assert.ElementsMatch(t, []int64{4, 3, 2, 1}, reconstructed, "Grafana UnifiedDashboardAPI filters each family without duplicating or losing revisions")
	assert.True(t, v1.Items[1].Status.Conversion.Failed)
	assert.Equal(t, "v2beta1", v1.Items[1].Status.Conversion.StoredVersion)
	assert.Equal(t, "dashboard.grafana.app/v2beta1", v1.Items[1].APIVersion)
	assert.False(t, v2.Items[1].Status.Conversion.Failed)
	assert.Equal(t, "v0alpha1", v2.Items[0].Status.Conversion.StoredVersion)
	assert.True(t, v2.Items[0].Status.Conversion.Failed)
	assert.Equal(t, "v1beta1", v2.Items[2].Status.Conversion.StoredVersion)
	assert.Equal(t, "18446744073709551615", string(v1.Items[2].Metadata.Opaque))
	assert.Equal(t, "preserved", v1.Items[2].Metadata.Annotations["opaque.example/key"])
	assert.Contains(t, string(v1.Items[0].Spec), `"version":200`, "source version remains original while generation is authoritative")
	assert.NotContains(t, string(v1.Items[3].Spec), "outer-must-not-leak")
	entry, err := db.DashboardVersion(ctx, d.ID, 1)
	require.NoError(t, err)
	assert.JSONEq(t, sources[0], string(entry.Snapshot.Grafana), "GET must not rewrite the saved source")
}

func TestDashboardHistoryResourcesStableTwoStreamContinuation(t *testing.T) {
	db, _, handler := newCatalogServer(t)
	ctx := context.Background()
	d := model.Dashboard{ID: "resource-pages", Name: "Paged", Panels: []model.Panel{}}
	for i := 0; i < 4; i++ {
		if i%2 == 0 {
			d.Grafana = json.RawMessage(`{"uid":"paged-history","title":"Classic","version":1,"panels":[]}`)
		} else {
			d.Grafana = json.RawMessage(`{"apiVersion":"dashboard.grafana.app/v2beta1","metadata":{"name":"paged-history"},"spec":{"title":"V2","elements":{}}}`)
		}
		var err error
		d, err = db.SaveDashboard(ctx, d)
		require.NoError(t, err)
	}
	firstV1 := readHistoryResource(t, handler, "v1", d.UID(), 2, "")
	firstV2 := readHistoryResource(t, handler, "v2", d.UID(), 2, "")
	require.NotEmpty(t, firstV1.Metadata.Continue)
	assert.Equal(t, firstV1.Metadata.Continue, firstV2.Metadata.Continue)
	_, err := db.SaveDashboard(ctx, d)
	require.NoError(t, err)
	for _, version := range []string{"v1", "v2"} {
		next := readHistoryResource(t, handler, version, d.UID(), 2, firstV1.Metadata.Continue)
		require.Len(t, next.Items, 2)
		assert.Equal(t, []int64{2, 1}, []int64{next.Items[0].Metadata.Generation, next.Items[1].Metadata.Generation})
		assert.Empty(t, next.Metadata.Continue)
	}
	assert.Equal(t, 400, call(handler, "GET", historyResourcePath("v1", "system", 2, firstV1.Metadata.Continue), "", "catalog-token-233", "").Code)
}

func TestDashboardHistoryResourcesValidationAuthenticationAndNativeSpec(t *testing.T) {
	_, _, handler := newCatalogServer(t)
	path := historyResourcePath("v1beta1", "system", 1000, "")
	assert.Equal(t, 401, call(handler, "GET", path, "", "", "").Code)
	list := readHistoryResource(t, handler, "v1beta1", "system", 1000, "")
	require.NotEmpty(t, list.Items)
	assert.Contains(t, string(list.Items[0].Spec), `"panels"`)
	assert.Contains(t, string(list.Items[0].Spec), `"targets"`)
	for _, path := range []string{
		"/apis/dashboard.grafana.app/v1/namespaces/default/dashboards",
		historyResourcePath("v1", "system", 1001, ""),
		historyResourcePath("v1", "system", -1, ""),
		historyResourcePath("v1", "system", 1, "bad-token"),
		historyResourcePath("v1", "system", 1, "") + "&limit=2",
		"/apis/dashboard.grafana.app/v1/namespaces/default/dashboards?labelSelector=grafana.app/get-history%3Dtrue&fieldSelector=metadata.name%3Dsystem,metadata.name%3Dother",
	} {
		assert.Equal(t, 400, call(handler, "GET", path, "", "catalog-token-233", "").Code, path)
	}
	assert.Equal(t, 404, call(handler, "GET", historyResourcePath("v9", "system", 1, ""), "", "catalog-token-233", "").Code)
	assert.Equal(t, 404, call(handler, "GET", "/apis/dashboard.grafana.app/v1/namespaces/other/dashboards?labelSelector=grafana.app/get-history%3Dtrue&fieldSelector=metadata.name%3Dsystem", "", "catalog-token-233", "").Code)
	assert.Equal(t, 404, call(handler, "GET", historyResourcePath("v1", "absent", 1, ""), "", "catalog-token-233", "").Code)
}

func TestDashboardHistoryResourcesResponseBudgetKeepsPaginationAndRejectsWholeResponse(t *testing.T) {
	db, _, handler := newCatalogServer(t)
	ctx := context.Background()
	d := model.Dashboard{ID: "large-history", Name: "Large", Panels: []model.Panel{}}
	prefix, suffix := `{"uid":"large-history","title":"Large","version":1,"panels":[],"opaque":"`, `"}`
	source := prefix + strings.Repeat("x", 4*1024*1024-len(prefix)-len(suffix)) + suffix
	for range 4 {
		d.Grafana = json.RawMessage(source)
		var err error
		d, err = db.SaveDashboard(ctx, d)
		require.NoError(t, err)
	}
	first := readHistoryResource(t, handler, "v1", d.UID(), 2, "")
	require.Len(t, first.Items, 2)
	require.NotEmpty(t, first.Metadata.Continue)
	w := call(handler, "GET", historyResourcePath("v1", d.UID(), 1000, ""), "", "catalog-token-233", "")
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "reduce limit")
	assert.NotContains(t, w.Body.String(), `"items"`, "budget failure must not emit a partial resource stream")
	second := readHistoryResource(t, handler, "v1", d.UID(), 2, first.Metadata.Continue)
	require.Len(t, second.Items, 2)
	assert.Equal(t, []int64{2, 1}, []int64{second.Items[0].Metadata.Generation, second.Items[1].Metadata.Generation})
	assert.Empty(t, second.Metadata.Continue)
	history, err := db.DashboardVersions(ctx, d.ID, 1000, 0, "")
	require.NoError(t, err)
	assert.Len(t, history.Versions, 4, "history reads do not write or truncate stored revisions")
}
