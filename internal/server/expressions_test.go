package server_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const expressionGraph = `{"from":"1000","to":"4000","queries":[
 {"refId":"C","datasource":{"uid":"__expr__"},"type":"math","expression":"$A + $Reduced"},
 {"refId":"A","hide":true,"datasource":{"uid":"metricspanel"},"expr":"vector(233)","instant":true},
 {"refId":"B","hide":true,"datasource":{"uid":"grafana"},"queryType":"randomWalk","intervalMs":1000,"startValue":3,"spread":0},
 {"refId":"Reduced","hide":true,"datasource":{"type":"__expr__"},"type":"reduce","expression":"B","reducer":"mean"},
 {"refId":"D","datasource":{"uid":"-100"},"type":"threshold","expression":"C","conditions":[{"evaluator":{"type":"gt","params":[233]}}]},
 {"refId":"Classic","datasource":{"uid":"__expr__"},"type":"classic_conditions","conditions":[{"query":{"params":["C","5m","now"]},"reducer":{"type":"avg"},"evaluator":{"type":"gt","params":[233]}}]},
 {"refId":"Missing","datasource":{"uid":"__expr__"},"type":"math","expression":"$NotFound"}
 ]}`

func TestExpressionsMixedGraphHiddenDependenciesAndErrorIsolation(t *testing.T) {
	store, handler := setup(t, "")
	response := call(handler, "POST", "/api/ds/query", expressionGraph, "", "")
	require.Equal(t, 200, response.Code, response.Body.String())
	var result backend.QueryDataResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.NoError(t, result.Responses["C"].Error)
	require.Len(t, result.Responses["C"].Frames, 1)
	assert.Equal(t, float64(236), *result.Responses["C"].Frames[0].Fields[0].At(0).(*float64))
	assert.Equal(t, float64(1), *result.Responses["D"].Frames[0].Fields[0].At(0).(*float64))
	require.NoError(t, result.Responses["Classic"].Error)
	assert.Equal(t, float64(1), *result.Responses["Classic"].Frames[0].Fields[0].At(0).(*float64))
	assert.Empty(t, result.Responses["Classic"].Frames[0].Fields[0].Labels)
	metadata, err := json.Marshal(result.Responses["Classic"].Frames[0].Meta.Custom)
	require.NoError(t, err)
	assert.Contains(t, string(metadata), `"value":"236"`)
	assert.Contains(t, string(metadata), `"metric":"C"`)
	assert.ErrorContains(t, result.Responses["Missing"].Error, "missing query reference")
	require.Len(t, result.Responses["A"].Frames, 1)
	assert.Equal(t, float64(233), *result.Responses["A"].Frames[0].Fields[0].At(0).(*float64))
	var count int
	require.NoError(t, store.DB.QueryRow("SELECT count(*) FROM series").Scan(&count))
	assert.Zero(t, count)
	for _, uid := range []string{"__expr__", "-100", "Expression"} {
		response = call(handler, "GET", "/api/datasources/uid/"+uid, "", "", "")
		require.Equal(t, 200, response.Code)
		var source model.DataSource
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &source))
		assert.Equal(t, "__expr__", source.UID)
		assert.True(t, source.ReadOnly)
		assert.Equal(t, 400, call(handler, "DELETE", "/api/datasources/uid/"+uid, "", "", "").Code)
	}
}
func TestExpressionConnectionDTOAndNDJSONMatchLegacy(t *testing.T) {
	_, handler := setup(t, "")
	// SDK connection DTO permits different datasource references only on the expression connection.
	payload := strings.Replace(expressionGraph, `"datasource":{"type":"__expr__"}`, `"datasource":{"uid":"__expr__","type":"__expr__"}`, 1)
	response := call(handler, "POST", "/apis/__expr__.datasource.grafana.app/v0alpha1/namespaces/default/connections/__expr__/query", payload, "", "")
	require.Equal(t, 200, response.Code, response.Body.String())
	var result backend.QueryDataResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.NoError(t, result.Responses["C"].Error)
	assert.Equal(t, float64(236), *result.Responses["C"].Frames[0].Fields[0].At(0).(*float64))
	// A normal connection remains isolated to its configured source.
	assert.Equal(t, 400, call(handler, "POST", "/apis/prometheus.datasource.grafana.app/v0alpha1/namespaces/default/connections/metricspanel/query", payload, "", "").Code)
}
