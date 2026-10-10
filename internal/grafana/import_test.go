package grafana_test

import (
	"encoding/json"
	"github.com/neko233-com/MetricsPanel233/internal/grafana"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
)

func TestCommunityNodeExporterFullTemplate(t *testing.T) {
	data, err := os.ReadFile("testdata/node-exporter-full.json")
	require.NoError(t, err)
	r, err := grafana.Import(data)
	require.NoError(t, err)
	assert.Equal(t, "Node Exporter Full", r.Dashboard.Name)
	assert.Greater(t, len(r.Dashboard.Panels), 100)
	assert.JSONEq(t, string(data), string(r.Dashboard.Grafana))
	for _, panel := range r.Dashboard.Panels {
		assert.NotEmpty(t, panel.Config)
	}
}

func TestClassicDashboardImportPreservesOriginalAndVariables(t *testing.T) {
	data := []byte(`{"title":"MySQL","panels":[{"id":1,"title":"Connections","type":"timeseries","targets":[{"expr":"mysql_global_status_threads_connected{job=\"$job\"}"}]},{"type":"row","panels":[{"title":"QPS","type":"stat","targets":[{"expr":"sum(rate(mysql_global_status_questions[5m]))"}]}]},{"title":"Plugin","type":"third-party-plugin"}],"templating":{"list":[{"name":"job","type":"query","query":{"query":"label_values(mysql_up,job)"},"current":{"value":"mysql"},"includeAll":true}]}}`)
	result, err := grafana.Import(data)
	require.NoError(t, err)
	require.Len(t, result.Dashboard.Panels, 4)
	assert.Equal(t, "stat", result.Dashboard.Panels[2].Visualization)
	assert.Contains(t, result.Warnings[0], "third-party-plugin")
	assert.JSONEq(t, string(data), string(result.Dashboard.Grafana))
	require.Len(t, result.Dashboard.Variables, 1)
	assert.Equal(t, "mysql", result.Dashboard.Variables[0].Current)
}
func TestUnsupportedAndMalformedTemplates(t *testing.T) {
	for _, data := range []string{"not json", `{}`, `{"title":"Empty","panels":[]}`} {
		_, err := grafana.Import([]byte(data))
		require.Error(t, err)
	}
}

func TestPercentUnitUsesOriginalQueryAndOfficialDisplayFormat(t *testing.T) {
	r, err := grafana.Import([]byte(`{"title":"Ratio","panels":[{"title":"Usage","type":"gauge","targets":[{"expr":"cpu_ratio"}],"fieldConfig":{"defaults":{"unit":"percentunit"}}}]}`))
	require.NoError(t, err)
	assert.Equal(t, "cpu_ratio", r.Dashboard.Panels[0].Expr)
	assert.Equal(t, "percent", r.Dashboard.Panels[0].Unit)
	require.Empty(t, r.Warnings)
	assert.Equal(t, "gauge", r.Dashboard.Panels[0].Visualization)
	var config map[string]any
	require.NoError(t, json.Unmarshal(r.Dashboard.Panels[0].Config, &config))
	assert.Equal(t, "percentunit", config["fieldConfig"].(map[string]any)["defaults"].(map[string]any)["unit"])
}

func TestResourceSchemasAndLosslessContracts(t *testing.T) {
	classic := `{"title":"Resource","panels":[{"id":1,"title":"Requests","type":"table","gridPos":{"x":6,"y":2,"w":18,"h":8},"targets":[{"refId":"B","expr":"up","instant":true,"format":"table","legendFormat":"{{job}}"}],"transformations":[{"id":"organize","options":{"renameByName":{"Value #B":"Healthy"}}}],"options":{"showHeader":true},"repeat":"job"}],"templating":{"list":[{"name":"job","type":"query","query":"label_values(up,job)","regex":"/mysql.*/","sort":1,"includeAll":true,"allValue":"mysql.*","current":{"value":"$__all"}}]}}`
	for _, source := range []string{classic, `{"apiVersion":"dashboard.grafana.app/v1beta1","kind":"Dashboard","metadata":{"name":"resource"},"spec":` + classic + `}`} {
		r, err := grafana.Import([]byte(source))
		require.NoError(t, err)
		require.Len(t, r.Dashboard.Panels, 1)
		assert.JSONEq(t, source, string(r.Dashboard.Grafana))
		assert.Contains(t, string(r.Dashboard.Panels[0].Config), `"renameByName"`)
		assert.Equal(t, "$__all", r.Dashboard.Variables[0].Current)
		assert.Contains(t, string(r.Dashboard.Variables[0].Config), `"allValue"`)
	}
	v2 := `{"apiVersion":"dashboard.grafana.app/v2beta1","kind":"Dashboard","metadata":{"name":"v2"},"spec":{"title":"V2","elements":{"table":{"kind":"Panel","spec":{"id":3,"title":"Health","data":{"kind":"QueryGroup","spec":{"queries":[{"kind":"PanelQuery","spec":{"refId":"A","hidden":false,"query":{"kind":"DataQuery","group":"prometheus","spec":{"expr":"up","instant":true,"format":"table"}}}}],"transformations":[{"kind":"organize","spec":{"id":"organize","options":{"excludeByName":{"Time":true}}}}]}},"vizConfig":{"kind":"VizConfig","group":"table","spec":{"options":{},"fieldConfig":{"defaults":{"unit":"short"},"overrides":[]}}}}}},"layout":{"kind":"GridLayout","spec":{"items":[{"kind":"GridLayoutItem","spec":{"x":4,"y":2,"width":20,"height":7,"element":{"kind":"ElementReference","name":"table"}}}]}},"variables":[]}}`
	r, err := grafana.Import([]byte(v2))
	require.NoError(t, err)
	require.Len(t, r.Dashboard.Panels, 1)
	assert.Equal(t, "up", r.Dashboard.Panels[0].Expr)
	assert.JSONEq(t, v2, string(r.Dashboard.Grafana))
	assert.Contains(t, string(r.Dashboard.Panels[0].Config), `"w":20`)
	assert.Empty(t, r.Warnings)
}

func TestTimeOverridesSurviveClassicAndResourceImport(t *testing.T) {
	classic := `{"title":"Timing","refresh":"7s","timepicker":{"refresh_intervals":["7s","1m"]},"panels":[{"id":1,"title":"Past","type":"stat","timeFrom":"15m","timeShift":"$shift","hideTimeOverride":true,"compareWith":"1d","targets":[{"expr":"up","timeRangeCompare":false}]}]}`
	v2 := `{"apiVersion":"dashboard.grafana.app/v2beta1","kind":"Dashboard","metadata":{"name":"timing-v2"},"spec":{"title":"Timing","timeSettings":{"autoRefresh":"7s","autoRefreshIntervals":["7s","1m"]},"elements":{"past":{"kind":"Panel","spec":{"id":1,"title":"Past","data":{"kind":"QueryGroup","spec":{"queryOptions":{"timeFrom":"15m","timeShift":"$shift","hideTimeOverride":true,"compareWith":"1d"},"queries":[{"kind":"PanelQuery","spec":{"refId":"A","query":{"kind":"DataQuery","group":"prometheus","spec":{"expr":"up","timeRangeCompare":false}}}}]}},"vizConfig":{"kind":"VizConfig","group":"stat","spec":{}}}}},"layout":{"kind":"GridLayout","spec":{"items":[{"kind":"GridLayoutItem","spec":{"x":0,"y":0,"width":12,"height":8,"element":{"kind":"ElementReference","name":"past"}}}]}}}}`
	for name, source := range map[string]string{"classic": classic, "v1": `{"apiVersion":"dashboard.grafana.app/v1beta1","kind":"Dashboard","spec":` + classic + `}`, "v2": v2} {
		t.Run(name, func(t *testing.T) {
			result, err := grafana.Import([]byte(source))
			require.NoError(t, err)
			require.Len(t, result.Dashboard.Panels, 1)
			assert.JSONEq(t, source, string(result.Dashboard.Grafana))
			var config map[string]any
			require.NoError(t, json.Unmarshal(result.Dashboard.Panels[0].Config, &config))
			assert.Equal(t, "15m", config["timeFrom"])
			assert.Equal(t, "$shift", config["timeShift"])
			assert.Equal(t, true, config["hideTimeOverride"])
			assert.Equal(t, "1d", config["compareWith"])
			targets := config["targets"].([]any)
			require.Len(t, targets, 1)
			assert.Equal(t, false, targets[0].(map[string]any)["timeRangeCompare"])
		})
	}
}
