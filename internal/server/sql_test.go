package server_test

import (
	"encoding/json"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSQLBothQueryAPIsCombineLocalMetricsAndGrafanaFrames(t *testing.T) {
	_, handler := setup(t, "")
	payload := `{"from":"1000","to":"4000","queries":[{"refId":"C","datasource":{"uid":"__expr__","type":"__expr__"},"type":"sql","expression":"WITH totals AS (SELECT SUM(__value__) AS total FROM B) SELECT A.__value__ + totals.total AS total FROM A CROSS JOIN totals"},{"refId":"A","datasource":{"uid":"metricspanel"},"expr":"vector(233)","instant":true},{"refId":"B","datasource":{"uid":"grafana"},"queryType":"randomWalk","intervalMs":1000,"startValue":3,"spread":0}]}`
	for _, path := range []string{"/api/ds/query", "/apis/__expr__.datasource.grafana.app/v0alpha1/namespaces/default/connections/__expr__/query"} {
		w := call(handler, "POST", path, payload, "", "")
		require.Equal(t, 200, w.Code, w.Body.String())
		var response backend.QueryDataResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		q := response.Responses["C"]
		require.NoError(t, q.Error)
		require.Len(t, q.Frames, 1)
		value, err := q.Frames[0].Fields[0].FloatAt(0)
		require.NoError(t, err)
		assert.Equal(t, float64(242), value)
		assert.Equal(t, "__value__", response.Responses["A"].Frames[0].Fields[1].Name)
	}
}
