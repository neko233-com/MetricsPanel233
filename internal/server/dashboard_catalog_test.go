package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/grafana"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type catalogTransport func(*http.Request) (*http.Response, error)

func (f catalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const mappedCommunityDocument = `{"title":"MySQL Overview","uid":"community-original","__inputs":[{"name":"DS_PROM","label":"Prometheus","type":"datasource","pluginId":"prometheus"},{"name":"PREFIX","type":"constant"}],"panels":[{"id":1,"title":"Connections","type":"stat","datasource":{"uid":"${DS_PROM}","type":"prometheus"},"targets":[{"refId":"A","expr":"${PREFIX}connections","opaque":{"kept":233}}]}],"future":{"integer":9007199254740993},"annotations":{"list":[]}}`

func newCatalogServer(t *testing.T) (*store.Store, *server.Server, http.Handler) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "catalog.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.DB.Close() })
	app := server.New(db, "catalog-token-233", 30)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/dashboards/7362":
			_, _ = w.Write([]byte(`{"name":"MySQL Overview","description":"Exporter metrics","revision":3}`))
		case "/api/dashboards/7362/revisions":
			_, _ = w.Write([]byte(`{"items":[{"revision":1},{"revision":5},{"revision":3}]}`))
		case "/api/dashboards/7362/revisions/5/download", "/api/dashboards/7362/revisions/3/download":
			_, _ = w.Write([]byte(mappedCommunityDocument))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(remote.Close)
	address, err := url.Parse(remote.URL)
	require.NoError(t, err)
	app.Community = &grafana.CommunityClient{HTTPClient: &http.Client{Transport: catalogTransport(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, "grafana.com", request.URL.Host)
		assert.Equal(t, "https", request.URL.Scheme)
		clone := request.Clone(request.Context())
		clone.URL.Scheme, clone.URL.Host = address.Scheme, address.Host
		return http.DefaultTransport.RoundTrip(clone)
	})}}
	return db, app, app.Handler()
}

func TestCommunityPreviewImportAndDurableBoundExport(t *testing.T) {
	db, _, handler := newCatalogServer(t)
	before, err := db.Dashboards(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 401, call(handler, "GET", "/api/v1/dashboards/community/7362", "", "", "").Code)
	w := call(handler, "GET", "/api/v1/dashboards/community/7362", "", "catalog-token-233", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var preview grafana.CommunityDashboard
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &preview))
	assert.Equal(t, 5, preview.Revision)
	assert.Equal(t, []int{5, 3, 1}, preview.Revisions)
	assert.JSONEq(t, mappedCommunityDocument, string(preview.Dashboard))
	require.Len(t, preview.Inputs, 2)
	assert.True(t, preview.Inputs[0].Required)
	after, err := db.Dashboards(context.Background())
	require.NoError(t, err)
	assert.Len(t, after, len(before))
	w = call(handler, "GET", "/api/v1/dashboards/community/7362?revision=3", "", "catalog-token-233", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &preview))
	assert.Equal(t, 3, preview.Revision)
	w = call(handler, "POST", "/api/v1/import/grafana-community", `{"url":"https://grafana.com/api/dashboards/7362/revisions/3/download","name":"Production MySQL","uid":"production-mysql","inputs":{"DS_PROM":"metricspanel","PREFIX":"mysql_"}}`, "catalog-token-233", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var result struct {
		Dashboard model.Dashboard `json:"dashboard"`
		Source    struct {
			ID       int    `json:"id"`
			Revision int    `json:"revision"`
			URL      string `json:"url"`
		} `json:"source"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
	assert.Equal(t, 7362, result.Source.ID)
	assert.Equal(t, 3, result.Source.Revision)
	assert.Equal(t, "Production MySQL", result.Dashboard.Name)
	assert.Equal(t, "mysql_connections", result.Dashboard.Panels[0].Expr)
	assert.Contains(t, string(result.Dashboard.Grafana), "9007199254740993")
	assert.Contains(t, string(result.Dashboard.Panels[0].Config), `"uid":"metricspanel"`)
	assert.Contains(t, string(result.Dashboard.Panels[0].Config), `"opaque":{"kept":233}`)
	w = call(handler, "GET", "/api/dashboards/uid/production-mysql", "", "catalog-token-233", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"mysql_connections"`)
	// A conflicting community UID cannot create an ambiguous route or replace data.
	w = call(handler, "POST", "/api/v1/import/grafana-community", `{"id":7362,"uid":"production-mysql","inputs":{"DS_PROM":"metricspanel","PREFIX":"redis_"}}`, "catalog-token-233", "")
	assert.Equal(t, 409, w.Code, w.Body.String())
	after, err = db.Dashboards(context.Background())
	require.NoError(t, err)
	assert.Len(t, after, len(before)+1)
	path := db.Path
	require.NoError(t, db.DB.Close())
	reopened, err := store.Open(path)
	require.NoError(t, err)
	defer reopened.DB.Close()
	persisted, err := reopened.Dashboards(context.Background())
	require.NoError(t, err)
	var retained *model.Dashboard
	for index := range persisted {
		if persisted[index].ID == result.Dashboard.ID {
			retained = &persisted[index]
		}
	}
	require.NotNil(t, retained)
	assert.JSONEq(t, string(result.Dashboard.Grafana), string(retained.Grafana))
	assert.JSONEq(t, string(result.Dashboard.Panels[0].Config), string(retained.Panels[0].Config))
}

func TestCommunityBadInputNeverWritesDashboard(t *testing.T) {
	db, _, handler := newCatalogServer(t)
	before, err := db.Dashboards(context.Background())
	require.NoError(t, err)
	for _, body := range []string{
		`{"id":7362,"url":"https://grafana.com/dashboards/7362"}`,
		`{"id":-1}`,
		`{"id":7362,"revision":-1}`,
		`{"url":"https://evil.test/dashboards/7362"}`,
		`{"url":"https://grafana.com/api/dashboards/7362/revisions/3/download","revision":5}`,
		`{"id":7362,"inputs":{"PREFIX":"mysql_"}}`,
		`{"id":7362,"inputs":{"DS_PROM":"missing","PREFIX":"mysql_"}}`,
		`{"id":7362,"inputs":{"DS_PROM":"grafana","PREFIX":"mysql_"}}`,
		`{"id":7362,"inputs":{"DS_PROM":"metricspanel","PREFIX":"mysql_","unknown":"233"}}`,
		`{"id":7362,"inputs":{"DS_PROM":"metricspanel","PREFIX":"mysql_"},"uid":"invalid/uid"}`,
	} {
		w := call(handler, "POST", "/api/v1/import/grafana-community", body, "catalog-token-233", "")
		assert.Equal(t, 400, w.Code, w.Body.String())
	}
	assert.Equal(t, 404, call(handler, "GET", "/api/v1/dashboards/community/999999", "", "catalog-token-233", "").Code)
	for _, path := range []string{"/api/v1/dashboards/community/0", "/api/v1/dashboards/community/7362?revision=no", "/api/v1/dashboards/community/7362?revision=0", "/api/v1/dashboards/community/7362?revision=999"} {
		assert.Equal(t, 400, call(handler, "GET", path, "", "catalog-token-233", "").Code)
	}
	after, err := db.Dashboards(context.Background())
	require.NoError(t, err)
	assert.Len(t, after, len(before))
}

func TestLocalPreviewBindingAndOptionalDatasourceVariable(t *testing.T) {
	db, _, handler := newCatalogServer(t)
	body, err := json.Marshal(map[string]any{"dashboard": json.RawMessage(mappedCommunityDocument)})
	require.NoError(t, err)
	w := call(handler, "POST", "/api/v1/import/grafana-preview", string(body), "catalog-token-233", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"name":"DS_PROM"`)
	assert.Contains(t, w.Body.String(), `"required":true`)
	assert.Equal(t, 403, call(handler, "POST", "/api/v1/import/grafana-preview", string(body), "catalog-token-233", "https://unrelated.test").Code)
	request := map[string]any{"dashboard": json.RawMessage(mappedCommunityDocument), "inputs": map[string]string{"DS_PROM": "metricspanel", "PREFIX": "mysql_"}}
	body, err = json.Marshal(request)
	require.NoError(t, err)
	w = call(handler, "POST", "/api/v1/import/grafana-bound", string(body), "catalog-token-233", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var imported grafana.ImportResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &imported))
	assert.Equal(t, "mysql_connections", imported.Dashboard.Panels[0].Expr)
	assert.NotContains(t, string(imported.Dashboard.Grafana), `"uid":"community-original"`)
	variable := `{"title":"Node Exporter","panels":[{"type":"stat","title":"Load","datasource":{"uid":"${ds_prometheus}","type":"prometheus"},"targets":[{"expr":"vector(233)"}]}],"templating":{"list":[{"name":"ds_prometheus","type":"datasource","query":"prometheus","current":{}}]}}`
	request = map[string]any{"dashboard": json.RawMessage(variable), "inputs": map[string]string{"ds_prometheus": "metricspanel"}}
	body, err = json.Marshal(request)
	require.NoError(t, err)
	w = call(handler, "POST", "/api/v1/import/grafana-bound", string(body), "catalog-token-233", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &imported))
	assert.Equal(t, "metricspanel", imported.Dashboard.Variables[0].Current)
	assert.Contains(t, string(imported.Dashboard.Panels[0].Config), `${ds_prometheus}`)
	assert.True(t, strings.Contains(string(imported.Dashboard.Grafana), `"value":"metricspanel"`))
	stored, err := db.Dashboards(context.Background())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(stored), 2)
}

func TestBoundImportConcurrentUIDCreatesOneDashboard(t *testing.T) {
	db, _, handler := newCatalogServer(t)
	before, err := db.Dashboards(context.Background())
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{"dashboard": json.RawMessage(mappedCommunityDocument), "uid": "one-import", "inputs": map[string]string{"DS_PROM": "metricspanel", "PREFIX": "mysql_"}})
	require.NoError(t, err)
	statuses := make(chan int, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			statuses <- call(handler, "POST", "/api/v1/import/grafana-bound", string(body), "catalog-token-233", "").Code
		}()
	}
	group.Wait()
	close(statuses)
	success, conflicts := 0, 0
	for status := range statuses {
		switch status {
		case 200:
			success++
		case 409:
			conflicts++
		default:
			t.Errorf("unexpected import status: %d", status)
		}
	}
	assert.Equal(t, 1, success)
	assert.Equal(t, 7, conflicts)
	after, err := db.Dashboards(context.Background())
	require.NoError(t, err)
	assert.Len(t, after, len(before)+1)
}
