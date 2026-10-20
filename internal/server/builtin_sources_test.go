package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLokiSourceQueriesLogsAndHealth(t *testing.T) {
	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ready":
			w.WriteHeader(http.StatusOK)
		case "/loki/api/v1/query_range":
			assert.Equal(t, `{app="api"}`, r.URL.Query().Get("query"))
			assert.Equal(t, "2", r.URL.Query().Get("limit"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[{"stream":{"app":"api"},"values":[["2000000000","second"],["1000000000","first"]]}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer loki.Close()
	_, handler := setup(t, "")
	saved := call(handler, "POST", "/api/datasources", `{"uid":"loki","name":"Loki","type":"loki","url":"`+loki.URL+`","access":"proxy"}`, "", "")
	require.Equal(t, 200, saved.Code, saved.Body.String())
	query := call(handler, "POST", "/api/ds/query", `{"from":"1700000000000","to":"1700000600000","queries":[{"refId":"A","datasource":{"uid":"loki"},"expr":"{app=\"api\"}","limit":2}]}`, "", "")
	require.Equal(t, 200, query.Code, query.Body.String())
	var response struct {
		Results map[string]struct {
			Frames []struct {
				Schema struct {
					Fields []struct {
						Name   string            `json:"name"`
						Labels map[string]string `json:"labels"`
					} `json:"fields"`
				} `json:"schema"`
				Data struct {
					Values [][]any `json:"values"`
				} `json:"data"`
			} `json:"frames"`
			Error string `json:"error"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(query.Body.Bytes(), &response))
	result := response.Results["A"]
	require.Empty(t, result.Error)
	require.Len(t, result.Frames, 1)
	fields := result.Frames[0].Schema.Fields
	require.Len(t, fields, 2)
	assert.Equal(t, "Time", fields[0].Name)
	assert.Equal(t, "Line", fields[1].Name)
	assert.Equal(t, "api", fields[1].Labels["app"])
	require.Len(t, result.Frames[0].Data.Values, 2)
	assert.Equal(t, []any{"first", "second"}, result.Frames[0].Data.Values[1])

	health := call(handler, "GET", "/api/datasources/uid/loki/health", "", "", "")
	require.Equal(t, 200, health.Code, health.Body.String())
}

func TestTempoSourceReturnsTraceTable(t *testing.T) {
	tempo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/search" {
			http.NotFound(w, r)
			return
		}
		assert.Equal(t, "{}", r.URL.Query().Get("q"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"traces":[{"traceID":"abc","rootServiceName":"api","rootTraceName":"GET /","startTimeUnixNano":"1700000000000000000","durationMs":12}]}`))
	}))
	defer tempo.Close()
	_, handler := setup(t, "")
	saved := call(handler, "POST", "/api/datasources", `{"uid":"tempo","name":"Tempo","type":"tempo","url":"`+tempo.URL+`","access":"proxy"}`, "", "")
	require.Equal(t, 200, saved.Code, saved.Body.String())
	query := call(handler, "POST", "/api/ds/query", `{"from":"1700000000000","to":"1700000600000","queries":[{"refId":"A","datasource":{"uid":"tempo"},"query":"{}","limit":5}]}`, "", "")
	require.Equal(t, 200, query.Code, query.Body.String())
	var response struct {
		Results map[string]struct {
			Frames []struct {
				Schema struct {
					Fields []struct {
						Name string `json:"name"`
					} `json:"fields"`
				} `json:"schema"`
				Data struct {
					Values [][]any `json:"values"`
				} `json:"data"`
			} `json:"frames"`
		} `json:"results"`
	}
	require.NoError(t, json.Unmarshal(query.Body.Bytes(), &response))
	frames := response.Results["A"].Frames
	require.Len(t, frames, 1)
	names := []string{}
	for _, field := range frames[0].Schema.Fields {
		names = append(names, field.Name)
	}
	assert.Equal(t, []string{"traceID", "rootServiceName", "rootTraceName", "startTime", "durationMs"}, names)
	require.Len(t, frames[0].Data.Values, 5)
	assert.Equal(t, []any{"abc"}, frames[0].Data.Values[0])
}
