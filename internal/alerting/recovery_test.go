package alerting

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/promcompat"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoveryReadsOnlyRealPendingAndFiringDimensions(t *testing.T) {
	for _, test := range []struct {
		state, reason string
		loaded        bool
	}{
		{"Pending", "", true}, {"Firing", "", true}, {"Recovering", "", false},
		{"Normal", "", false}, {"NoData", "", false}, {"Error", "", false},
		{"Firing", "NoData", false}, {"Pending", "QueryError", false}, {"Firing", "MissingSeries", false},
	} {
		t.Run(test.state+"/"+test.reason, func(t *testing.T) {
			ctx := context.Background()
			s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
			require.NoError(t, err)
			defer s.DB.Close()
			r, err := CompileGrafana(grafRule(t, `{"uid":"recover","title":"Recover","condition":"C","data":[{"refId":"A","datasourceUid":"source","model":{"custom":true}},{"refId":"C","datasourceUid":"__expr__","model":{"type":"threshold","expression":"A","conditions":[{"evaluator":{"type":"gt","params":[100]},"unloadEvaluator":{"type":"lt","params":[80]}}]}}]}`), 15)
			require.NoError(t, err)
			e := New(s, promcompat.New(s))
			labels := data.Labels{"host": "go"}
			e.GraphSource = func(context.Context, string, []backend.DataQuery) (backend.Responses, string, error) {
				return backend.Responses{"A": {Frames: data.Frames{data.NewFrame("", data.NewField("A", labels, []float64{90}))}}}, "backend", nil
			}
			view, err := e.SaveRule(ctx, r)
			require.NoError(t, err)
			ls := mergedLabels(labels, r.Labels, r.Title)
			previous := model.AlertRuntime{LastEvaluation: 100000, Health: "ok", Instances: []model.AlertInstance{{Key: instanceKey(ls), Labels: ls, State: test.state, Reason: test.reason, ActiveAt: 90000, FiringAt: 90000, ResultFingerprint: strconv.FormatUint(uint64(labels.Fingerprint()), 10)}}}
			require.NoError(t, s.CommitAlertEvaluation(ctx, r.UID, view.Version, previous, nil))
			next, err := e.Evaluate(ctx, r.UID, time.UnixMilli(110000))
			require.NoError(t, err)
			require.Equal(t, "ok", next.Runtime.Health, next.Runtime.Error)
			require.Len(t, next.Runtime.Instances, 1)
			want := float64(0)
			if test.loaded {
				want = 1
			}
			assert.Equal(t, want, *next.Runtime.Instances[0].Value)
			wantState := "Normal"
			if test.loaded {
				wantState = "Firing"
			}
			assert.Equal(t, wantState, next.Runtime.Instances[0].State)
		})
	}
}
