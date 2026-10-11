package grafana_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/grafana"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type communityTransport func(*http.Request) (*http.Response, error)

func (f communityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func communityResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"application/json"}}, Request: r}
}

func TestCommunityReferenceUsesOnlyOfficialIdentity(t *testing.T) {
	for _, reference := range []string{"1860", " 1860 ", "https://grafana.com/grafana/dashboards/1860-node-exporter-full/?utm_source=test", "https://grafana.com/dashboards/1860", "grafana.com/grafana/dashboards/1860/", "https://www.grafana.com/grafana/dashboards/1860/"} {
		id, revision, err := grafana.ParseCommunityReference(reference)
		require.NoError(t, err, reference)
		assert.Equal(t, 1860, id)
		assert.Zero(t, revision)
	}
	id, revision, err := grafana.ParseCommunityReference("https://grafana.com/api/dashboards/1860/revisions/42/download")
	require.NoError(t, err)
	assert.Equal(t, 1860, id)
	assert.Equal(t, 42, revision)
	for _, reference := range []string{"", "0", "-1", "+1860", "1.2", "2147483648", "https://evil.test/grafana/dashboards/1860", "http://grafana.com/dashboards/1860", "https://grafana.com:443/dashboards/1860", "https://user@grafana.com/dashboards/1860", "https://grafana.com.evil.test/dashboards/1860", "https://grafana.com/dashboards/1860/evil", "https://grafana.com/api/dashboards/1860/revisions/0/download"} {
		_, _, err := grafana.ParseCommunityReference(reference)
		require.Error(t, err, reference)
	}
}

func TestCommunityLoadPinsLatestRevisionAndKeepsDownload(t *testing.T) {
	paths := []string{}
	client := grafana.CommunityClient{HTTPClient: &http.Client{Transport: communityTransport(func(r *http.Request) (*http.Response, error) {
		assert.Equal(t, "grafana.com", r.URL.Host)
		assert.Equal(t, "https", r.URL.Scheme)
		assert.Equal(t, "application/json", r.Header.Get("Accept"))
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/dashboards/1860":
			return communityResponse(r, 200, `{"name":"Node Exporter Full","revision":42,"description":"Linux monitoring"}`), nil
		case "/api/dashboards/1860/revisions":
			return communityResponse(r, 200, `{"items":[{"revision":1},{"revision":45},{"revision":42},{"revision":45}],"direction":"desc"}`), nil
		case "/api/dashboards/1860/revisions/45/download", "/api/dashboards/1860/revisions/42/download":
			return communityResponse(r, 200, `{"title":"Node Exporter Full","panels":[{"type":"stat","title":"CPU","future":{"integer":9007199254740993}}],"templating":{"list":[{"type":"datasource","name":"ds_prometheus","query":"prometheus","current":{}}]}}`), nil
		default:
			return communityResponse(r, 404, `{}`), nil
		}
	})}}
	result, err := client.Load(context.Background(), 1860, 0)
	require.NoError(t, err)
	assert.Equal(t, 45, result.Revision)
	assert.Equal(t, []int{45, 42, 1}, result.Revisions)
	assert.Equal(t, "/api/dashboards/1860/revisions/45/download", paths[2])
	assert.Contains(t, string(result.Dashboard), "9007199254740993")
	require.Len(t, result.Inputs, 1)
	assert.False(t, result.Inputs[0].Required)
	assert.Equal(t, "prometheus", result.Inputs[0].PluginID)
	result, err = client.Load(context.Background(), 1860, 42)
	require.NoError(t, err)
	assert.Equal(t, 42, result.Revision)
	_, err = client.Load(context.Background(), 1860, 999)
	require.ErrorContains(t, err, "does not exist")
}

func TestCommunityDownloadBoundsRedirectAndCancellation(t *testing.T) {
	for name, body := range map[string]string{"oversize": strings.Repeat("x", grafana.CommunityBodyLimit+1), "invalid": `<html>unavailable</html>`} {
		t.Run(name, func(t *testing.T) {
			client := grafana.CommunityClient{HTTPClient: &http.Client{Transport: communityTransport(func(r *http.Request) (*http.Response, error) { return communityResponse(r, 200, body), nil })}}
			_, err := client.Load(context.Background(), 1860, 0)
			require.Error(t, err)
		})
	}
	requests := 0
	client := grafana.CommunityClient{HTTPClient: &http.Client{Transport: communityTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		response := communityResponse(r, 302, "")
		response.Header.Set("Location", "http://127.0.0.1/private")
		return response, nil
	})}}
	_, err := client.Load(context.Background(), 1860, 0)
	require.ErrorContains(t, err, "not allowed")
	assert.Equal(t, 1, requests)
	client.HTTPClient.Transport = communityTransport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = client.Load(ctx, 1860, 0)
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
	client.HTTPClient.Transport = communityTransport(func(r *http.Request) (*http.Response, error) { return communityResponse(r, 404, `{}`), nil })
	_, err = client.Load(context.Background(), 1860, 0)
	var remote *grafana.CommunityHTTPError
	require.ErrorAs(t, err, &remote)
	assert.Equal(t, 404, remote.Status)
}

func TestBindPortableInputsLiteralAndOpaquePreservation(t *testing.T) {
	source := `{"dashboard":{"title":"MySQL","uid":"source","__inputs":[{"name":"DS_PROM","type":"datasource","pluginId":"prometheus"},{"name":"PREFIX","type":"constant"},{"name":"EXPR","type":"datasource","pluginId":"__expr__"}],"future":{"integer":9007199254740993,"array":[true,null,{"opaque":"kept"}]},"panels":[{"type":"stat","title":"${PREFIX} ${untouched}","datasource":{"type":"prometheus","uid":"${DS_PROM}"},"targets":[{"expr":"${PREFIX}requests_total{job=\"$job\"}","datasource":{"uid":"${DS_PROM}"}},{"datasource":{"uid":"${EXPR}","type":"__expr__"}}]}]},"meta":{"opaque":233}}`
	title, inputs, err := grafana.PreviewInputs([]byte(source))
	require.NoError(t, err)
	assert.Equal(t, "MySQL", title)
	require.Len(t, inputs, 2)
	assert.True(t, inputs[0].Required)
	bound, err := grafana.BindDashboard([]byte(source), map[string]string{"DS_PROM": "metricspanel", "PREFIX": `${literal}$\quoted"`}, "Bound MySQL", "mysql-bound")
	require.NoError(t, err)
	assert.Contains(t, string(bound), "9007199254740993")
	var root map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(bound, &root))
	assert.JSONEq(t, `{"opaque":233}`, string(root["meta"]))
	var dashboard struct {
		Title  string `json:"title"`
		UID    string `json:"uid"`
		Panels []struct {
			Title   string `json:"title"`
			Targets []struct {
				Expr       string `json:"expr"`
				Datasource struct {
					UID string `json:"uid"`
				} `json:"datasource"`
			} `json:"targets"`
		} `json:"panels"`
	}
	require.NoError(t, json.Unmarshal(root["dashboard"], &dashboard))
	assert.Equal(t, "Bound MySQL", dashboard.Title)
	assert.Equal(t, "mysql-bound", dashboard.UID)
	assert.Equal(t, `${literal}$\quoted" ${untouched}`, dashboard.Panels[0].Title)
	assert.Equal(t, `${literal}$\quoted"requests_total{job="$job"}`, dashboard.Panels[0].Targets[0].Expr)
	assert.Equal(t, "metricspanel", dashboard.Panels[0].Targets[0].Datasource.UID)
	assert.Equal(t, "__expr__", dashboard.Panels[0].Targets[1].Datasource.UID)
	_, err = grafana.BindDashboard([]byte(source), nil, "", "")
	require.ErrorContains(t, err, "requires a value")
	_, err = grafana.BindDashboard([]byte(source), map[string]string{"DS_PROM": "metricspanel", "PREFIX": "", "typo": "233"}, "", "")
	require.ErrorContains(t, err, "unknown dashboard input")
}

func TestBindDatasourceVariablesAndV2ExportLabels(t *testing.T) {
	classic := `{"apiVersion":"dashboard.grafana.app/v1beta1","kind":"Dashboard","metadata":{"name":"old","labels":{"team":"ops"}},"spec":{"title":"Node","panels":[{"type":"stat","datasource":{"uid":"${ds_prometheus}","type":"prometheus"}}],"templating":{"list":[{"name":"ds_prometheus","type":"datasource","query":"prometheus","current":{"opaque":233},"options":[{"value":"old","text":"old","selected":true,"opaque":true}]}]}}}`
	bound, err := grafana.BindDashboard([]byte(classic), map[string]string{"ds_prometheus": "metricspanel"}, "", "node-new")
	require.NoError(t, err)
	assert.Contains(t, string(bound), `"uid":"${ds_prometheus}"`)
	assert.Contains(t, string(bound), `"current":{"opaque":233,"text":"metricspanel","value":"metricspanel"}`)
	assert.Contains(t, string(bound), `"name":"node-new"`)
	assert.Contains(t, string(bound), `"opaque":true,"selected":false`)
	v2 := `{"apiVersion":"dashboard.grafana.app/v2beta1","kind":"Dashboard","metadata":{"name":"v2","labels":{"team":"ops"}},"spec":{"title":"Sources","variables":[{"kind":"DatasourceVariable","spec":{"name":"source","query":"prometheus","current":{}}},{"kind":"ConstantVariable","spec":{"name":"prefix","query":"old_","opaque":233}}],"elements":{"panel":{"kind":"Panel","spec":{"title":"A","data":{"kind":"QueryGroup","spec":{"queries":[{"kind":"PanelQuery","spec":{"refId":"A","query":{"kind":"DataQuery","group":"prometheus","datasource":{"name":"$source"},"labels":{"grafana.app/export-label":"PROM_A","grafana.app/export-datasource-name":"Production","opaque":"kept"},"spec":{"expr":"vector(233)","opaque":9007199254740993}}}}]}},"vizConfig":{"kind":"VizConfig","group":"stat","spec":{}}}}}}}`
	_, inputs, err := grafana.PreviewInputs([]byte(v2))
	require.NoError(t, err)
	require.Len(t, inputs, 2)
	bound, err = grafana.BindDashboard([]byte(v2), map[string]string{"PROM_A": "metricspanel", "prefix": "new_"}, "Mapped", "v2-new")
	require.NoError(t, err)
	assert.Contains(t, string(bound), `"name":"$source"`)
	assert.Contains(t, string(bound), `"text":"metricspanel","value":"metricspanel"`)
	assert.Contains(t, string(bound), `"query":"new_"`)
	assert.Contains(t, string(bound), `"opaque":9007199254740993`)
	assert.Contains(t, string(bound), `"opaque":"kept"`)
	assert.Contains(t, string(bound), `"name":"v2-new"`)
	result, err := grafana.Import(bound)
	require.NoError(t, err)
	assert.Equal(t, "Mapped", result.Dashboard.Name)
	assert.Equal(t, "vector(233)", result.Dashboard.Panels[0].Expr)
	var resource map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(v2), &resource))
	bound, err = grafana.BindDashboard(resource["spec"], map[string]string{"PROM_A": "metricspanel", "prefix": "new_"}, "Raw spec", "raw-v2")
	require.NoError(t, err)
	assert.Contains(t, string(bound), `"metadata":{"name":"raw-v2"}`)
	assert.Contains(t, string(bound), `"apiVersion":"dashboard.grafana.app/v2beta1"`)
	result, err = grafana.Import(bound)
	require.NoError(t, err)
	assert.Equal(t, "Raw spec", result.Dashboard.Name)
	_, err = grafana.BindDashboard([]byte(v2), map[string]string{"PROM_A": "metricspanel", "source": "external"}, "", "")
	require.ErrorContains(t, err, "unknown dashboard input")
}

func TestV2QueryVariableDatasourceNormalizationPreservesOriginal(t *testing.T) {
	source := `{"apiVersion":"dashboard.grafana.app/v2beta1","kind":"Dashboard","metadata":{"name":"variables"},"spec":{"title":"Query variables","variables":[{"kind":"QueryVariable","spec":{"name":"job","query":{"kind":"DataQuery","group":"prometheus","datasource":{"name":"external-prometheus"},"labels":{"future":"retained"},"spec":{"query":"label_values(node_uname_info, job)","refId":"VariableQuery","opaque":{"value":233}}}}},{"kind":"QueryVariable","spec":{"name":"local","query":{"kind":"DataQuery","spec":{"query":"label_values(job)"}}}}],"elements":{"load":{"kind":"Panel","spec":{"title":"Load","data":{"kind":"QueryGroup","spec":{"queries":[{"kind":"PanelQuery","spec":{"refId":"A","query":{"kind":"DataQuery","group":"prometheus","spec":{"expr":"vector(233)"}}}}]}},"vizConfig":{"kind":"VizConfig","group":"stat","spec":{}}}}}}}`
	result, err := grafana.Import([]byte(source))
	require.NoError(t, err)
	assert.JSONEq(t, source, string(result.Dashboard.Grafana))
	require.Len(t, result.Dashboard.Variables, 2)
	assert.Equal(t, "label_values(node_uname_info, job)", result.Dashboard.Variables[0].Query)
	var configuration map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(result.Dashboard.Variables[0].Config, &configuration))
	assert.JSONEq(t, `{"uid":"external-prometheus","type":"prometheus"}`, string(configuration["datasource"]))
	assert.JSONEq(t, `{"query":"label_values(node_uname_info, job)","refId":"VariableQuery","opaque":{"value":233}}`, string(configuration["query"]))
	require.NoError(t, json.Unmarshal(result.Dashboard.Variables[1].Config, &configuration))
	assert.NotContains(t, string(result.Dashboard.Variables[1].Config), `"datasource"`)
}
