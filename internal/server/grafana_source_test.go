package server_test

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuiltinGrafanaDiscoveryReadonlyAndDefault(t *testing.T) {
	_, handler := setup(t, "")
	response := call(handler, "GET", "/api/datasources", "", "", "")
	require.Equal(t, 200, response.Code)
	var sources []model.DataSource
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &sources))
	require.Len(t, sources, 2)
	assert.Equal(t, "metricspanel", sources[0].UID)
	assert.True(t, sources[0].IsDefault)
	assert.Equal(t, "grafana", sources[1].UID)
	assert.Equal(t, int64(-1), sources[1].ID)
	assert.True(t, sources[1].ReadOnly)
	assert.False(t, sources[1].IsDefault)
	for _, uid := range []string{"grafana", "-- Grafana --", "-1"} {
		endpoint := "/api/datasources/uid/" + url.PathEscape(uid)
		assert.Equal(t, 200, call(handler, "GET", endpoint, "", "", "").Code)
		assert.Equal(t, 200, call(handler, "GET", endpoint+"/health", "", "", "").Code)
		assert.Equal(t, 400, call(handler, "PUT", endpoint, `{}`, "", "").Code)
		assert.Equal(t, 400, call(handler, "DELETE", endpoint, "", "", "").Code)
	}
	for _, payload := range []string{`{"uid":"grafana","name":"Shadow","type":"prometheus"}`, `{"uid":"shadow","name":"-- Grafana --","type":"prometheus"}`, `{"uid":"shadow","name":"Shadow","type":"grafana"}`} {
		assert.Equal(t, 400, call(handler, "POST", "/api/datasources", payload, "", "").Code)
	}
}

func TestBuiltinGrafanaQueriesListRangeOmissionAndRandomFrames(t *testing.T) {
	store, handler := setup(t, "")
	response := call(handler, "POST", "/api/ds/query", `{"queries":[{"refId":"L","queryType":"list","path":"","datasource":{"uid":"grafana"}}]}`, "", "")
	require.Equal(t, 200, response.Code, response.Body.String())
	var result backend.QueryDataResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.Len(t, result.Responses["L"].Frames, 1)
	frame := result.Responses["L"].Frames[0]
	assert.Equal(t, "name", frame.Fields[0].Name)
	assert.Equal(t, "mediaType", frame.Fields[1].Name)
	require.GreaterOrEqual(t, frame.Rows(), 1)
	found := false
	for i := 0; i < frame.Rows(); i++ {
		if frame.Fields[0].At(i) == "index.html" {
			found = true
		}
	}
	require.True(t, found, "public index is missing from SDK listFiles")
	response = call(handler, "POST", "/api/ds/query", `{"from":"1000","to":"10000","intervalMs":1000,"queries":[{"refId":"R","queryType":"randomWalk","startValue":233,"seriesCount":2,"spread":0,"noise":0,"datasource":{"uid":"grafana"}},{"refId":"L","queryType":"list","path":"../data","datasource":{"uid":"grafana"}}]}`, "", "")
	require.Equal(t, 200, response.Code, response.Body.String())
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.Len(t, result.Responses["R"].Frames, 2)
	for i, frame := range result.Responses["R"].Frames {
		assert.Equal(t, "R", frame.RefID)
		assert.Equal(t, 9, frame.Rows())
		assert.Equal(t, float64(233), frame.Fields[1].At(0))
		if i == 0 {
			assert.Equal(t, "R-series", frame.Fields[1].Name)
		} else {
			assert.Equal(t, "R-series1", frame.Fields[1].Name)
		}
	}
	assert.ErrorContains(t, result.Responses["L"].Error, "relative public asset folder")
	assert.Equal(t, 400, call(handler, "POST", "/api/ds/query", `{"queries":[{"refId":"A","queryType":"randomWalk","datasource":{"uid":"grafana"}}]}`, "", "").Code)
	var series int
	require.NoError(t, store.DB.QueryRowContext(context.Background(), "SELECT count(*) FROM series").Scan(&series))
	assert.Zero(t, series, "generated visualization data must never be ingested")
}
