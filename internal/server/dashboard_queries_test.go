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

func TestDashboardQueryEditsPreserveUnchangedRawNumbersAndEnvelopes(t *testing.T) {
	classic := `{"uid":"query-precision","title":"Queries","opaque":1e10000,"panels":[{"id":1,"type":"stat","title":"First","options":{"opaque":9007199254740993},"targets":[{"refId":"A","expr":"vector(1)","opaque":9007199254740993,"filters":[{"id":"unchanged","opaque":9007199254740993}],"datasource":{"uid":"metricspanel","type":"prometheus"}},{"refId":"B","expr":"vector(2)","opaque":9007199254740993}]},{"id":2,"type":"text","title":"Untouched","options":{"opaque":9007199254740993}}]}`
	v2 := `{"apiVersion":"dashboard.grafana.app/v2beta1","metadata":{"name":"query-precision","opaque":1e10000},"spec":{"title":"Queries","elements":{"a":{"kind":"Panel","spec":{"id":1,"title":"First","vizConfig":{"group":"stat","spec":{"options":{"opaque":9007199254740993}}},"data":{"kind":"QueryGroup","spec":{"opaque":9007199254740993,"queries":[{"kind":"PanelQuery","opaque":9007199254740993,"spec":{"refId":"A","query":{"kind":"DataQuery","group":"prometheus","opaque":9007199254740993,"datasource":{"name":"metricspanel","opaque":9007199254740993},"spec":{"expr":"vector(1)","opaque":9007199254740993,"filters":[{"id":"unchanged","opaque":9007199254740993}]}}}},{"kind":"PanelQuery","spec":{"refId":"B","query":{"group":"prometheus","spec":{"expr":"vector(2)","opaque":9007199254740993}}}}]}}}},"b":{"kind":"Panel","spec":{"id":2,"title":"Untouched","vizConfig":{"group":"text","spec":{"options":{"opaque":9007199254740993}}}}}},"layout":{"kind":"GridLayout","spec":{"items":[{"kind":"GridLayoutItem","spec":{"x":0,"y":0,"width":12,"height":8,"element":{"name":"a"}}},{"kind":"GridLayoutItem","spec":{"x":12,"y":0,"width":12,"height":8,"element":{"name":"b"}}}]}}}}`
	for name, source := range map[string]string{"classic": classic, "v1": `{"apiVersion":"dashboard.grafana.app/v1beta1","metadata":{"name":"query-precision"},"spec":` + classic + `}`, "v2": v2} {
		t.Run(name, func(t *testing.T) {
			db, handler := setup(t, "")
			imported, err := grafana.Import([]byte(source))
			require.NoError(t, err)
			d, err := db.SaveDashboard(context.Background(), imported.Dashboard)
			require.NoError(t, err)
			d, err = db.Dashboard(context.Background(), d.ID)
			require.NoError(t, err)
			var config struct {
				Targets []map[string]any `json:"targets"`
			}
			require.NoError(t, json.Unmarshal(d.Panels[0].Config, &config))
			// Match the browser JSON.parse -> JSON.stringify roundtrip, which
			// rounds these unknown counters down before the editor opens.
			baseline, err := json.Marshal(config.Targets[0])
			require.NoError(t, err)
			require.Contains(t, string(baseline), "9007199254740992")
			config.Targets[0]["expr"] = "vector(3)"
			config.Targets[0]["refId"] = "C"
			query, err := json.Marshal(config.Targets[0])
			require.NoError(t, err)
			second, err := json.Marshal(config.Targets[1])
			require.NoError(t, err)
			body, err := json.Marshal(map[string]any{"panels": []any{map[string]any{"id": d.Panels[0].ID, "queries": []any{map[string]any{"originalRef": "A", "baseline": json.RawMessage(baseline), "query": json.RawMessage(query)}, map[string]any{"originalRef": "B", "baseline": json.RawMessage(second), "query": json.RawMessage(second)}}}}})
			require.NoError(t, err)
			r := httptest.NewRequest("PATCH", "/api/v1/dashboards/"+d.ID+"/queries", bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("If-Match", strconv.FormatInt(d.UpdatedAt, 10))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			require.Equal(t, 200, w.Code, w.Body.String())
			saved, err := db.Dashboard(context.Background(), d.ID)
			require.NoError(t, err)
			assert.Contains(t, string(saved.Grafana), "1e10000")
			assert.NotContains(t, string(saved.Grafana), "9007199254740992")
			assert.NotContains(t, string(saved.Panels[0].Config), "9007199254740992")
			assert.Equal(t, d.Panels[1], saved.Panels[1])
			assert.Equal(t, "vector(3)", saved.Panels[0].Expr)
			roundtrip, err := grafana.Import(saved.Grafana)
			require.NoError(t, err)
			assert.Equal(t, []string{"vector(3)", "vector(2)"}, roundtrip.Dashboard.Panels[0].Expressions)
			assert.Contains(t, string(roundtrip.Dashboard.Panels[0].Config), `"refId":"C"`)
			history, err := db.DashboardVersions(context.Background(), d.ID, 1000, 0, "")
			require.NoError(t, err)
			assert.Len(t, history.Versions, 2)
			r = httptest.NewRequest("PATCH", "/api/v1/dashboards/"+d.ID+"/queries", bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("If-Match", strconv.FormatInt(d.UpdatedAt, 10))
			w = httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			assert.Equal(t, 409, w.Code)
		})
	}
}

func TestDashboardQueriesRejectInvalidBaselineAtomically(t *testing.T) {
	db, handler := setup(t, "")
	d, err := db.SaveDashboard(context.Background(), model.Dashboard{ID: "query-native", Name: "Queries", Panels: []model.Panel{{ID: "one", Title: "One", Aggregation: "last", Expr: "vector(1)", Config: json.RawMessage(`{"targets":[{"refId":"A","expr":"vector(1)"}]}`)}}})
	require.NoError(t, err)
	for _, tc := range []struct {
		match, queries string
		status         int
	}{
		{"", `[]`, 428}, {`""123""`, `[]`, 400},
		{strconv.FormatInt(d.UpdatedAt, 10), `[{"originalRef":"A","baseline":{"refId":"A","expr":"vector(999)"},"query":{"refId":"A","expr":"vector(2)"}}]`, 400},
		{strconv.FormatInt(d.UpdatedAt, 10), `[{"query":{"refId":"A","expr":"vector(2)"}},{"query":{"refId":"A","expr":"vector(3)"}}]`, 400},
	} {
		body := `{"panels":[{"id":"one","queries":` + tc.queries + `}]}`
		r := httptest.NewRequest("PATCH", "/api/v1/dashboards/query-native/queries", bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("If-Match", tc.match)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		assert.Equal(t, tc.status, w.Code, w.Body.String())
	}
	current, err := db.Dashboard(context.Background(), d.ID)
	require.NoError(t, err)
	assert.Equal(t, d, current)
}

func TestDashboardQueriesV2DatasourceClearingAndUntouchedResources(t *testing.T) {
	source := `{"apiVersion":"dashboard.grafana.app/v2beta1","metadata":{"name":"clear-datasource"},"spec":{"title":"Queries","elements":{"a":{"kind":"Panel","spec":{"id":1,"title":"First","vizConfig":{"group":"stat","spec":{}},"data":{"kind":"QueryGroup","spec":{"queries":[{"kind":"PanelQuery","opaque":9007199254740993,"spec":{"refId":"A","query":{"kind":"DataQuery","group":"prometheus","datasource":{"name":"remote-prom","opaque":9007199254740993},"spec":{"expr":"vector(1)","opaque":9007199254740993}}}},{"kind":"PanelQuery","spec":{"refId":"B","query":{"kind":"DataQuery","group":"prometheus","spec":{"expr":"vector(2)","opaque":9007199254740993}}}}]}}}}},"layout":{"kind":"GridLayout","spec":{"items":[{"kind":"GridLayoutItem","spec":{"x":0,"y":0,"width":12,"height":8,"element":{"name":"a"}}}]}}}}`
	decodeExact := func(raw []byte) map[string]any {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value map[string]any
		require.NoError(t, decoder.Decode(&value))
		return value
	}
	queriesInSource := func(raw []byte) []any {
		doc := decodeExact(raw)
		return doc["spec"].(map[string]any)["elements"].(map[string]any)["a"].(map[string]any)["spec"].(map[string]any)["data"].(map[string]any)["spec"].(map[string]any)["queries"].([]any)
	}
	for _, action := range []string{"omit", "null", "empty-uid", "empty-reference", "empty-string", "keep", "new-uid"} {
		t.Run(action, func(t *testing.T) {
			db, handler := setup(t, "")
			imported, err := grafana.Import([]byte(source))
			require.NoError(t, err)
			d, err := db.SaveDashboard(context.Background(), imported.Dashboard)
			require.NoError(t, err)
			var config struct {
				Targets []map[string]any `json:"targets"`
			}
			require.NoError(t, json.Unmarshal(d.Panels[0].Config, &config))
			baseline, err := json.Marshal(config.Targets[0])
			require.NoError(t, err)
			config.Targets[0]["expr"] = "vector(3)"
			switch action {
			case "omit":
				delete(config.Targets[0], "datasource")
			case "null":
				config.Targets[0]["datasource"] = nil
			case "empty-uid":
				config.Targets[0]["datasource"] = map[string]any{"uid": "", "type": "prometheus"}
			case "empty-reference":
				config.Targets[0]["datasource"] = map[string]any{"type": "prometheus"}
			case "empty-string":
				config.Targets[0]["datasource"] = ""
			case "new-uid":
				config.Targets[0]["datasource"] = map[string]any{"uid": "replacement-prom", "type": "prometheus"}
			}
			query, _ := json.Marshal(config.Targets[0])
			second, _ := json.Marshal(config.Targets[1])
			body, _ := json.Marshal(map[string]any{"panels": []any{map[string]any{"id": d.Panels[0].ID, "queries": []any{map[string]any{"originalRef": "A", "baseline": json.RawMessage(baseline), "query": json.RawMessage(query)}, map[string]any{"originalRef": "B", "baseline": json.RawMessage(second), "query": json.RawMessage(second)}}}}})
			r := httptest.NewRequest("PATCH", "/api/v1/dashboards/"+d.ID+"/queries", bytes.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("If-Match", strconv.FormatInt(d.UpdatedAt, 10))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			require.Equal(t, 200, w.Code, w.Body.String())
			saved, err := db.Dashboard(context.Background(), d.ID)
			require.NoError(t, err)
			originals, updated := queriesInSource([]byte(source)), queriesInSource(saved.Grafana)
			assert.Equal(t, originals[1], updated[1], "untouched V2 query envelope remains exactly unchanged")
			dataQuery := updated[0].(map[string]any)["spec"].(map[string]any)["query"].(map[string]any)
			reference := dataQuery["datasource"].(map[string]any)
			expected := ""
			if action == "keep" {
				expected = "remote-prom"
			}
			if action == "new-uid" {
				expected = "replacement-prom"
			}
			assert.Equal(t, expected, reference["name"])
			assert.Equal(t, json.Number("9007199254740993"), reference["opaque"])
			assert.Equal(t, json.Number("9007199254740993"), dataQuery["spec"].(map[string]any)["opaque"])
			roundtrip, err := grafana.Import(saved.Grafana)
			require.NoError(t, err)
			var exported struct {
				Targets []map[string]any `json:"targets"`
			}
			require.NoError(t, json.Unmarshal(roundtrip.Dashboard.Panels[0].Config, &exported))
			assert.Equal(t, expected, exported.Targets[0]["datasource"].(map[string]any)["uid"])
			assert.Equal(t, "vector(3)", exported.Targets[0]["expr"])
		})
	}
}
