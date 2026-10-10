package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/neko233-com/MetricsPanel233/internal/alerting"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/promcompat"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAutomaticAlertAnnotationsStatesTagsDurabilityAndLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	rule := model.AlertRule{UID: "auto", Title: "API latency", Expr: "vector(1)", Condition: "nonzero", ForSeconds: 1, KeepFiringForSeconds: 1, NoDataState: "NoData", ErrorState: "Error", Annotations: map[string]string{"__dashboardUid__": "linked", "__panelId__": "2"}}
	_, err = s.SaveAlertRule(ctx, rule)
	require.NoError(t, err)
	values := []alerting.Value{{Labels: map[string]string{"service": "api:233", "environment": "prod", "__secret__": "hidden", "long": strings.Repeat("中", 101)}, Value: 1}}
	var queryErr error
	query := func(context.Context, string, time.Time) ([]alerting.Value, error) { return values, queryErr }
	e := alerting.New(s, promcompat.New(s))
	e.Query = query
	base := time.Now().Add(-time.Minute)
	evaluate := func(offset time.Duration) model.AlertRuleView {
		t.Helper()
		view, err := e.Evaluate(ctx, rule.UID, base.Add(offset))
		require.NoError(t, err)
		return view
	}
	evaluate(0)
	evaluate(time.Second)
	evaluate(1500 * time.Millisecond) // unchanged state must not create another annotation
	items, err := s.Annotations(ctx, model.AnnotationQuery{Type: "alert", AlertUID: rule.UID, DashboardUID: "linked", PanelID: 2})
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "Pending", items[0].PrevState)
	assert.Equal(t, "Alerting", items[0].NewState)
	assert.Equal(t, items[0].Time, items[0].TimeEnd)
	assert.Positive(t, items[0].AlertID)
	assert.Zero(t, items[0].UserID)
	assert.Contains(t, items[0].Tags, "service:api_233")
	assert.NotContains(t, items[0].Text, "hidden")
	assert.NotContains(t, strings.Join(items[0].Tags, ","), "long:")
	var data map[string]any
	require.NoError(t, json.Unmarshal(items[0].Data, &data))
	assert.Equal(t, float64(1), data["values"].(map[string]any)["A"])
	filtered, err := s.Annotations(ctx, model.AnnotationQuery{AlertID: items[0].AlertID, Tags: []string{"environment:prod", "service:api_233"}})
	require.NoError(t, err)
	require.Len(t, filtered, 2)
	values[0].Value = 0
	evaluate(2 * time.Second)
	evaluate(3 * time.Second)
	values = nil
	evaluate(4 * time.Second)
	queryErr = errors.New("database down")
	evaluate(5 * time.Second)
	items, err = s.Annotations(ctx, model.AnnotationQuery{AlertUID: rule.UID})
	require.NoError(t, err)
	require.Len(t, items, 6)
	assert.Equal(t, "Error (Error)", items[0].NewState)
	assert.Equal(t, "NoData (NoData)", items[0].PrevState)
	assert.Contains(t, string(items[0].Data), `"error":"database down"`)
	assert.Contains(t, string(items[1].Data), `"noData":true`)
	assert.Equal(t, "Normal", items[2].NewState)
	assert.Equal(t, "Recovering", items[3].NewState)
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	restored, err := s.Annotations(ctx, model.AnnotationQuery{AlertUID: rule.UID})
	require.NoError(t, err)
	assert.Equal(t, items, restored)
	e = alerting.New(s, promcompat.New(s))
	e.Query = query
	values, queryErr = []alerting.Value{{Value: 1, Labels: map[string]string{"service": "api"}}}, nil
	view := evaluate(6 * time.Second)
	view.Paused = true
	_, err = e.SaveRule(ctx, view.AlertRule)
	require.NoError(t, err)
	items, err = s.Annotations(ctx, model.AnnotationQuery{AlertUID: rule.UID})
	require.NoError(t, err)
	assert.Equal(t, "Normal (Paused)", items[0].NewState)
	assert.Equal(t, "linked", items[0].DashboardUID)
	view, err = s.AlertRule(ctx, rule.UID)
	require.NoError(t, err)
	view.Paused = false
	_, err = e.SaveRule(ctx, view.AlertRule)
	require.NoError(t, err)
	evaluate(7 * time.Second)
	require.NoError(t, e.DeleteRule(ctx, rule.UID))
	items, err = s.Annotations(ctx, model.AnnotationQuery{AlertUID: rule.UID})
	require.NoError(t, err)
	assert.Equal(t, "Normal (Deleted)", items[0].NewState)
	assert.Equal(t, restored[0].AlertID, items[0].AlertID, "rule deletion must preserve numeric identity and history")
	manual, err := s.Annotations(ctx, model.AnnotationQuery{Type: "annotation"})
	require.NoError(t, err)
	assert.Empty(t, manual)
	tags, err := s.AnnotationTags(ctx, "service:", 100, "alert")
	require.NoError(t, err)
	assert.NotEmpty(t, tags)
	tags, err = s.AnnotationTags(ctx, "service:", 100, "annotation")
	require.NoError(t, err)
	assert.Empty(t, tags)
}

func TestAlertAnnotationRollbackRetryAndBoundedRetention(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "atomic.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	view, err := s.SaveAlertRule(ctx, model.AlertRule{UID: "atomic", Title: "Atomic", Expr: "vector(1)"})
	require.NoError(t, err)
	value := 1.0
	runtime := model.AlertRuntime{LastEvaluation: 1000, Health: "ok", Instances: []model.AlertInstance{{Key: "instance", State: "Firing", Value: &value}}}
	events := []model.AlertEvent{{UID: "atomic", Key: "instance", From: "Normal", To: "Firing", Timestamp: 1000, Value: &value}}
	_, err = s.DB.Exec(`CREATE TRIGGER fail_projection BEFORE INSERT ON annotation_alerts BEGIN SELECT RAISE(ABORT,'projection unavailable'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, s.CommitAlertEvaluation(ctx, view.UID, view.Version, runtime, events), "projection unavailable")
	view, err = s.AlertRule(ctx, view.UID)
	require.NoError(t, err)
	assert.Zero(t, view.Runtime.LastEvaluation)
	history, err := s.AlertHistory(ctx, view.UID, 10)
	require.NoError(t, err)
	assert.Empty(t, history)
	items, err := s.Annotations(ctx, model.AnnotationQuery{})
	require.NoError(t, err)
	assert.Empty(t, items)
	_, err = s.DB.Exec(`DROP TRIGGER fail_projection`)
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { errs <- s.CommitAlertEvaluation(ctx, view.UID, view.Version, runtime, events) })
	}
	wg.Wait()
	close(errs)
	succeeded, stale := 0, 0
	for err := range errs {
		if err == nil {
			succeeded++
		} else {
			require.ErrorIs(t, err, store.ErrStaleEvaluation)
			stale++
		}
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, stale)
	items, err = s.Annotations(ctx, model.AnnotationQuery{Type: "alert"})
	require.NoError(t, err)
	require.Len(t, items, 1)
	manual, err := s.CreateAnnotation(ctx, model.Annotation{Time: 1000, Text: "Keep manual note", Tags: []string{"manual"}}, "")
	require.NoError(t, err)
	_, err = s.DB.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<100005) INSERT INTO alert_events(uid,timestamp,payload) SELECT 'atomic',i,'{"uid":"atomic","from":"Normal","to":"Firing"}' FROM n`)
	require.NoError(t, err)
	view, err = s.AlertRule(ctx, view.UID)
	require.NoError(t, err)
	view.Paused = true
	_, err = s.SaveAlertRule(ctx, view.AlertRule)
	require.NoError(t, err)
	items, err = s.Annotations(ctx, model.AnnotationQuery{Type: "alert"})
	require.NoError(t, err)
	require.Len(t, items, 1, "expired automatic annotation must be removed along with bounded history")
	assert.Equal(t, "Normal (Paused)", items[0].NewState)
	items, err = s.Annotations(ctx, model.AnnotationQuery{Type: "annotation"})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, manual.ID, items[0].ID)
}

func TestAlertLinksAndLargeLabelsCannotBreakAnnotationCommit(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "large.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	for _, link := range []map[string]string{{"__dashboardUid__": "linked"}, {"__panelId__": "1"}, {"__dashboardUid__": "linked", "__panelId__": "-1"}} {
		_, err := s.SaveAlertRule(ctx, model.AlertRule{UID: "invalid", Title: "Invalid", Expr: "vector(1)", Annotations: link})
		require.ErrorContains(t, err, "dashboard link")
	}
	view, err := s.SaveAlertRule(ctx, model.AlertRule{UID: "large", Title: strings.Repeat("中", 80), Expr: "vector(1)"})
	require.NoError(t, err)
	labels := map[string]string{}
	for i := range 32 {
		labels["label"+string(rune('A'+i))] = strings.Repeat("中", 170)
	}
	event := model.AlertEvent{UID: view.UID, From: "Normal", To: "Firing", Timestamp: 1000, Labels: labels}
	require.NoError(t, s.CommitAlertEvaluation(ctx, view.UID, view.Version, model.AlertRuntime{LastEvaluation: 1000}, []model.AlertEvent{event}))
	items, err := s.Annotations(ctx, model.AnnotationQuery{AlertUID: view.UID})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.LessOrEqual(t, len(items[0].Text), 8192)
	assert.True(t, utf8.ValidString(items[0].Text))
}
