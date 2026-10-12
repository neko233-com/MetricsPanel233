package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/grafana"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func variableBrowserCopy(t *testing.T, variable model.Variable) (json.RawMessage, rawObject) {
	t.Helper()
	value, err := historyJSON(rawValue(variable))
	require.NoError(t, err)
	raw := rawValue(browserJSON(value))
	var object rawObject
	require.NoError(t, json.Unmarshal(raw, &object))
	return raw, object
}

func variableTestHandler(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "variables.db"))
	require.NoError(t, err)
	app := New(db, "", 30)
	t.Cleanup(func() {
		app.CloseSQLSources()
		app.Plugins.Close()
		app.Live.Close()
		_ = db.DB.Close()
	})
	return db, app.Handler()
}

func variablePATCH(handler http.Handler, d model.Dashboard, body any, match string) *httptest.ResponseRecorder {
	raw := rawValue(body)
	r := httptest.NewRequest("PATCH", "/api/v1/dashboards/"+d.ID+"/variables", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	if match != "" {
		r.Header.Set("If-Match", match)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestDashboardVariablesPatchPreservesImportedRawAndHistory(t *testing.T) {
	classic := `{"uid":"variables-precision","title":"Variables","opaque":1e10000,"panels":[{"id":1,"title":"One","type":"stat","options":{"opaque":9007199254740993},"targets":[{"refId":"A","expr":"vector(233)"}]}],"templating":{"opaque":9007199254740993,"list":[{"name":"host","type":"query","query":{"query":"label_values(host)","opaque":9007199254740993},"datasource":{"uid":"metricspanel","type":"prometheus","opaque":9007199254740993},"multi":true,"includeAll":true,"current":{"value":["alpha"],"text":["Alpha"],"opaque":9007199254740993},"options":[{"value":"alpha","text":"Alpha","opaque":9007199254740993},{"value":"beta","text":"Beta"}],"opaque":9007199254740993},{"name":"drop","type":"constant","query":"deleted"},{"name":"interval","type":"interval","query":"1m,5m","current":{"value":"5m"},"options":[{"value":"1m","text":"1 minute"},{"value":"5m","text":"5 minutes","opaque":9007199254740993}]}]}}`
	v2 := `{"apiVersion":"dashboard.grafana.app/v2beta1","metadata":{"name":"variables-precision","opaque":1e10000},"spec":{"title":"Variables","elements":{"a":{"kind":"Panel","spec":{"id":1,"title":"One","vizConfig":{"group":"stat","spec":{"options":{"opaque":9007199254740993}}},"data":{"kind":"QueryGroup","spec":{"queries":[{"kind":"PanelQuery","spec":{"refId":"A","query":{"group":"prometheus","spec":{"expr":"vector(233)"}}}}]}}}}},"layout":{"kind":"GridLayout","spec":{"items":[{"kind":"GridLayoutItem","spec":{"x":0,"y":0,"width":12,"height":8,"element":{"name":"a"}}}]}},"variables":[{"kind":"QueryVariable","opaque":1e10000,"spec":{"name":"host","query":{"kind":"DataQuery","group":"prometheus","opaque":9007199254740993,"datasource":{"name":"metricspanel","opaque":9007199254740993},"spec":{"query":"label_values(host)","opaque":9007199254740993}},"multi":true,"includeAll":true,"current":{"value":["alpha"],"text":["Alpha"],"opaque":9007199254740993},"options":[{"value":"alpha","text":"Alpha","opaque":9007199254740993},{"value":"beta","text":"Beta"}],"opaque":9007199254740993}},{"kind":"ConstantVariable","spec":{"name":"drop","query":"deleted"}},{"kind":"IntervalVariable","opaque":9007199254740993,"spec":{"name":"interval","query":"1m,5m","current":{"value":"5m"},"options":[{"value":"1m","text":"1 minute"},{"value":"5m","text":"5 minutes","opaque":9007199254740993}]}}]}}`
	for name, source := range map[string]string{
		"classic":         classic,
		"classic-wrapper": `{"meta":{"opaque":1e10000},"dashboard":` + classic + `}`,
		"v1":              `{"apiVersion":"dashboard.grafana.app/v1beta1","metadata":{"name":"variables-precision","opaque":1e10000},"spec":` + classic + `}`,
		"v2":              v2,
		"v2-wrapper":      `{"meta":{"opaque":1e10000},"dashboard":` + v2 + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			db, handler := variableTestHandler(t)
			imported, err := grafana.Import([]byte(source))
			require.NoError(t, err)
			d, err := db.SaveDashboard(context.Background(), imported.Dashboard)
			require.NoError(t, err)
			baseline, host := variableBrowserCopy(t, d.Variables[0])
			assert.Contains(t, string(baseline), "9007199254740992")
			host["name"], host["query"], host["current"] = rawValue("service"), rawValue("label_values(service)"), rawValue("beta|alpha")
			config, err := variableObject(host["config"])
			require.NoError(t, err)
			config["name"] = rawValue("service")
			query, err := variableObject(config["query"])
			require.NoError(t, err)
			query["query"] = rawValue("label_values(service)")
			config["query"] = rawValue(query)
			current, err := variableObject(config["current"])
			require.NoError(t, err)
			current["value"], current["text"] = rawValue([]string{"beta", "alpha"}), rawValue([]string{"Beta", "Alpha"})
			config["current"] = rawValue(current)
			config["hide"], config["refresh"], config["sort"] = rawValue(1), rawValue(2), rawValue(5)
			config["datasource"] = rawValue(rawObject{"uid": rawValue("alternate"), "type": rawValue("mysql")})
			host["config"] = rawValue(config)
			intervalBaseline, interval := variableBrowserCopy(t, d.Variables[2])
			body := map[string]any{"variables": []any{
				map[string]any{"originalName": "interval", "baseline": intervalBaseline, "variable": interval},
				map[string]any{"originalName": "host", "baseline": baseline, "variable": host},
				map[string]any{"variable": model.Variable{Name: "constant", Type: "constant", Query: "233", Options: []string{}}},
			}}
			response := variablePATCH(handler, d, body, `"`+strconv.FormatInt(d.UpdatedAt, 10)+`"`)
			require.Equal(t, 200, response.Code, response.Body.String())
			saved, err := db.Dashboard(context.Background(), d.ID)
			require.NoError(t, err)
			require.Len(t, saved.Variables, 3)
			assert.Equal(t, []string{"interval", "service", "constant"}, []string{saved.Variables[0].Name, saved.Variables[1].Name, saved.Variables[2].Name})
			assert.Equal(t, "label_values(service)", saved.Variables[1].Query)
			assert.Equal(t, "beta|alpha", saved.Variables[1].Current)
			assert.Contains(t, string(saved.Variables[1].Config), "9007199254740993")
			assert.Contains(t, string(saved.Variables[1].Config), `"text":["Beta","Alpha"]`)
			assert.Equal(t, "233", saved.Variables[2].Current)
			assert.Equal(t, d.Panels, saved.Panels)
			assert.Contains(t, string(saved.Grafana), "1e10000")
			assert.NotContains(t, string(saved.Grafana), "9007199254740992")
			roundtrip, err := grafana.Import(saved.Grafana)
			require.NoError(t, err)
			assert.Equal(t, "service", roundtrip.Dashboard.Variables[1].Name)
			assert.Equal(t, "query", roundtrip.Dashboard.Variables[1].Type)
			assert.Equal(t, "label_values(service)", roundtrip.Dashboard.Variables[1].Query)
			if name == "v2" || name == "v2-wrapper" {
				assert.Contains(t, string(saved.Grafana), `"hide":"hideLabel"`)
				assert.Contains(t, string(saved.Grafana), `"refresh":"onTimeRangeChanged"`)
				assert.Contains(t, string(saved.Grafana), `"sort":"alphabeticalCaseInsensitiveAsc"`)
				assert.Contains(t, string(saved.Grafana), `"group":"mysql"`)
				assert.Contains(t, string(saved.Grafana), `"name":"alternate"`)
			}
			history, err := db.DashboardVersions(context.Background(), d.ID, 1000, 0, "")
			require.NoError(t, err)
			assert.Len(t, history.Versions, 2)
			assert.Equal(t, 409, variablePATCH(handler, d, body, strconv.FormatInt(d.UpdatedAt, 10)).Code)
			history, err = db.DashboardVersions(context.Background(), d.ID, 1000, 0, "")
			require.NoError(t, err)
			assert.Len(t, history.Versions, 2)
		})
	}
}

func TestDashboardVariablesNativeCLISemanticsAndValidationAreAtomic(t *testing.T) {
	db, handler := variableTestHandler(t)
	_, err := db.SaveLibraryElement(context.Background(), model.LibraryElement{UID: "shared-variables", Name: "Shared", Kind: 1, Model: json.RawMessage(`{"type":"text","title":"Shared","options":{"content":"$host"},"opaque":9007199254740993}`)}, true)
	require.NoError(t, err)
	d, err := db.SaveDashboard(context.Background(), model.Dashboard{ID: "native-variables", Name: "Native", Panels: []model.Panel{{ID: "shared", Title: "Shared", Aggregation: "last", Config: json.RawMessage(`{"id":1,"type":"text","title":"Shared","libraryPanel":{"uid":"shared-variables"},"opaque":9007199254740993}`)}}, Variables: []model.Variable{{Name: "host", Type: "custom", Query: "a,b", Current: "a", Options: []string{"a", "b"}}}})
	require.NoError(t, err)
	libraryBefore, err := db.LibraryElement(context.Background(), "shared-variables")
	require.NoError(t, err)
	baseline, variable := variableBrowserCopy(t, d.Variables[0])
	variable["name"], variable["current"], variable["multi"] = rawValue("service"), rawValue("b|a"), rawValue(true)
	body := map[string]any{"variables": []any{map[string]any{"originalName": "host", "baseline": baseline, "variable": variable}}}
	w := variablePATCH(handler, d, body, strconv.FormatInt(d.UpdatedAt, 10))
	require.Equal(t, 200, w.Code, w.Body.String())
	saved, err := db.Dashboard(context.Background(), d.ID)
	require.NoError(t, err)
	assert.Equal(t, "service", saved.Variables[0].Name)
	assert.Equal(t, "b|a", saved.Variables[0].Current)
	assert.Contains(t, string(saved.Variables[0].Config), `"value":["b","a"]`)
	assert.Equal(t, []string{"a", "b"}, saved.Variables[0].Options)
	assert.Equal(t, d.Panels, saved.Panels)
	currentBaseline, current := variableBrowserCopy(t, saved.Variables[0])
	invalidBaseline := append(json.RawMessage(nil), currentBaseline...)
	var wrong rawObject
	require.NoError(t, json.Unmarshal(invalidBaseline, &wrong))
	wrong["query"] = rawValue("changed outside the editor")
	for _, item := range []struct {
		match  string
		body   any
		status int
	}{
		{"", map[string]any{"variables": []any{}}, 428},
		{"*", map[string]any{"variables": []any{}}, 400},
		{strconv.FormatInt(saved.UpdatedAt, 10), map[string]any{}, 400},
		{strconv.FormatInt(saved.UpdatedAt, 10), map[string]any{"variables": nil}, 400},
		{strconv.FormatInt(saved.UpdatedAt, 10), map[string]any{"variables": []any{map[string]any{"originalName": "service", "baseline": wrong, "variable": current}}}, 400},
		{strconv.FormatInt(saved.UpdatedAt, 10), map[string]any{"variables": []any{map[string]any{"variable": model.Variable{Name: "invalid-name", Type: "query"}}}}, 400},
		{strconv.FormatInt(saved.UpdatedAt, 10), map[string]any{"variables": []any{map[string]any{"variable": model.Variable{Name: "x", Type: "unsupported"}}}}, 400},
		{strconv.FormatInt(saved.UpdatedAt, 10), map[string]any{"variables": []any{map[string]any{"variable": model.Variable{Name: "x", Type: "custom", Config: rawValue(nil)}}}}, 400},
		{strconv.FormatInt(saved.UpdatedAt, 10), map[string]any{"variables": []any{map[string]any{"originalName": "service", "baseline": currentBaseline, "variable": current}, map[string]any{"originalName": "service", "baseline": currentBaseline, "variable": current}}}, 400},
		{strconv.FormatInt(saved.UpdatedAt, 10), map[string]any{"variables": []any{map[string]any{"variable": model.Variable{Name: "x", Type: "custom"}}, map[string]any{"variable": model.Variable{Name: "x", Type: "query"}}}}, 400},
	} {
		w := variablePATCH(handler, saved, item.body, item.match)
		assert.Equal(t, item.status, w.Code, w.Body.String())
	}
	untouched, err := db.Dashboard(context.Background(), d.ID)
	require.NoError(t, err)
	assert.Equal(t, saved, untouched)
	history, err := db.DashboardVersions(context.Background(), d.ID, 1000, 0, "")
	require.NoError(t, err)
	assert.Len(t, history.Versions, 2)
	w = variablePATCH(handler, saved, map[string]any{"variables": []any{}}, strconv.FormatInt(saved.UpdatedAt, 10))
	require.Equal(t, 200, w.Code, w.Body.String())
	deleted, err := db.Dashboard(context.Background(), d.ID)
	require.NoError(t, err)
	assert.Empty(t, deleted.Variables)
	libraryAfter, err := db.LibraryElement(context.Background(), "shared-variables")
	require.NoError(t, err)
	assert.Equal(t, libraryBefore, libraryAfter)
}

func TestDashboardVariablesCLITopLevelEditsPreserveSQLObjectAndOptionLabels(t *testing.T) {
	d := model.Dashboard{Name: "CLI", Panels: []model.Panel{}, Variables: []model.Variable{{Name: "host", Type: "query", Query: "SELECT 1", Current: "a", Multi: true, Options: []string{"a", "b"}, Config: json.RawMessage(`{"name":"host","type":"query","multi":true,"query":{"rawSql":"SELECT 1","opaque":9007199254740993},"current":{"value":["a"],"opaque":9007199254740993},"options":[{"value":"a","text":"Alpha","opaque":9007199254740993},{"value":"b","text":"Beta"}]}`)}}}
	baseline, variable := variableBrowserCopy(t, d.Variables[0])
	variable["query"], variable["current"], variable["options"], variable["include_all"] = rawValue("SELECT 2"), rawValue("b|a"), rawValue([]string{"b", "a"}), rawValue(true)
	name := "host"
	saved, err := applyDashboardVariables(d, []dashboardVariableDraft{{OriginalName: &name, Baseline: baseline, Variable: rawValue(variable)}})
	require.NoError(t, err)
	assert.Equal(t, "SELECT 2", saved.Variables[0].Query)
	assert.True(t, saved.Variables[0].IncludeAll)
	assert.Contains(t, string(saved.Variables[0].Config), `"rawSql":"SELECT 2"`)
	assert.Contains(t, string(saved.Variables[0].Config), `"value":["b","a"]`)
	assert.Contains(t, string(saved.Variables[0].Config), `"text":"Alpha"`)
	assert.Contains(t, string(saved.Variables[0].Config), "9007199254740993")
	assert.NotContains(t, string(saved.Variables[0].Config), "9007199254740992")
	drafts := make([]dashboardVariableDraft, 33)
	_, err = applyDashboardVariables(d, drafts)
	require.ErrorContains(t, err, "at most 32")
}

func TestDashboardVariablesV2NewKindsAndEnvelopeReferences(t *testing.T) {
	imported, err := grafana.Import([]byte(`{"apiVersion":"dashboard.grafana.app/v2beta1","metadata":{"name":"new-variables"},"spec":{"title":"V2","elements":{"a":{"kind":"Panel","spec":{"id":1,"title":"One","vizConfig":{"group":"text","spec":{"options":{"content":"233"}}}}}},"variables":[{"kind":"AdhocVariable","group":"prometheus","datasource":{"name":"metricspanel","opaque":9007199254740993},"opaque":1e10000,"spec":{"name":"filters","filters":[{"key":"service","operator":"=","value":"orders","opaque":9007199254740993}],"baseFilters":[],"defaultKeys":[]}}]}}`))
	require.NoError(t, err)
	d := imported.Dashboard
	baseline, filters := variableBrowserCopy(t, d.Variables[0])
	config, err := variableObject(filters["config"])
	require.NoError(t, err)
	config["datasource"] = rawValue(rawObject{"uid": rawValue("external"), "type": rawValue("loki")})
	filters["config"] = rawValue(config)
	name := "filters"
	drafts := []dashboardVariableDraft{{OriginalName: &name, Baseline: baseline, Variable: rawValue(filters)}}
	for _, kind := range []string{"query", "custom", "constant", "textbox", "datasource", "interval", "adhoc", "groupby"} {
		drafts = append(drafts, dashboardVariableDraft{Variable: rawValue(model.Variable{Name: "new_" + kind, Type: kind, Query: "233", Options: []string{}})})
	}
	saved, err := applyDashboardVariables(d, drafts)
	require.NoError(t, err)
	assert.Contains(t, string(saved.Grafana), `"group":"loki"`)
	assert.Contains(t, string(saved.Grafana), `"name":"external","opaque":9007199254740993`)
	assert.Contains(t, string(saved.Grafana), "1e10000")
	assert.NotContains(t, string(saved.Grafana), "9007199254740992")
	var resource struct {
		Spec struct {
			Variables []struct {
				Kind string
				Spec rawObject
			}
		}
	}
	require.NoError(t, json.Unmarshal(saved.Grafana, &resource))
	require.Len(t, resource.Spec.Variables, 9)
	var query rawObject
	require.NoError(t, json.Unmarshal(resource.Spec.Variables[1].Spec["query"], &query))
	assert.Equal(t, "DataQuery", rawString(query["kind"]))
	assert.Equal(t, "prometheus", rawString(query["group"]))
	assert.Equal(t, "233", rawString(query["spec"]))
	assert.Empty(t, resource.Spec.Variables[3].Spec["options"])
	assert.Empty(t, resource.Spec.Variables[3].Spec["multi"])
	assert.Equal(t, "233", rawString(resource.Spec.Variables[5].Spec["pluginId"]))
	assert.Empty(t, resource.Spec.Variables[5].Spec["query"])
	assert.Equal(t, "onTimeRangeChanged", rawString(resource.Spec.Variables[6].Spec["refresh"]))
	assert.Equal(t, rawValue(30), resource.Spec.Variables[6].Spec["auto_count"])
	assert.Equal(t, rawValue([]any{}), resource.Spec.Variables[7].Spec["baseFilters"])
	assert.Empty(t, resource.Spec.Variables[7].Spec["query"])
	for i, kind := range []string{"query", "custom", "constant", "textbox", "datasource", "interval", "adhoc", "groupby"} {
		assert.Equal(t, variableKinds[kind], resource.Spec.Variables[i+1].Kind)
		assert.Equal(t, "dontHide", rawString(resource.Spec.Variables[i+1].Spec["hide"]))
	}
	_, err = grafana.Import(saved.Grafana)
	require.NoError(t, err)
}
