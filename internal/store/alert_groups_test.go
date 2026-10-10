package store_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlertGroupRollbackIncludesDeletesSchedulesHistoryAndAnnotations(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "group.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	rules := []model.AlertRule{{UID: "a", Title: "A", Expr: "vector(1)"}, {UID: "b", Title: "B", Expr: "vector(1)"}}
	views, err := s.ReplaceAlertRuleGroup(ctx, "general", "group", 30, rules, "api")
	require.NoError(t, err)
	for _, view := range views {
		runtime := model.AlertRuntime{LastEvaluation: 100000, Health: "ok", Instances: []model.AlertInstance{{Key: view.UID, Labels: map[string]string{"alertname": view.Title}, State: "Firing", ActiveAt: 90000, FiringAt: 100000}}}
		require.NoError(t, s.CommitAlertEvaluation(ctx, view.UID, view.Version, runtime, []model.AlertEvent{{UID: view.UID, Key: view.UID, Labels: runtime.Instances[0].Labels, From: "Normal", To: "Firing", Timestamp: 100000}}))
	}
	before, err := s.AlertRules(ctx)
	require.NoError(t, err)
	history, err := s.AlertHistory(ctx, "", 100)
	require.NoError(t, err)
	annotations, err := s.Annotations(ctx, model.AnnotationQuery{})
	require.NoError(t, err)
	counts := func() map[string]int {
		result := map[string]int{}
		for _, table := range []string{"alert_rules", "alert_schedule", "alert_events", "annotations", "annotation_tags", "annotation_alerts", "alert_rule_identity"} {
			var count int
			require.NoError(t, s.DB.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
			result[table] = count
		}
		return result
	}
	beforeCounts := counts()
	_, err = s.DB.Exec(`CREATE TRIGGER fail_group BEFORE INSERT ON alert_rules WHEN NEW.uid='reject' BEGIN SELECT RAISE(ABORT,'group rollback fixture'); END`)
	require.NoError(t, err)
	_, err = s.ReplaceAlertRuleGroup(ctx, "general", "group", 60, []model.AlertRule{{UID: "a", Title: "Changed", Expr: "vector(0)"}, {UID: "reject", Title: "Reject", Expr: "vector(1)"}}, "")
	require.ErrorContains(t, err, "group rollback fixture")
	after, err := s.AlertRules(ctx)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Equal(t, beforeCounts, counts())
	afterHistory, err := s.AlertHistory(ctx, "", 100)
	require.NoError(t, err)
	assert.Equal(t, history, afterHistory)
	afterAnnotations, err := s.Annotations(ctx, model.AnnotationQuery{})
	require.NoError(t, err)
	assert.Equal(t, annotations, afterAnnotations)
	_, err = s.DB.Exec(`DROP TRIGGER fail_group`)
	require.NoError(t, err)
	updated, err := s.ReplaceAlertRuleGroup(ctx, "general", "group", 60, []model.AlertRule{{UID: "a", Title: "Changed", Expr: "vector(0)"}}, "")
	require.NoError(t, err)
	require.Len(t, updated, 1)
	assert.Empty(t, updated[0].Runtime.Instances)
	assert.Equal(t, 1, counts()["alert_schedule"])
	afterHistory, err = s.AlertHistory(ctx, "", 100)
	require.NoError(t, err)
	require.Len(t, afterHistory, 4)
	assert.Equal(t, "RuleUpdated", afterHistory[0].Reason)
	assert.Equal(t, "RuleDeleted", afterHistory[1].Reason)
}

func TestAlertGroupQuotaReplacementAndFailureAreAtomic(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "quota.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	_, err = s.ReplaceAlertRuleGroup(ctx, "general", "replace", 30, []model.AlertRule{{UID: "old", Title: "Old", Expr: "vector(1)"}}, "api")
	require.NoError(t, err)
	base := model.AlertRule{UID: "quota", Title: "Quota", Expr: "vector(1)", FolderUID: "general", Group: "others", Paused: true, Version: 1}
	base.Defaults()
	raw, err := json.Marshal(base)
	require.NoError(t, err)
	_, err = s.DB.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<999) INSERT INTO alert_rules(uid,version,config,runtime,last_evaluation) SELECT 'quota-'||i,1,json_set(?,'$.uid','quota-'||i,'$.title','Quota '||i),'{"health":"paused","instances":[]}',0 FROM n`, string(raw))
	require.NoError(t, err)
	_, err = s.DB.Exec(`INSERT INTO alert_schedule SELECT uid,30,1 FROM alert_rules WHERE uid LIKE 'quota-%'`)
	require.NoError(t, err)
	_, err = s.ReplaceAlertRuleGroup(ctx, "general", "replace", 30, []model.AlertRule{{UID: "new", Title: "New", Expr: "vector(1)"}, {UID: "excess", Title: "Excess", Expr: "vector(1)"}}, "api")
	require.ErrorContains(t, err, "1000")
	_, err = s.AlertRule(ctx, "old")
	require.NoError(t, err)
	_, err = s.AlertRule(ctx, "new")
	require.Error(t, err)
	views, err := s.ReplaceAlertRuleGroup(ctx, "general", "replace", 30, []model.AlertRule{{UID: "new", Title: "New", Expr: "vector(1)"}}, "api")
	require.NoError(t, err)
	require.Len(t, views, 1)
	var count int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_rules`).Scan(&count))
	assert.Equal(t, 1000, count)
}
