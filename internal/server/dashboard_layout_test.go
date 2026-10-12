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

func TestDashboardLayoutPreservesGrafanaSourcesAndHistory(t *testing.T) {
	for name, source := range map[string]string{
		"classic": `{"uid":"layout","title":"Layout","opaque":9007199254740993,"panels":[{"id":1,"type":"text","title":"One","options":{"content":"kept","opaque":1e10000},"gridPos":{"x":0,"y":0,"w":12,"h":8}},{"id":2,"type":"row","title":"Row","panels":[{"id":3,"type":"stat","title":"Nested","targets":[{"refId":"A","expr":"vector(1)"}],"gridPos":{"x":0,"y":9,"w":12,"h":8}}],"gridPos":{"x":0,"y":8,"w":24,"h":1}}]}`,
		"v1":      `{"apiVersion":"dashboard.grafana.app/v1beta1","metadata":{"name":"layout","opaque":9007199254740993},"spec":{"title":"Layout","panels":[{"id":1,"type":"text","title":"One","options":{"content":"kept","opaque":1e10000}}]}}`,
		"v2-grid": `{"apiVersion":"dashboard.grafana.app/v2beta1","metadata":{"name":"layout","opaque":9007199254740993},"spec":{"title":"Layout","elements":{"a":{"kind":"Panel","spec":{"id":1,"title":"One","vizConfig":{"group":"text","spec":{"options":{"content":"kept","opaque":1e10000}}}}}},"layout":{"kind":"GridLayout","spec":{"items":[{"kind":"GridLayoutItem","spec":{"x":0,"y":0,"width":12,"height":8,"element":{"name":"a"},"opaque":9007199254740993}}]}}}}`,
		"v2-auto": `{"apiVersion":"dashboard.grafana.app/v2beta1","metadata":{"name":"layout","opaque":9007199254740993},"spec":{"title":"Layout","elements":{"a":{"kind":"Panel","spec":{"id":1,"title":"One","vizConfig":{"group":"text","spec":{"options":{"content":"kept","opaque":1e10000}}}}}},"layout":{"kind":"AutoGridLayout","spec":{"maxColumnCount":2,"items":[{"kind":"AutoGridLayoutItem","spec":{"element":{"name":"a"},"opaque":9007199254740993}}]}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			db, handler := setup(t, "")
			imported, err := grafana.Import([]byte(source))
			require.NoError(t, err)
			d, err := db.SaveDashboard(context.Background(), imported.Dashboard)
			require.NoError(t, err)
			positions := make([]map[string]any, len(d.Panels))
			for i, p := range d.Panels {
				positions[i] = map[string]any{"id": p.ID, "x": 2, "y": i * 6, "w": 20, "h": 6}
			}
			body, err := json.Marshal(map[string]any{"panels": positions})
			require.NoError(t, err)
			r := httptest.NewRequest("PATCH", "/api/v1/dashboards/"+d.ID+"/layout", bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("If-Match", strconv.FormatInt(d.UpdatedAt, 10))
			r.Header.Set("X-Dashboard-Message", `"Arrange panels"`)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			require.Equal(t, 200, w.Code, w.Body.String())
			saved, err := db.Dashboard(context.Background(), d.ID)
			require.NoError(t, err)
			assert.Greater(t, saved.UpdatedAt, d.UpdatedAt)
			assert.Contains(t, string(saved.Grafana), "9007199254740993")
			assert.Contains(t, string(saved.Grafana), "1e10000")
			assert.Contains(t, string(saved.Grafana), "kept")
			again, err := grafana.Import(saved.Grafana)
			require.NoError(t, err)
			require.Len(t, again.Dashboard.Panels, len(d.Panels))
			for i, p := range again.Dashboard.Panels {
				var config struct {
					Grid struct{ X, Y, W, H int } `json:"gridPos"`
				}
				require.NoError(t, json.Unmarshal(p.Config, &config))
				assert.Equal(t, 2, config.Grid.X)
				assert.Equal(t, i*6, config.Grid.Y)
				assert.Equal(t, 20, config.Grid.W)
				assert.Equal(t, 6, config.Grid.H)
			}
			history, err := db.DashboardVersions(context.Background(), d.ID, 1000, 0, "")
			require.NoError(t, err)
			require.Len(t, history.Versions, 2)
			assert.Equal(t, "Arrange panels", history.Versions[0].Message)
			assert.Contains(t, string(saved.Panels[0].Config), "1e10000")
		})
	}
}

func TestDashboardLayoutRejectsIncompleteInvalidAndStaleEdits(t *testing.T) {
	db, handler := setup(t, "")
	d, err := db.SaveDashboard(context.Background(), model.Dashboard{ID: "layout-native", Name: "Layout", Panels: []model.Panel{{ID: "first", Title: "First", Expr: "vector(1)", Aggregation: "last"}, {ID: "second", Title: "Second", Expr: "vector(2)", Aggregation: "last"}}})
	require.NoError(t, err)
	valid := `{"panels":[{"id":"first","x":0,"y":0,"w":12,"h":8},{"id":"second","x":12,"y":0,"w":12,"h":8}]}`
	for _, tc := range []struct {
		match, body string
		status      int
	}{
		{"", valid, 428}, {"*", valid, 400},
		{strconv.FormatInt(d.UpdatedAt, 10), `{"panels":[]}`, 400},
		{strconv.FormatInt(d.UpdatedAt, 10), `{"panels":[{"id":"first","x":0,"y":0,"w":12,"h":8},{"id":"first","x":12,"y":0,"w":12,"h":8}]}`, 400},
		{strconv.FormatInt(d.UpdatedAt, 10), `{"panels":[{"id":"first","x":23,"y":0,"w":12,"h":8},{"id":"second","x":12,"y":0,"w":12,"h":8}]}`, 400},
		{strconv.FormatInt(d.UpdatedAt, 10), `{"panels":[{"id":"first","x":0.5,"y":0,"w":12,"h":8},{"id":"second","x":12,"y":0,"w":12,"h":8}]}`, 400},
		{strconv.FormatInt(d.UpdatedAt-1, 10), valid, 409},
	} {
		r := httptest.NewRequest("PATCH", "/api/v1/dashboards/layout-native/layout", bytes.NewBufferString(tc.body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("If-Match", tc.match)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		assert.Equal(t, tc.status, w.Code, w.Body.String())
	}
	after, err := db.Dashboard(context.Background(), d.ID)
	require.NoError(t, err)
	assert.Equal(t, d, after)
	r := httptest.NewRequest("PATCH", "/api/v1/dashboards/layout-native/layout", bytes.NewBufferString(valid))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("If-Match", strconv.FormatInt(d.UpdatedAt, 10))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	require.Equal(t, 200, w.Code, w.Body.String())
	saved, err := db.Dashboard(context.Background(), d.ID)
	require.NoError(t, err)
	assert.Equal(t, d.Panels[0].Expr, saved.Panels[0].Expr)
	assert.Contains(t, string(saved.Panels[1].Config), `"x":12`)
	export := httptest.NewRecorder()
	handler.ServeHTTP(export, httptest.NewRequest("GET", "/api/v1/dashboard-exports/layout-native?format=grafana", nil))
	require.Equal(t, 200, export.Code, export.Body.String())
	roundtrip, err := grafana.Import(export.Body.Bytes())
	require.NoError(t, err)
	require.Len(t, roundtrip.Dashboard.Panels, 2)
	assert.Equal(t, "vector(1)", roundtrip.Dashboard.Panels[0].Expr)
	assert.Equal(t, "First", roundtrip.Dashboard.Panels[0].Title)
	r = httptest.NewRequest("PATCH", "/api/v1/dashboards/layout-native/layout", bytes.NewBufferString(valid))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("If-Match", strconv.FormatInt(d.UpdatedAt, 10))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	assert.Equal(t, 409, w.Code)
}
