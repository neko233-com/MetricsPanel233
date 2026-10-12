package notificationtemplate

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func templateData() Data {
	return Data{Receiver: "Operations", Status: "firing", GroupLabels: KV{"team": "ops"}, CommonLabels: KV{"team": "ops", "alertname": "Service"}, CommonAnnotations: KV{"summary": "Unavailable"}, Alerts: Alerts{{Status: "firing", Labels: KV{"team": "ops", "alertname": "Service"}, Annotations: KV{"summary": "Unavailable"}, Values: map[string]float64{"A": 3}, StartsAt: time.Date(2026, 10, 12, 1, 2, 3, 0, time.UTC)}, {Status: "resolved", Labels: KV{"team": "ops"}, Values: map[string]float64{"B": 2}}}}
}

func TestNotificationTemplateOfficialFunctionsDataAndDefinitions(t *testing.T) {
	compiled, err := Compile([]Definition{{Name: "ops", Template: `{{define "ops.title"}}{{toUpper .Receiver}} {{len .Alerts.Firing}}/{{len .Alerts.Resolved}} {{.GroupLabels.SortedPairs.Values | join "/"}}{{end}}{{define "ops.message"}}{{range .Alerts.Firing}}{{.Labels.alertname}}={{index .Values "A"}} {{date "2006-01-02 15:04" (.StartsAt | tz "Asia/Shanghai")}}{{end}}{{end}}`}})
	require.NoError(t, err)
	data := templateData()
	require.NoError(t, data.Normalize(time.Now()))
	title, err := compiled.Render(context.Background(), `{{template "ops.title" .}}`, data)
	require.NoError(t, err)
	assert.Equal(t, "OPERATIONS 1/1 ops", title)
	message, err := compiled.Render(context.Background(), `{{template "ops.message" .}}`, data)
	require.NoError(t, err)
	assert.Equal(t, "Service=3 2026-10-12 09:02", message)
	_, err = compiled.Render(context.Background(), `{{toJson .Alerts}}`, data)
	require.NoError(t, err)
	// Repeated renders copy syntax trees instead of accumulating instrumentation.
	again, err := compiled.Render(context.Background(), `{{template "ops.title" .}}`, data)
	require.NoError(t, err)
	assert.Equal(t, title, again)
	compiled, err = Compile([]Definition{{Name: "raw-group", Template: `Hello {{.Receiver}}`}})
	require.NoError(t, err)
	value, err := compiled.Render(context.Background(), `{{template "raw-group" .}}`, data)
	require.NoError(t, err)
	assert.Equal(t, "Hello Operations", value)
}

func TestNotificationTemplateLimitsStopActualEmptyLoopsAndExpansion(t *testing.T) {
	compiled, err := Compile(nil)
	require.NoError(t, err)
	data := templateData()
	ctx := context.Background()
	for _, source := range []string{`{{range 1000000000000}}{{end}}`, `{{range 100000}}{{range 100000}}{{end}}{{end}}`, `{{printf "%1000000000000s" "x"}}`, `{{printf "%*s" 1000000000000 "x"}}`, `{{$x:=list "x"}}{{range 30}}{{$x=list $x $x}}{{end}}{{toJson $x}}`, `{{range 10000}}{{printf "%4096s" "x"}}{{end}}`} {
		t.Run(source, func(t *testing.T) {
			started := time.Now()
			_, err := compiled.Render(ctx, source, data)
			require.Error(t, err)
			assert.Less(t, time.Since(started), time.Second, "execution stops synchronously without leaving a timed-out goroutine")
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = compiled.Render(canceled, `{{range 1000000000}}{{end}}`, data)
	assert.ErrorIs(t, err, context.Canceled)
	for _, source := range []string{`{{define "recursive"}}{{template "recursive" .}}{{end}}`, `{{define "x"}}{{template "missing" .}}{{end}}`, `{{define "default.title"}}override{{end}}`, `{{define "x"}}{{range 1}}{{range 1}}{{range 1}}{{range 1}}{{range 1}}{{range 1}}{{range 1}}{{range 1}}{{range 1}}deep{{end}}{{end}}{{end}}{{end}}{{end}}{{end}}{{end}}{{end}}{{end}}{{end}}`} {
		_, err := Compile([]Definition{{Name: "bad", Template: source}})
		require.Error(t, err)
	}
	_, err = Compile([]Definition{{Name: "a", Template: `{{define "same"}}A{{end}}`}, {Name: "b", Template: `{{define "same"}}B{{end}}`}})
	require.Error(t, err)
	// A wide DAG is checked once per definition, not by walking every path.
	var graph strings.Builder
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&graph, `{{define "n%d"}}`, i)
		if i < 24 {
			fmt.Fprintf(&graph, `{{template "n%d" .}}{{template "n%d" .}}`, i+1, i+1)
		}
		graph.WriteString(`{{end}}`)
	}
	started := time.Now()
	wide, err := Compile([]Definition{{Name: "graph", Template: graph.String()}})
	require.NoError(t, err)
	assert.Less(t, time.Since(started), time.Second)
	_, err = wide.Render(ctx, `{{template "n0" .}}`, data)
	require.Error(t, err)
}

func TestNotificationTemplateDataPrivateFieldsLinksAndBounds(t *testing.T) {
	data := templateData()
	data.ExternalURL = "http://127.0.0.1:7333"
	data.CommonLabels["__secret__"] = "private-label"
	data.Alerts[0].Annotations["__dashboardUid__"] = "service"
	data.Alerts[0].Annotations["__panelId__"] = "12"
	data.Alerts[0].Labels["__alert_rule_uid__"] = "rule-a"
	require.NoError(t, data.Normalize(time.Now()))
	assert.NotContains(t, data.CommonLabels, "__secret__")
	assert.Equal(t, "rule-a", data.Alerts[0].RuleUID)
	assert.Contains(t, data.Alerts[0].DashboardURL, "/d/service?")
	assert.Contains(t, data.Alerts[0].PanelURL, "viewPanel=12")
	assert.NotContains(t, data.Alerts[0].Annotations, "__dashboardUid__")
	compiled, err := Compile(nil)
	require.NoError(t, err)
	value, err := compiled.Render(context.Background(), `{{toJson .}}`, data)
	require.NoError(t, err)
	assert.NotContains(t, value, "private-label")
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(value), &decoded))
	data.Alerts = make(Alerts, 1001)
	assert.Error(t, data.Normalize(time.Now()))
}

func TestNotificationTemplateFunctionExpansionIsBoundedBeforeAllocation(t *testing.T) {
	compiled, err := Compile(nil)
	require.NoError(t, err)
	data := templateData()
	data.CommonLabels["long"] = strings.Repeat("x", 60000)
	data.CommonLabels["replacement"] = strings.Repeat("z", 30000)
	for _, expression := range []string{
		`{{reReplaceAll "x" .CommonLabels.replacement .CommonLabels.long}}`,
		`{{reReplaceAll "x+" "${0}${0}${0}" .CommonLabels.long}}`,
		`{{join .CommonLabels.replacement (stringSlice "" "" "" "" "" "" "" "")}}`,
		`{{printf "%[1]s%[1]s%[1]s%[1]s%[1]s%[1]s" .CommonLabels.long}}`,
	} {
		started := time.Now()
		_, err = compiled.Render(context.Background(), expression, data)
		require.ErrorIs(t, err, ErrLimit, expression)
		assert.Less(t, time.Since(started), time.Second)
	}
	value, err := compiled.Render(context.Background(), `{{printf "%s" .CommonLabels.long}}`, data)
	require.NoError(t, err)
	assert.Len(t, value, 60000)
	for _, example := range [][3]string{{`(?P<name>[a-z]+)`, `[$name]`, "abc 123 def"}, {`a*`, "x", "aabb"}, {``, "_", "abc"}, {`a`, "$$", "a"}, {`(.)`, "$1$1", "ab"}} {
		e := &execution{ctx: context.Background()}
		value, err := e.boundedReplace(example[0], example[1], example[2])
		require.NoError(t, err)
		assert.Equal(t, regexp.MustCompile(example[0]).ReplaceAllString(example[2], example[1]), value)
	}
}
