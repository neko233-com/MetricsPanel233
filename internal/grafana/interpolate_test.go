package grafana_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/grafana"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLegacyInterpolateOrderedWildcardAndLiteralInputs(t *testing.T) {
	source := `{"title":"Portable","__inputs":[{"name":"DS","type":"datasource","pluginId":"prometheus"},{"name":"PREFIX","type":"constant"},{"name":"B","type":"constant"},{"name":"EXPR","type":"datasource","pluginId":"__expr__"}],"__requires":[{"opaque":233}],"__elements":{"library":{"model":{"title":"${PREFIX}"}}},"panels":[{"type":"stat","datasource":{"uid":"${DS}"},"targets":[{"expr":"${PREFIX}${B} $job ${unknown}","datasource":"${EXPR}"}]}],"future":{"integer":9007199254740993,"__inputs":[{"opaque":true}]}}`
	for _, test := range []struct {
		name   string
		inputs []grafana.ImportInput
		uid    string
	}{
		{"wildcard-first", []grafana.ImportInput{{Name: "*", Type: "datasource", PluginID: "unrelated", Value: "wild"}, {Name: "DS", Type: "datasource", Value: "exact"}}, "wild"},
		{"exact-first", []grafana.ImportInput{{Name: "DS", Type: "datasource", Value: "exact"}, {Name: "*", Type: "datasource", Value: "wild"}}, "exact"},
	} {
		t.Run(test.name, func(t *testing.T) {
			inputs := append(test.inputs, grafana.ImportInput{Name: "PREFIX", Type: "constant", Value: `${B}$\"`}, grafana.ImportInput{Name: "B", Type: "constant", Value: "chosen"})
			result, err := grafana.Interpolate([]byte(source), inputs)
			require.NoError(t, err)
			assert.NotContains(t, string(result), `"__inputs"`)
			assert.Contains(t, string(result), `"__requires"`)
			assert.Contains(t, string(result), `"__elements"`)
			assert.Contains(t, string(result), `9007199254740993`)
			var doc struct {
				Panels []struct {
					Datasource struct{ UID string }
					Targets    []struct{ Expr, Datasource string }
				}
			}
			require.NoError(t, json.Unmarshal(result, &doc))
			assert.Equal(t, test.uid, doc.Panels[0].Datasource.UID)
			assert.Equal(t, `${B}$\"chosen $job ${unknown}`, doc.Panels[0].Targets[0].Expr)
			assert.Equal(t, "__expr__", doc.Panels[0].Targets[0].Datasource)
			clean, err := grafana.StripImportMetadata(result)
			require.NoError(t, err)
			assert.NotContains(t, string(clean), `"__requires"`)
			assert.NotContains(t, string(clean), `"__elements"`)
		})
	}
}

func TestLegacyInterpolateDoesNotAutoBindGrafanaOrWrongTypes(t *testing.T) {
	_, err := grafana.Interpolate([]byte(`{"title":"Builtin","__inputs":[{"name":"CORE","type":"datasource","pluginId":"grafana"}]}`), nil)
	require.True(t, errors.Is(err, grafana.ErrDashboardInputMissing))
	_, err = grafana.Interpolate([]byte(`{"title":"Typed","__inputs":[{"name":"X","type":"datasource"}]}`), []grafana.ImportInput{{Name: "*", Type: "constant", Value: "wrong"}})
	require.ErrorIs(t, err, grafana.ErrDashboardInputMissing)
	result, err := grafana.Interpolate([]byte(`{"title":"Empty","__inputs":[{"name":"X","type":"constant"}],"value":"${X}"}`), []grafana.ImportInput{{Name: "X", Type: "constant", Value: ""}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"title":"Empty","value":""}`, string(result))
}

func TestCommunityMetadataKeepsOfficialShape(t *testing.T) {
	metadata := `{"id":1860,"name":"Node Exporter Full","orgName":"Community","updatedAt":"2026-10-01T01:02:03Z","revision":45,"json":{"title":"Node","future":{"integer":9007199254740993}},"unknown":{"retained":233}}`
	client := grafana.CommunityClient{HTTPClient: &http.Client{Transport: communityTransport(func(r *http.Request) (*http.Response, error) {
		assert.Equal(t, "https://grafana.com/api/dashboards/1860", r.URL.String())
		return communityResponse(r, 200, metadata), nil
	})}}
	result, err := client.Metadata(context.Background(), 1860)
	require.NoError(t, err)
	assert.JSONEq(t, metadata, string(result))
	assert.Contains(t, string(result), "9007199254740993")
	_, err = client.Metadata(context.Background(), 0)
	require.Error(t, err)
}
