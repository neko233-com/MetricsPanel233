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

func TestAnnotationQueryEditsPreserveRawNumbersReorderingAndSource(t *testing.T) {
	annotations := `[ {"name":"First","opaque":9007199254740993,"datasource":{"uid":"grafana","type":"grafana"},"target":{"tags":["maintenance"]}}, {"name":"Second","opaque":9007199254740993} ]`
	classic := `{"uid":"annotations-raw","title":"Annotations","opaque":1e10000,"annotations":{"opaque":9007199254740993,"list":` + annotations + `},"panels":[{"id":1,"type":"stat","title":"Keep","options":{"opaque":9007199254740993},"targets":[{"refId":"A","expr":"vector(233)"}]}]}`
	v2Queries := `[ {"kind":"AnnotationQuery","opaque":9007199254740993,"spec":{"name":"First","query":{"kind":"DataQuery","group":"grafana","datasource":{"name":"grafana"},"spec":{"tags":["maintenance"],"opaque":9007199254740993}}}}, {"kind":"AnnotationQuery","spec":{"name":"Second","opaque":9007199254740993}} ]`
	v2 := `{"apiVersion":"dashboard.grafana.app/v2beta1","metadata":{"name":"annotations-raw","opaque":1e10000},"spec":{"title":"Annotations","annotations":` + v2Queries + `,"elements":{"keep":{"kind":"Panel","spec":{"id":1,"title":"Keep","vizConfig":{"group":"text","spec":{"options":{"content":"Keep","opaque":9007199254740993}}}}}},"layout":{"kind":"GridLayout","spec":{"items":[{"kind":"GridLayoutItem","spec":{"x":0,"y":0,"width":12,"height":8,"element":{"name":"keep"}}}]}}}}`
	for name, source := range map[string]string{"native": "", "classic": classic, "v1": `{"apiVersion":"dashboard.grafana.app/v1beta1","metadata":{"name":"annotations-raw"},"spec":` + classic + `}`, "v2": v2} {
		t.Run(name, func(t *testing.T) {
			db, handler := setup(t, "")
			d := model.Dashboard{ID: "annotations-raw", Name: "Annotations", Annotations: json.RawMessage(annotations)}
			if source != "" {
				imported, err := grafana.Import([]byte(source))
				require.NoError(t, err)
				d = imported.Dashboard
			}
			d, err := db.SaveDashboard(context.Background(), d)
			require.NoError(t, err)
			d, err = db.Dashboard(context.Background(), d.ID)
			require.NoError(t, err)
			queries := annotations
			if name == "v2" {
				queries = v2Queries
			}
			var original []map[string]any
			require.NoError(t, json.Unmarshal([]byte(queries), &original))
			baseline, _ := json.Marshal(original[0])
			assert.Contains(t, string(baseline), "9007199254740992")
			if name == "v2" {
				original[0]["spec"].(map[string]any)["name"] = "Renamed"
			} else {
				original[0]["name"] = "Renamed"
			}
			updated, _ := json.Marshal(original[0])
			second, _ := json.Marshal(original[1])
			body, _ := json.Marshal(map[string]any{"queries": []any{map[string]any{"originalIndex": 1, "baseline": json.RawMessage(second), "query": json.RawMessage(second)}, map[string]any{"originalIndex": 0, "baseline": json.RawMessage(baseline), "query": json.RawMessage(updated)}}})
			r := httptest.NewRequest("PATCH", "/api/v1/dashboards/"+d.ID+"/annotations", bytes.NewReader(body))
			r.Header.Set("If-Match", strconv.FormatInt(d.UpdatedAt, 10))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			require.Equal(t, 200, w.Code, w.Body.String())
			saved, err := db.Dashboard(context.Background(), d.ID)
			require.NoError(t, err)
			raw := saved.Annotations
			if source != "" {
				raw = saved.Grafana
				assert.Contains(t, string(raw), "1e10000")
			}
			assert.Contains(t, string(raw), "9007199254740993")
			assert.NotContains(t, string(raw), "9007199254740992")
			assert.Contains(t, string(raw), "Renamed")
			assert.Less(t, bytes.Index(raw, []byte("Second")), bytes.Index(raw, []byte("Renamed")))
			assert.Equal(t, d.Panels, saved.Panels)
			history, err := db.DashboardVersions(context.Background(), d.ID, 1000, 0, "")
			require.NoError(t, err)
			assert.Len(t, history.Versions, 2)
			r = httptest.NewRequest("PATCH", "/api/v1/dashboards/"+d.ID+"/annotations", bytes.NewReader(body))
			r.Header.Set("If-Match", strconv.FormatInt(d.UpdatedAt, 10))
			r.Header.Set("Content-Type", "application/json")
			w = httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			assert.Equal(t, 409, w.Code)
		})
	}
}

func TestAnnotationQueriesRejectInvalidBaselineAndAcceptEmptyArray(t *testing.T) {
	db, handler := setup(t, "")
	d, err := db.SaveDashboard(context.Background(), model.Dashboard{ID: "annotation-native", Name: "Native", Annotations: json.RawMessage(`[{"name":"A","opaque":9007199254740993}]`)})
	require.NoError(t, err)
	for _, tc := range []struct {
		body   string
		match  string
		status int
	}{
		{`{"queries":[]}`, "", 428}, {`{"queries":[]}`, "bad", 400}, {`{}`, strconv.FormatInt(d.UpdatedAt, 10), 400},
		{`{"queries":[{"originalIndex":0,"baseline":{"name":"Wrong"},"query":{"name":"Edited"}}]}`, strconv.FormatInt(d.UpdatedAt, 10), 400},
		{`{"queries":[{"query":null}]}`, strconv.FormatInt(d.UpdatedAt, 10), 400},
	} {
		r := httptest.NewRequest("PATCH", "/api/v1/dashboards/"+d.ID+"/annotations", bytes.NewBufferString(tc.body))
		r.Header.Set("If-Match", tc.match)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		assert.Equal(t, tc.status, w.Code, w.Body.String())
	}
	current, err := db.Dashboard(context.Background(), d.ID)
	require.NoError(t, err)
	assert.Equal(t, d, current)
	r := httptest.NewRequest("PATCH", "/api/v1/dashboards/"+d.ID+"/annotations", bytes.NewBufferString(`{"queries":[]}`))
	r.Header.Set("If-Match", strconv.FormatInt(d.UpdatedAt, 10))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	require.Equal(t, 200, w.Code, w.Body.String())
	current, err = db.Dashboard(context.Background(), d.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, string(current.Annotations))
}
