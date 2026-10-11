package server_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGrafanaDashboardAPIKeepsJSONNumberPrecisionAndVersions(t *testing.T) {
	_, handler := setup(t, "")
	for _, resource := range []struct{ name, document string }{
		{"classic", `{"uid":"precise-classic","title":"Precise","opaque":{"integer":9007199254740993,"negative":-9007199254740993,"decimal":1.234567890123456789,"text":"9007199254740993"},"panels":[{"id":1,"type":"text","title":"Text","options":{"content":"Precision"}}]}`},
		{"resource", `{"apiVersion":"dashboard.grafana.app/v1beta1","kind":"Dashboard","metadata":{"name":"precise-resource"},"spec":{"title":"Precise","opaque":{"integer":9007199254740993,"negative":-9007199254740993,"decimal":1.234567890123456789,"text":"9007199254740993"},"panels":[{"id":1,"type":"text","title":"Text","options":{"content":"Precision"}}]}}`},
	} {
		t.Run(resource.name, func(t *testing.T) {
			imported := call(handler, "POST", "/api/v1/import/grafana", resource.document, "", "")
			require.Equal(t, http.StatusOK, imported.Code, imported.Body.String())
			path := "/api/dashboards/uid/precise-" + resource.name
			read := call(handler, "GET", path, "", "", "")
			require.Equal(t, http.StatusOK, read.Code, read.Body.String())
			for _, field := range []string{`"integer":9007199254740993`, `"negative":-9007199254740993`, `"decimal":1.234567890123456789`, `"text":"9007199254740993"`} {
				require.Contains(t, read.Body.String(), field)
			}
			var result struct {
				Dashboard json.RawMessage `json:"dashboard"`
			}
			require.NoError(t, json.Unmarshal(read.Body.Bytes(), &result))
			body := `{"dashboard":` + string(result.Dashboard) + `,"overwrite":false}`
			saved := call(handler, "POST", "/api/dashboards/db", body, "", "")
			require.Equal(t, http.StatusOK, saved.Code, saved.Body.String())
			require.Contains(t, saved.Body.String(), `"version":2`)
			require.Equal(t, http.StatusPreconditionFailed, call(handler, "POST", "/api/dashboards/db", body, "", "").Code)
			again := call(handler, "GET", path, "", "", "")
			require.Equal(t, http.StatusOK, again.Code, again.Body.String())
			require.Contains(t, again.Body.String(), `"version":2`)
			for _, field := range []string{`"integer":9007199254740993`, `"negative":-9007199254740993`, `"decimal":1.234567890123456789`, `"text":"9007199254740993"`} {
				require.Contains(t, again.Body.String(), field)
			}
		})
	}
}
