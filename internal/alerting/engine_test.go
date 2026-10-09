package alerting

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/promcompat"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRule() model.AlertRule {
	r := model.AlertRule{UID: "test", Title: "CPU high", Expr: "cpu > bool 80", Condition: "nonzero", ForSeconds: 10, KeepFiringForSeconds: 20}
	r.Defaults()
	return r
}
func TestPendingFiringRecoveryAndSeriesIsolation(t *testing.T) {
	r := testRule()
	previous := model.AlertRuntime{}
	base := int64(100000)
	values := []Value{{Labels: map[string]string{"job": "api"}, Value: 1}, {Labels: map[string]string{"job": "mysql"}, Value: 0}}
	step := func(delta int64) []model.AlertEvent {
		next, events, err := Transition(r, previous, values, nil, base+delta*1000)
		require.NoError(t, err)
		previous = next
		return events
	}
	assert.Len(t, step(0), 1)
	states := func() map[string]string {
		out := map[string]string{}
		for _, v := range previous.Instances {
			out[v.Labels["job"]] = v.State
		}
		return out
	}
	assert.Equal(t, map[string]string{"api": "Pending", "mysql": "Normal"}, states())
	assert.Empty(t, step(9))
	assert.Len(t, step(10), 1)
	assert.Equal(t, "Firing", states()["api"])
	values[0].Value = 0
	assert.Len(t, step(11), 1)
	assert.Equal(t, "Recovering", states()["api"])
	step(30)
	assert.Equal(t, "Recovering", states()["api"])
	values[0].Value = 1
	step(31)
	assert.Equal(t, "Firing", states()["api"])
	values[0].Value = 0
	step(32)
	step(52)
	assert.Equal(t, "Normal", states()["api"])
	values[1].Value = 1
	step(53)
	step(62)
	assert.Equal(t, "Pending", states()["mysql"])
	step(63)
	assert.Equal(t, "Firing", states()["mysql"])
}
func TestPoliciesPresenceMissingSeriesAndLabelCollision(t *testing.T) {
	r := testRule()
	r.ForSeconds = 0
	r.KeepFiringForSeconds = 0
	initial, _, err := Transition(r, model.AlertRuntime{}, []Value{{Labels: map[string]string{"job": "api"}, Value: 1}}, nil, 1000)
	require.NoError(t, err)
	for _, p := range []string{"OK", "Alerting", "NoData", "Error", "KeepLast"} {
		t.Run(p, func(t *testing.T) {
			r.ErrorState = p
			next, _, err := Transition(r, initial, nil, errors.New("database down"), 2000)
			require.NoError(t, err)
			assert.Equal(t, "error", next.Health)
			assert.Equal(t, "database down", next.Error)
			expected := map[string]string{"OK": "Normal", "Alerting": "Firing", "NoData": "NoData", "Error": "Error", "KeepLast": "Firing"}[p]
			assert.Equal(t, expected, next.Instances[0].State)
		})
	}
	r.NoDataState = "KeepLast"
	next, events, err := Transition(r, initial, nil, nil, 3000)
	require.NoError(t, err)
	assert.Empty(t, events)
	assert.Equal(t, "Firing", next.Instances[0].State)
	values := []Value{{Labels: map[string]string{"job": "mysql"}, Value: 0}}
	next, _, err = Transition(r, initial, values, nil, 4000)
	require.NoError(t, err)
	assert.Len(t, next.Instances, 2)
	next, events, err = Transition(r, next, values, nil, 5000)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "MissingSeries", events[0].Reason)
	assert.Equal(t, "Normal", events[0].To)
	r.Condition = "presence"
	next, _, err = Transition(r, model.AlertRuntime{}, []Value{{Value: 0}}, nil, 6000)
	require.NoError(t, err)
	assert.Equal(t, "Firing", next.Instances[0].State, "Prometheus alert comparisons can return zero while firing")
	r.Labels = map[string]string{"job": "all"}
	_, _, err = Transition(r, model.AlertRuntime{}, []Value{{Labels: map[string]string{"job": "a"}, Value: 1}, {Labels: map[string]string{"job": "b"}, Value: 1}}, nil, 7000)
	require.ErrorContains(t, err, "collapse")
}
func TestPersistedTimersVersionCASAndRecordingRules(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "alerts.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	r := testRule()
	r.Expr = "vector(1)"
	saved, err := s.SaveAlertRule(ctx, r)
	require.NoError(t, err)
	assert.Equal(t, 1, saved.Version)
	e := New(s, promcompat.New(s))
	at := time.Now().Add(-time.Minute)
	first, err := e.Evaluate(ctx, r.UID, at)
	require.NoError(t, err)
	assert.Equal(t, "Pending", first.Runtime.Instances[0].State)
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	e = New(s, promcompat.New(s))
	second, err := e.Evaluate(ctx, r.UID, at.Add(10*time.Second))
	require.NoError(t, err)
	assert.Equal(t, "Firing", second.Runtime.Instances[0].State)
	assert.Equal(t, first.Runtime.Instances[0].ActiveAt, second.Runtime.Instances[0].ActiveAt)
	history, err := s.AlertHistory(ctx, r.UID, 100)
	require.NoError(t, err)
	require.Len(t, history, 2)
	assert.Equal(t, "Firing", history[0].To)
	_, err = e.Evaluate(ctx, r.UID, at)
	require.ErrorIs(t, err, store.ErrStaleEvaluation)
	_, err = s.SaveAlertRule(ctx, r)
	require.ErrorIs(t, err, store.ErrRuleConflict)
	paused := second.AlertRule
	paused.Paused = true
	updated, err := e.SaveRule(ctx, paused)
	require.NoError(t, err)
	assert.Empty(t, updated.Runtime.Instances)
	assert.Equal(t, "paused", updated.Runtime.Health)
	err = s.CommitAlertEvaluation(ctx, r.UID, second.Version, second.Runtime, nil)
	require.ErrorIs(t, err, store.ErrStaleEvaluation)
	_, err = e.Evaluate(ctx, r.UID, time.Now())
	require.ErrorContains(t, err, "paused")
	record := model.AlertRule{UID: "record", Title: "Request total", Expr: "sum(vector(233))", Record: "http_requests:sum"}
	record.Defaults()
	_, err = e.SaveRule(ctx, record)
	require.NoError(t, err)
	value, err := e.Evaluate(ctx, "record", time.Now())
	require.NoError(t, err)
	assert.Equal(t, "ok", value.Runtime.Health)
	query, err := s.Query(ctx, model.Query{Metric: record.Record, Aggregation: "last", Start: time.Now().Add(-time.Minute).UnixMilli(), End: time.Now().UnixMilli(), Step: 1000})
	require.NoError(t, err)
	require.Len(t, query.Series, 1)
	assert.Equal(t, 233.0, query.Series[0].Points[0].Value)
}
func TestMutationGateCancellationAndScheduler(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "data.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	ctx := context.Background()
	r := testRule()
	r.ForSeconds = 0
	_, err = s.SaveAlertRule(ctx, r)
	require.NoError(t, err)
	e := New(s, promcompat.New(s))
	entered := make(chan struct{})
	release := make(chan struct{})
	e.Query = func(ctx context.Context, _ string, _ time.Time) ([]Value, error) {
		close(entered)
		select {
		case <-release:
			return []Value{{Value: 1}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	done := make(chan error, 1)
	cancelCtx, cancel := context.WithCancel(ctx)
	go func() { _, err := e.Evaluate(cancelCtx, r.UID, time.Now()); done <- err }()
	<-entered
	_, err = e.SaveRule(ctx, r)
	require.ErrorIs(t, err, ErrBusy)
	require.ErrorIs(t, e.DeleteRule(ctx, r.UID), ErrBusy)
	_, err = e.Evaluate(ctx, r.UID, time.Now())
	require.ErrorIs(t, err, ErrBusy)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	view, err := s.AlertRule(ctx, r.UID)
	require.NoError(t, err)
	assert.Zero(t, view.Runtime.LastEvaluation, "cancellation must not become an error alert")
	var calls atomic.Int32
	e.Query = func(context.Context, string, time.Time) ([]Value, error) {
		calls.Add(1)
		return []Value{{Value: 1}}, nil
	}
	runCtx, stop := context.WithCancel(ctx)
	finished := make(chan struct{})
	go func() { e.Run(runCtx); close(finished) }()
	require.Eventually(t, func() bool { return calls.Load() > 0 }, 3*time.Second, 10*time.Millisecond)
	stop()
	<-finished
}
