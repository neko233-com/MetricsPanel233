package alerttemplates

import (
	"context"
	"math"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGrafanaTemplateVariablesFunctionsWhitespaceAndNestedTemplates(t *testing.T) {
	n := 233.0
	data := NewData(map[string]string{"instance": "node.example:7333", "zone": "上海"}, map[string]Capture{"A": {Labels: map[string]string{"instance": "node.example:7333"}, Value: &n, Datasource: true}, "C": {Labels: map[string]string{"instance": "node.example:7333", "region": "中国"}, Value: &n}}, "evaluation")
	root, _ := url.Parse("https://metrics.example/panel")
	at := time.UnixMilli(1700000000233)
	cases := map[string]string{
		`{{ $labels.zone }} {{ $values.A.Value }} {{ $value }}`:                          "上海 233 233",
		`{{ $labels.instance | stripPort | stripDomain }}`:                               "node",
		`{{ humanizePercentage 0.233 }} {{ humanize1024 1048576 }}`:                      "23.3% 1Mi",
		`{{ index $values "A" }} / {{ index $labels "missing" }}`:                        "233 / ",
		`{{ $labels.missing }} {{ $values.Missing.Value }}`:                              "[no value] [no value]",
		`{{ filterLabels $labels "zone" }} / {{ removeLabelsRe $labels "inst.*" }}`:      "zone=上海 / zone=上海",
		`{{ mergeLabelValues $values }}`:                                                 "instance=node.example:7333, region=中国",
		`{{ externalURL }} {{ pathPrefix }} {{ now }}`:                                   "https://metrics.example/panel /panel 1.700000000233e+09",
		`{{ define "hello" }}Hello {{ . }}!{{ end }}{{ template "hello" $labels.zone }}`: "Hello 上海!",
		"before \n {{- $labels.zone -}} \n after":                                        "before上海after",
		`{{ range $key, $value := $values }}{{ $key }}={{ $value.Value }};{{ end }}`:     "A=233;C=233;",
		`{{ len (query "vector(1)") }}`:                                                  "0",
	}
	for raw, want := range cases {
		t.Run(raw, func(t *testing.T) {
			got, err := Compile("test", raw).Expand(context.Background(), data, root, at, 4096)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
	for name, instant := range map[string]bool{"graphLink": false, "tableLink": true} {
		raw := `{{ ` + name + ` "{\"datasource\":\"metricspanel\",\"expr\":\"sum(up)\"}" }}`
		got, err := Compile("link", raw).Expand(context.Background(), data, root, at, 4096)
		require.NoError(t, err)
		assert.Contains(t, got, `/explore?left=`)
		if instant {
			assert.Contains(t, got, `"instant":true`)
		} else {
			assert.Contains(t, got, `"range":true`)
		}
	}
}

func TestTemplateFailuresRetainOriginalAndStopWorkWithoutChangingInputs(t *testing.T) {
	data := NewData(map[string]string{"job": "mysql"}, nil, "")
	for _, raw := range []string{`{{ $values.1A.Value }}`, `{{ unknownFunction }}`, `{{ reReplaceAll "[" "" "x" }}`, `{{ printf "%10000s" "huge" }}`, `{{ define "again" }}{{ template "again" . }}{{ end }}{{ template "again" . }}`} {
		got, err := Compile("bad", raw).Expand(context.Background(), data, nil, time.Now(), 512)
		require.Error(t, err, raw)
		assert.Equal(t, raw, got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw := `{{ $labels.job }}`
	got, err := Compile("cancel", raw).Expand(ctx, data, nil, time.Now(), 512)
	require.Error(t, err)
	assert.Equal(t, raw, got)
	assert.Equal(t, "mysql", data.Labels["job"])
	// Empty nested ranges still spend execution budget; output limits alone would not stop them.
	values := map[string]Capture{}
	for i := 0; i < 32; i++ {
		values[string(rune('A'+i))] = Capture{}
	}
	data = NewData(nil, values, "")
	raw = `{{ range $values }}{{ range $values }}{{ range $values }}{{ range $values }}{{ end }}{{ end }}{{ end }}{{ end }}`
	_, err = Compile("work", raw).Expand(context.Background(), data, nil, time.Now(), 512)
	require.ErrorContains(t, err, "50000")
}

func TestTemplateNumericSourceValueAndClassicEvaluationString(t *testing.T) {
	n := 233.0
	data := NewData(nil, map[string]Capture{"A": {Value: &n, Datasource: true}}, "capture string")
	assert.Equal(t, n, data.Value)
	data = NewData(nil, map[string]Capture{"A": {Value: &n, Datasource: true}, "B": {Value: &n, Datasource: true}}, "capture string")
	assert.Equal(t, "capture string", data.Value)
	data = NewData(nil, map[string]Capture{"K0": {Value: nil, Type: "classic_conditions"}}, "classic string")
	assert.True(t, math.IsNaN(data.Values["K0"].Value))
	assert.Equal(t, "classic string", data.Value)
}
