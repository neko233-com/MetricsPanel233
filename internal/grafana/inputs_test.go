package grafana_test

import (
	"encoding/json"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/grafana"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBindInputValuesAreLiteralAcrossDeclaredNames(t *testing.T) {
	source := `{"title":"Literal constants","__inputs":[{"name":"A","type":"constant"},{"name":"B","type":"constant"}],"panels":[{"type":"text","title":"${A}","options":{"content":"${A} / ${B}"}}]}`
	bound, err := grafana.BindDashboard([]byte(source), map[string]string{"A": "${B}", "B": "chosen"}, "", "")
	require.NoError(t, err)
	var doc struct {
		Inputs []struct{ Name, Value string } `json:"__inputs"`
		Panels []struct {
			Title   string
			Options struct{ Content string }
		}
	}
	require.NoError(t, json.Unmarshal(bound, &doc))
	assert.Equal(t, "${B}", doc.Inputs[0].Value, "retained input values must not expand another input")
	assert.Equal(t, "chosen", doc.Inputs[1].Value)
	assert.Equal(t, "${B}", doc.Panels[0].Title)
	assert.Equal(t, "${B} / chosen", doc.Panels[0].Options.Content)
}

func TestBindExportedDatasourceInputsRemapOnlyReferences(t *testing.T) {
	classic := `{"title":"Bound export","__inputs":[{"name":"DS_PROM","type":"datasource","pluginId":"prometheus"}],"future":{"datasource":{"uid":"old-source","type":"prometheus"},"value":"old-source"},"panels":[{"type":"timeseries","datasource":{"uid":"${DS_PROM}","type":"prometheus","opaque":"old-source"},"targets":[{"datasource":"${DS_PROM}","expr":"old-source","future":{"datasource":{"uid":"old-source","type":"prometheus"}}}],"panels":[{"type":"stat","datasource":{"uid":"${DS_PROM}","type":"prometheus"}}]}],"rows":[{"panels":[{"type":"stat","datasource":"${DS_PROM}"}]}],"templating":{"list":[{"name":"source","type":"datasource","query":"prometheus","current":{"value":"old-source","text":"old-source","opaque":233},"options":[{"value":"old-source","text":"old-source","selected":true,"opaque":233}]},{"name":"job","type":"query","datasource":{"uid":"${DS_PROM}","type":"prometheus"},"query":"old-source"}]},"annotations":{"list":[{"datasource":{"uid":"${DS_PROM}","type":"prometheus"},"target":{"expr":"old-source"}}]}}`
	v2 := `{"apiVersion":"dashboard.grafana.app/v2beta1","kind":"Dashboard","metadata":{"name":"bound"},"spec":{"title":"Bound export","__inputs":[{"name":"DS_PROM","type":"datasource","pluginId":"prometheus"}],"future":{"datasource":{"name":"old-source"},"value":"old-source"},"elements":{"load":{"kind":"Panel","spec":{"title":"Load","data":{"kind":"QueryGroup","spec":{"queries":[{"kind":"PanelQuery","spec":{"refId":"A","query":{"kind":"DataQuery","group":"prometheus","datasource":{"name":"${DS_PROM}","opaque":"old-source"},"spec":{"expr":"old-source","future":{"kind":"DataQuery","group":"prometheus","datasource":{"name":"old-source"}}}}}}]}},"vizConfig":{"kind":"VizConfig","group":"stat","spec":{}}}}},"variables":[{"kind":"DatasourceVariable","spec":{"name":"source","query":"prometheus","current":{"value":"old-source","text":"old-source","opaque":233},"options":[{"value":"old-source","text":"old-source","selected":true,"opaque":233}]}},{"kind":"QueryVariable","spec":{"name":"job","query":{"kind":"DataQuery","group":"prometheus","datasource":{"name":"${DS_PROM}"},"spec":{"query":"old-source"}}}},{"kind":"AdhocVariable","group":"prometheus","datasource":{"name":"${DS_PROM}"},"spec":{"name":"filters","opaque":"old-source"}},{"kind":"GroupByVariable","group":"prometheus","datasource":{"name":"${DS_PROM}"},"spec":{"name":"group","opaque":"old-source"}}],"annotations":[{"kind":"AnnotationQuery","spec":{"query":{"kind":"DataQuery","group":"prometheus","datasource":{"name":"${DS_PROM}"},"spec":{"expr":"old-source"}}}}]}}`
	for _, test := range []struct{ name, source string }{
		{"classic", classic},
		{"v1", `{"apiVersion":"dashboard.grafana.app/v1beta1","kind":"Dashboard","metadata":{"name":"bound"},"spec":` + classic + `}`},
		{"v2", v2},
	} {
		t.Run(test.name, func(t *testing.T) {
			exported, err := grafana.BindDashboard([]byte(test.source), map[string]string{"DS_PROM": "old-source"}, "", "")
			require.NoError(t, err)
			bound, err := grafana.BindDashboard(exported, map[string]string{"DS_PROM": "new-source"}, "", "")
			require.NoError(t, err)
			var root map[string]any
			require.NoError(t, json.Unmarshal(bound, &root))
			doc := root
			if spec, ok := root["spec"].(map[string]any); ok {
				doc = spec
			}
			assert.Equal(t, "new-source", doc["__inputs"].([]any)[0].(map[string]any)["value"])
			assert.Equal(t, "old-source", doc["future"].(map[string]any)["value"])
			var variable map[string]any
			if test.name == "v2" {
				panel := doc["elements"].(map[string]any)["load"].(map[string]any)["spec"].(map[string]any)
				query := panel["data"].(map[string]any)["spec"].(map[string]any)["queries"].([]any)[0].(map[string]any)["spec"].(map[string]any)["query"].(map[string]any)
				assert.Equal(t, "new-source", query["datasource"].(map[string]any)["name"])
				assert.Equal(t, "old-source", query["datasource"].(map[string]any)["opaque"])
				assert.Equal(t, "old-source", query["spec"].(map[string]any)["expr"])
				assert.Equal(t, "old-source", query["spec"].(map[string]any)["future"].(map[string]any)["datasource"].(map[string]any)["name"])
				variables := doc["variables"].([]any)
				variable = variables[0].(map[string]any)["spec"].(map[string]any)
				query = variables[1].(map[string]any)["spec"].(map[string]any)["query"].(map[string]any)
				assert.Equal(t, "new-source", query["datasource"].(map[string]any)["name"])
				assert.Equal(t, "old-source", query["spec"].(map[string]any)["query"])
				for _, raw := range variables[2:] {
					assert.Equal(t, "new-source", raw.(map[string]any)["datasource"].(map[string]any)["name"])
				}
				query = doc["annotations"].([]any)[0].(map[string]any)["spec"].(map[string]any)["query"].(map[string]any)
				assert.Equal(t, "new-source", query["datasource"].(map[string]any)["name"])
				assert.Equal(t, "old-source", doc["future"].(map[string]any)["datasource"].(map[string]any)["name"])
			} else {
				panel := doc["panels"].([]any)[0].(map[string]any)
				assert.Equal(t, "new-source", panel["datasource"].(map[string]any)["uid"])
				assert.Equal(t, "old-source", panel["datasource"].(map[string]any)["opaque"])
				target := panel["targets"].([]any)[0].(map[string]any)
				assert.Equal(t, "new-source", target["datasource"])
				assert.Equal(t, "old-source", target["expr"])
				assert.Equal(t, "old-source", target["future"].(map[string]any)["datasource"].(map[string]any)["uid"])
				assert.Equal(t, "new-source", panel["panels"].([]any)[0].(map[string]any)["datasource"].(map[string]any)["uid"])
				assert.Equal(t, "new-source", doc["rows"].([]any)[0].(map[string]any)["panels"].([]any)[0].(map[string]any)["datasource"])
				variables := doc["templating"].(map[string]any)["list"].([]any)
				variable = variables[0].(map[string]any)
				assert.Equal(t, "new-source", variables[1].(map[string]any)["datasource"].(map[string]any)["uid"])
				assert.Equal(t, "old-source", variables[1].(map[string]any)["query"])
				assert.Equal(t, "new-source", doc["annotations"].(map[string]any)["list"].([]any)[0].(map[string]any)["datasource"].(map[string]any)["uid"])
				assert.Equal(t, "old-source", doc["future"].(map[string]any)["datasource"].(map[string]any)["uid"])
			}
			current := variable["current"].(map[string]any)
			assert.Equal(t, "new-source", current["value"])
			assert.Equal(t, "new-source", current["text"])
			assert.Equal(t, float64(233), current["opaque"])
			selected := []string{}
			for _, raw := range variable["options"].([]any) {
				option := raw.(map[string]any)
				if option["selected"] == true {
					selected = append(selected, option["value"].(string))
				}
			}
			assert.Equal(t, []string{"new-source"}, selected)
		})
	}
}

func TestBindExportedDatasourceInputsRejectAmbiguousReferences(t *testing.T) {
	definitions := `"__inputs":[{"name":"DS_A","type":"datasource","pluginId":"prometheus","value":"shared"},{"name":"DS_B","type":"datasource","pluginId":"prometheus","value":"shared"}]`
	bindings := map[string]string{"DS_A": "east", "DS_B": "west"}
	_, err := grafana.BindDashboard([]byte(`{"title":"Ambiguous",`+definitions+`,"panels":[{"type":"stat","datasource":{"type":"prometheus","uid":"shared"}}]}`), bindings, "", "")
	require.ErrorContains(t, err, "ambiguous")
	_, err = grafana.BindDashboard([]byte(`{"title":"One unchanged",`+definitions+`,"panels":[{"type":"stat","datasource":"shared"}]}`), map[string]string{"DS_A": "east", "DS_B": "shared"}, "", "")
	require.ErrorContains(t, err, "ambiguous")
	bound, err := grafana.BindDashboard([]byte(`{"title":"Explicit",`+definitions+`,"future":"shared","panels":[{"type":"stat","datasource":{"type":"prometheus","uid":"${DS_A}"}},{"type":"stat","datasource":{"type":"prometheus","uid":"${DS_B}"}}]}`), bindings, "", "")
	require.NoError(t, err)
	assert.Contains(t, string(bound), `"uid":"east"`)
	assert.Contains(t, string(bound), `"uid":"west"`)
	assert.Contains(t, string(bound), `"future":"shared"`)
	v2 := `{"title":"Labeled","__inputs":[{"name":"DS_A","type":"datasource","pluginId":"prometheus","value":"shared"},{"name":"DS_B","type":"datasource","pluginId":"prometheus","value":"shared"}],"elements":{"panel":{"kind":"Panel","spec":{"data":{"kind":"QueryGroup","spec":{"queries":[{"kind":"PanelQuery","spec":{"query":{"kind":"DataQuery","group":"prometheus","datasource":{"name":"shared"},"labels":{"grafana.app/export-label":"DS_A"},"spec":{"expr":"shared"}}}},{"kind":"PanelQuery","spec":{"query":{"kind":"DataQuery","group":"prometheus","datasource":{"name":"shared"},"labels":{"grafana.app/export-label":"DS_B"},"spec":{"expr":"shared"}}}}]}}}}}}`
	bound, err = grafana.BindDashboard([]byte(v2), bindings, "", "")
	require.NoError(t, err)
	assert.Contains(t, string(bound), `"name":"east"`)
	assert.Contains(t, string(bound), `"name":"west"`)
	assert.Contains(t, string(bound), `"expr":"shared"`)
	bound, err = grafana.BindDashboard([]byte(`{"title":"Same choice",`+definitions+`,"panels":[{"type":"stat","datasource":"shared"}]}`), map[string]string{"DS_A": "east", "DS_B": "east"}, "", "")
	require.NoError(t, err)
	assert.Contains(t, string(bound), `"datasource":"east"`)
}

func TestBindExportedDatasourceInputsRespectTypeAndDoNotCascade(t *testing.T) {
	source := `{"title":"Typed sources","__inputs":[{"name":"DS_P","type":"datasource","pluginId":"prometheus","value":"old"},{"name":"DS_M","type":"datasource","pluginId":"mysql","value":"old"},{"name":"DS_NEXT","type":"datasource","pluginId":"prometheus","value":"next"}],"panels":[{"type":"stat","datasource":{"type":"prometheus","uid":"old"},"targets":[{"datasource":{"type":"mysql","uid":"old"}},{"datasource":{"type":"prometheus","uid":"next"}}]}]}`
	bindings := map[string]string{"DS_P": "next", "DS_M": "mysql-new", "DS_NEXT": "final"}
	bound, err := grafana.BindDashboard([]byte(source), bindings, "", "")
	require.NoError(t, err)
	var doc struct {
		Panels []struct {
			Datasource struct{ UID string }
			Targets    []struct{ Datasource struct{ UID string } }
		}
	}
	require.NoError(t, json.Unmarshal(bound, &doc))
	assert.Equal(t, "next", doc.Panels[0].Datasource.UID)
	assert.Equal(t, "mysql-new", doc.Panels[0].Targets[0].Datasource.UID)
	assert.Equal(t, "final", doc.Panels[0].Targets[1].Datasource.UID)
	_, err = grafana.BindDashboard([]byte(`{"title":"Untyped","__inputs":[{"name":"DS_P","type":"datasource","pluginId":"prometheus","value":"old"},{"name":"DS_M","type":"datasource","pluginId":"mysql","value":"old"}],"panels":[{"type":"stat","datasource":"old"}]}`), map[string]string{"DS_P": "prom-new", "DS_M": "mysql-new"}, "", "")
	require.ErrorContains(t, err, "ambiguous")
}

func TestBindExportedDatasourceInputsKeepOpaqueVariableAndDefaultReferences(t *testing.T) {
	source := `{"title":"Preserved objects","__inputs":[{"name":"DS_PROM","type":"datasource","pluginId":"prometheus","value":"old"}],"future":{"name":"source","type":"datasource","query":"prometheus","current":{"value":"old"}},"panels":[{"type":"stat","datasource":{"uid":"old","type":"prometheus"}}],"templating":{"list":[{"name":"source","type":"datasource","query":"prometheus","current":{"value":"old"}},{"name":"text","type":"query","query":"old","current":{"value":"old"}}]}}`
	bound, err := grafana.BindDashboard([]byte(source), map[string]string{"DS_PROM": "new"}, "", "")
	require.NoError(t, err)
	var root map[string]any
	require.NoError(t, json.Unmarshal(bound, &root))
	assert.Equal(t, "old", root["future"].(map[string]any)["current"].(map[string]any)["value"])
	variables := root["templating"].(map[string]any)["list"].([]any)
	assert.Equal(t, "new", variables[0].(map[string]any)["current"].(map[string]any)["value"])
	assert.Equal(t, "old", variables[1].(map[string]any)["current"].(map[string]any)["value"])
	bound, err = grafana.BindDashboard([]byte(`{"title":"Unset input","__inputs":[{"name":"DS_PROM","type":"datasource","pluginId":"prometheus","value":""}],"panels":[{"type":"stat","datasource":{"type":"prometheus"}}]}`), map[string]string{"DS_PROM": "new"}, "", "")
	require.NoError(t, err)
	assert.Contains(t, string(bound), `"datasource":{"type":"prometheus"}`)
}

func TestBindExportedV2DatasourceUsesGroupAndKeepsUnknownReferenceType(t *testing.T) {
	source := `{"title":"Future reference fields","__inputs":[{"name":"DS_PROM","type":"datasource","pluginId":"prometheus","value":"old"}],"elements":{"panel":{"kind":"Panel","spec":{"data":{"kind":"QueryGroup","spec":{"queries":[{"kind":"PanelQuery","spec":{"query":{"kind":"DataQuery","group":"prometheus","datasource":{"name":"old","type":"future-reference-type"},"spec":{"expr":"old"}}}}]}}}}}}`
	bound, err := grafana.BindDashboard([]byte(source), map[string]string{"DS_PROM": "new"}, "", "")
	require.NoError(t, err)
	assert.Contains(t, string(bound), `"name":"new"`)
	assert.Contains(t, string(bound), `"type":"future-reference-type"`)
	assert.Contains(t, string(bound), `"expr":"old"`)
}

func TestV2DatasourceVariableUsesPluginIDAndRetainsOriginalSpec(t *testing.T) {
	source := `{"apiVersion":"dashboard.grafana.app/v2beta1","kind":"Dashboard","metadata":{"name":"source-variables"},"spec":{"title":"Source variables","elements":{"panel":{"kind":"Panel","spec":{"title":"Panel","data":{"kind":"QueryGroup","spec":{"queries":[]}},"vizConfig":{"kind":"VizConfig","group":"stat","spec":{}}}}},"variables":[{"kind":"DatasourceVariable","spec":{"name":"database","pluginId":"mysql","query":"prometheus","current":{"value":"mysql-old","text":"MySQL","opaque":233},"options":[],"future":"retained"}},{"kind":"DatasourceVariable","spec":{"name":"legacy","query":"redis","current":{"value":"redis-old"}}},{"kind":"DatasourceVariable","spec":{"name":"metrics","pluginId":"prometheus","current":{"value":"prom-old"}}}]}}`
	_, inputs, err := grafana.PreviewInputs([]byte(source))
	require.NoError(t, err)
	require.Len(t, inputs, 3)
	assert.Equal(t, "mysql", inputs[0].PluginID)
	assert.Equal(t, "redis", inputs[1].PluginID)
	assert.Equal(t, "prometheus", inputs[2].PluginID)
	bound, err := grafana.BindDashboard([]byte(source), map[string]string{"database": "mysql-new"}, "", "")
	require.NoError(t, err)
	var resource struct {
		Spec struct {
			Variables []struct {
				Spec struct {
					PluginID string `json:"pluginId"`
					Query    string
					Current  struct {
						Value  string
						Opaque int
					}
					Future string
				}
			}
		}
	}
	require.NoError(t, json.Unmarshal(bound, &resource))
	assert.Equal(t, "mysql", resource.Spec.Variables[0].Spec.PluginID)
	assert.Equal(t, "prometheus", resource.Spec.Variables[0].Spec.Query, "binding must retain the original V2 spec")
	assert.Equal(t, "mysql-new", resource.Spec.Variables[0].Spec.Current.Value)
	assert.Equal(t, 233, resource.Spec.Variables[0].Spec.Current.Opaque)
	assert.Equal(t, "retained", resource.Spec.Variables[0].Spec.Future)
	result, err := grafana.Import(bound)
	require.NoError(t, err)
	require.Len(t, result.Dashboard.Variables, 3)
	assert.Equal(t, "mysql", result.Dashboard.Variables[0].Query)
	assert.Equal(t, "mysql-new", result.Dashboard.Variables[0].Current)
	assert.Equal(t, "redis", result.Dashboard.Variables[1].Query)
	assert.Equal(t, "prometheus", result.Dashboard.Variables[2].Query)
	assert.JSONEq(t, string(bound), string(result.Dashboard.Grafana))
}
