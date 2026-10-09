package grafana_test

import (
	"github.com/neko233-com/MetricsPanel233/internal/grafana"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestClassicDashboardImportPreservesOriginalAndVariables(t *testing.T) {
	data := []byte(`{"title":"MySQL","panels":[{"id":1,"title":"Connections","type":"timeseries","targets":[{"expr":"mysql_global_status_threads_connected{job=\"$job\"}"}]},{"type":"row","panels":[{"title":"QPS","type":"stat","targets":[{"expr":"sum(rate(mysql_global_status_questions[5m]))"}]}]},{"title":"Plugin","type":"third-party-plugin"}],"templating":{"list":[{"name":"job","type":"query","query":{"query":"label_values(mysql_up,job)"},"current":{"value":"mysql"},"includeAll":true}]}}`)
	result, err := grafana.Import(data)
	require.NoError(t, err)
	require.Len(t, result.Dashboard.Panels, 2)
	assert.Equal(t, "stat", result.Dashboard.Panels[1].Visualization)
	assert.Contains(t, result.Warnings[0], "third-party-plugin")
	assert.JSONEq(t, string(data), string(result.Dashboard.Grafana))
	require.Len(t, result.Dashboard.Variables, 1)
	assert.Equal(t, "mysql", result.Dashboard.Variables[0].Current)
}
func TestUnsupportedAndMalformedTemplates(t *testing.T) {
	for _, data := range []string{"not json", `{}`, `{"title":"Only plugin","panels":[{"type":"unsupported"}]}`} {
		_, err := grafana.Import([]byte(data))
		require.Error(t, err)
	}
}

func TestPercentUnitScalingAndGaugeWarning(t *testing.T) {
	r, err := grafana.Import([]byte(`{"title":"Ratio","panels":[{"title":"Usage","type":"gauge","targets":[{"expr":"cpu_ratio"}],"fieldConfig":{"defaults":{"unit":"percentunit"}}}]}`))
	require.NoError(t, err)
	assert.Equal(t, "(cpu_ratio) * 100", r.Dashboard.Panels[0].Expr)
	assert.Equal(t, "percent", r.Dashboard.Panels[0].Unit)
	require.Len(t, r.Warnings, 1)
}
