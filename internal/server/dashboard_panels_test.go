package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/grafana"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativePanelEditAndDuplicatePreserveQueriesAndOpaqueValues(t *testing.T) {
	db, handler := setup(t, "")
	d, err := db.SaveDashboard(context.Background(), model.Dashboard{ID: "native-edit", Name: "Native", Panels: []model.Panel{
		{ID: "first", Title: "First", Aggregation: "last", Visualization: "table", Config: json.RawMessage(`{"id":7,"type":"table","opaque":1e10000,"fieldConfig":{"defaults":{"unit":"ms","opaque":9007199254740993},"overrides":[]},"targets":[{"refId":"A","rawSql":"SELECT 233","datasource":{"uid":"sql","type":"mysql"},"opaque":9007199254740993}]}`)},
		{ID: "other", Title: "Other", Aggregation: "last", Expr: "vector(9)", Config: json.RawMessage(`{"gridPos":{"x":12,"y":0,"w":12,"h":8},"opaque":9007199254740993}`)},
	}})
	require.NoError(t, err)
	d, err = db.Dashboard(context.Background(), d.ID)
	require.NoError(t, err)
	other := d.Panels[1]
	request := func(body string, revision int64) int {
		r := httptest.NewRequest("PATCH", "/api/v1/dashboards/"+d.ID+"/panels", bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		if revision > 0 {
			r.Header.Set("If-Match", strconv.FormatInt(revision, 10))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code == 200 {
			var saved model.Dashboard
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &saved))
			d = saved
		}
		return w.Code
	}
	baseRevision := d.UpdatedAt
	body := `{"action":"save","sourceId":"first","panel":{"id":"first","title":"Renamed","visualization":"stat","unit":"","expr":"vector(999)"}}`
	require.Equal(t, 200, request(body, d.UpdatedAt))
	assert.Contains(t, string(d.Panels[0].Config), "1e10000")
	assert.Contains(t, string(d.Panels[0].Config), "9007199254740993")
	assert.Contains(t, string(d.Panels[0].Config), `"rawSql":"SELECT 233"`)
	assert.Contains(t, string(d.Panels[0].Config), `"unit":"ms"`)
	assert.Equal(t, other, d.Panels[1])
	assert.Equal(t, "", d.Panels[0].Expr)
	assert.Equal(t, 409, request(body, baseRevision))
	assert.Equal(t, 428, request(body, 0))
	require.Equal(t, 200, request(`{"action":"duplicate","sourceId":"first","panel":{"id":"copied","title":"Copy","visualization":"table","unit":""},"position":{"x":0,"y":8,"w":12,"h":8}}`, d.UpdatedAt))
	require.Len(t, d.Panels, 3)
	assert.Contains(t, string(d.Panels[2].Config), "1e10000")
	assert.Contains(t, string(d.Panels[2].Config), `"id":8`)
	assert.Equal(t, "copied", d.Panels[2].ID)
	assert.Equal(t, 400, request(`{"action":"duplicate","sourceId":"first","panel":{"id":"copied","title":"Duplicate"},"position":{"x":0,"y":16,"w":12,"h":8}}`, d.UpdatedAt))
	require.Equal(t, 200, request(`{"action":"delete","sourceId":"copied"}`, d.UpdatedAt))
	require.Len(t, d.Panels, 2)
	assert.Equal(t, other, d.Panels[1])
	history, err := db.DashboardVersions(context.Background(), d.ID, 1000, 0, "")
	require.NoError(t, err)
	assert.Len(t, history.Versions, 4)
}

func TestNativePanelSaveExportsLegacyPromQLAfterDisplayChanges(t *testing.T) {
	db, handler := setup(t, "")
	d, err := db.SaveDashboard(context.Background(), model.Dashboard{ID: "native-promql-edit", Name: "PromQL", Panels: []model.Panel{{ID: "one", Title: "One", Expr: "vector(233)", Aggregation: "last"}}})
	require.NoError(t, err)
	r := httptest.NewRequest("PATCH", "/api/v1/dashboards/"+d.ID+"/panels", bytes.NewBufferString(`{"action":"save","sourceId":"one","panel":{"id":"one","title":"Gauge","visualization":"gauge","expr":"vector(234)","aggregation":"last","unit":"count"}}`))
	r.Header.Set("If-Match", strconv.FormatInt(d.UpdatedAt, 10))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	require.Equal(t, 200, w.Code, w.Body.String())
	r = httptest.NewRequest("GET", "/api/v1/dashboard-exports/"+d.ID+"?format=grafana", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	require.Equal(t, 200, w.Code, w.Body.String())
	result, err := grafana.Import(w.Body.Bytes())
	require.NoError(t, err)
	require.Len(t, result.Dashboard.Panels, 1)
	assert.Equal(t, "vector(234)", result.Dashboard.Panels[0].Expr)
	assert.Equal(t, "gauge", result.Dashboard.Panels[0].Visualization)
	assert.Equal(t, "Gauge", result.Dashboard.Panels[0].Title)
}
