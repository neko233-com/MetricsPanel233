package server_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/server"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const recoveryRule = `{"uid":"recovery","title":"Temperature","folderUID":"general","ruleGroup":"recovery","condition":"C","for":"10s","keepFiringFor":"10s","labels":{"zone":"operator"},"annotations":{"summary":"{{ $labels.host }} = {{ $values.A.Value }} / {{ $labels.zone }}"},"data":[{"refId":"A","datasourceUid":"metricspanel","model":{"expr":"recovery_temperature","instant":true}},{"refId":"C","datasourceUid":"__expr__","model":{"type":"threshold","expression":"A","conditions":[{"evaluator":{"type":"gt","params":[100]},"unloadEvaluator":{"type":"lt","params":[80]},"loadedFingerprints":["0"]}]}}]}`

func TestRecoveryProvisioningRealMetricsRestartTimersHistoryAndNoopGroups(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recovery.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	app := server.New(s, "", 30)
	t.Cleanup(func() { app.Plugins.Close(); app.Live.Close(); s.DB.Close() })
	cachedFP := strconv.FormatUint(uint64(data.Labels{"host": "mysql", "zone": "query"}.Fingerprint()), 10)
	body := strings.Replace(recoveryRule, `"loadedFingerprints":["0"]`, `"loadedFingerprints":["`+cachedFP+`"]`, 1)
	w := call(app.Handler(), "POST", "/api/v1/provisioning/alert-rules", body, "", "")
	require.Equal(t, 201, w.Code, w.Body.String())
	start := time.Now().Truncate(time.Millisecond)
	var activeAt int64
	for _, round := range []struct {
		seconds        int
		a, b           float64
		stateA, stateB string
	}{
		{0, 101, 90, "Pending", "Normal"},
		{10, 90, 110, "Firing", "Pending"},
		{20, 80, 90, "Firing", "Firing"},
		{30, 79, 79, "Recovering", "Recovering"},
		{40, 90, 90, "Normal", "Normal"},
		{50, 90, 101, "Normal", "Pending"},
	} {
		at := start.Add(time.Duration(round.seconds) * time.Second)
		samples := []model.Sample{}
		for host, n := range map[string]float64{"go": round.a, "mysql": round.b} {
			samples = append(samples, model.Sample{Name: "recovery_temperature", Labels: map[string]string{"host": host, "zone": "query"}, Value: n, Timestamp: at.UnixMilli()})
		}
		body, err := json.Marshal(map[string]any{"samples": samples})
		require.NoError(t, err)
		w = call(app.Handler(), "POST", "/api/v1/ingest", string(body), "", "")
		require.Equal(t, 200, w.Code, w.Body.String())
		view, err := app.Alerts.Evaluate(ctx, "recovery", at)
		require.NoError(t, err)
		require.Equal(t, "ok", view.Runtime.Health, view.Runtime.Error)
		require.Len(t, view.Runtime.Instances, 2)
		for _, instance := range view.Runtime.Instances {
			host := instance.Labels["host"]
			want := round.stateA
			if host == "mysql" {
				want = round.stateB
			}
			assert.Equal(t, want, instance.State, "host %s at %ds", host, round.seconds)
			assert.Equal(t, "operator", instance.Labels["zone"])
			assert.Equal(t, strconv.FormatUint(uint64(data.Labels{"host": host, "zone": "query"}.Fingerprint()), 10), instance.ResultFingerprint, "raw query identity survives configured label overrides")
			assert.Contains(t, instance.Annotations["summary"], "/ query")
			if host == "go" && round.seconds == 0 {
				activeAt = instance.ActiveAt
			}
			if host == "go" && round.seconds == 10 {
				assert.Equal(t, activeAt, instance.ActiveAt)
			}
		}
		if round.seconds == 0 {
			app.Plugins.Close()
			app.Live.Close()
			require.NoError(t, s.DB.Close())
			s, err = store.Open(path)
			require.NoError(t, err)
			app = server.New(s, "", 30)
		}
	}
	before, err := s.AlertRule(ctx, "recovery")
	require.NoError(t, err)
	exported := call(app.Handler(), "GET", "/api/v1/provisioning/folder/general/rule-groups/recovery", "", "", "")
	require.Equal(t, 200, exported.Code)
	w = call(app.Handler(), "PUT", "/api/v1/provisioning/folder/general/rule-groups/recovery", exported.Body.String(), "", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	after, err := s.AlertRule(ctx, "recovery")
	require.NoError(t, err)
	assert.Equal(t, before.Version, after.Version)
	assert.Equal(t, before.Runtime, after.Runtime)
	assert.Contains(t, string(after.Grafana), `"loadedFingerprints":["`+cachedFP+`"]`, "ephemeral evaluation state must not replace provisioned models")
	history, err := s.AlertHistory(ctx, "recovery", 100)
	require.NoError(t, err)
	assert.Len(t, history, 9)
	annotations, err := s.Annotations(ctx, model.AnnotationQuery{AlertUID: "recovery", Limit: 100})
	require.NoError(t, err)
	assert.Len(t, annotations, len(history))
}

func TestRecoveryExpressionQueriesAcceptExactFingerprintsAndLegacyFrames(t *testing.T) {
	_, handler := setup(t, "")
	fp := uint64(data.Labels(nil).Fingerprint())
	frame := data.NewFrame("", data.NewField("fingerprints", nil, []uint64{fp}))
	frame.SetMeta(&data.FrameMeta{Type: "fingerprints", TypeVersion: data.FrameTypeVersion{1, 0}})
	legacy, err := json.Marshal(frame)
	require.NoError(t, err)
	for _, test := range []struct {
		name, extra string
		want        float64
	}{
		{"new", `"loadedFingerprints":[]`, 0},
		{"loaded", `"loadedFingerprints":["` + strconv.FormatUint(fp, 10) + `"]`, 1},
		{"legacy", `"loadedDimensions":` + string(legacy), 1},
		{"new list overrides legacy", `"loadedFingerprints":[],"loadedDimensions":` + string(legacy), 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{"from":"1000","to":"4000","queries":[{"refId":"A","datasource":{"uid":"metricspanel"},"expr":"vector(90)","instant":true},{"refId":"C","datasource":{"uid":"__expr__"},"type":"threshold","expression":"A","conditions":[{"evaluator":{"type":"gt","params":[100]},"unloadEvaluator":{"type":"lt","params":[80]},` + test.extra + `}]}]}`
			for _, path := range []string{"/api/ds/query", "/apis/__expr__.datasource.grafana.app/v0alpha1/namespaces/default/connections/__expr__/query"} {
				w := call(handler, "POST", path, body, "", "")
				require.Equal(t, 200, w.Code, w.Body.String())
				var result backend.QueryDataResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
				r := result.Responses["C"]
				require.NoError(t, r.Error)
				require.Len(t, r.Frames, 1)
				n, err := r.Frames[0].Fields[0].NullableFloatAt(0)
				require.NoError(t, err)
				require.NotNil(t, n)
				assert.Equal(t, test.want, *n)
			}
		})
	}
}
