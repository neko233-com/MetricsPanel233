package grafana_test

import (
	"encoding/json"
	"github.com/neko233-com/MetricsPanel233/internal/grafana"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestLegacyGrafanaSnapshotUsesFrozenFramesAndPreservesSource(t *testing.T) {
	original := `{"title":"Legacy snapshot","panels":[{"id":1,"title":"Saved value","type":"stat","datasource":{"uid":"offline","type":"prometheus"},"targets":[{"expr":"must_not_query"}],"snapshotData":[{"refId":"Old","name":"Original","meta":{"custom":{"opaque":233}},"fields":[{"name":"Value","type":"number","labels":{"job":"api"},"config":{"unit":"short"},"values":[233,777]}]}],"opaque":true}]}`
	result, err := grafana.Import([]byte(original))
	require.NoError(t, err)
	require.JSONEq(t, original, string(result.Dashboard.Grafana))
	require.Empty(t, result.Dashboard.Panels[0].Expressions)
	var config map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(result.Dashboard.Panels[0].Config, &config))
	require.JSONEq(t, `{"uid":"grafana","type":"grafana"}`, string(config["datasource"]))
	require.JSONEq(t, `[{"refId":"Snapshot","queryType":"snapshot","snapshot":[{"schema":{"refId":"Old","name":"Original","meta":{"custom":{"opaque":233}},"fields":[{"name":"Value","type":"number","labels":{"job":"api"},"config":{"unit":"short"}}]},"data":{"values":[[233,777]]}}]}]`, string(config["targets"]))
}

func TestLegacySnapshotRejectsMalformedOrMismatchedFields(t *testing.T) {
	for _, payload := range []string{`{}`, `[null]`, `[{"fields":42}]`, `[{"fields":[{"values":42}]}]`, `[{"fields":[{"values":[1]},{"values":[1,2]}]}]`} {
		_, err := grafana.Import([]byte(`{"title":"Snapshot","panels":[{"title":"Bad","type":"table","snapshotData":` + payload + `}]}`))
		require.Error(t, err, payload)
	}
}
