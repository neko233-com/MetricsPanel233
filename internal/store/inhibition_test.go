package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	amlabels "github.com/prometheus/alertmanager/pkg/labels"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func inhibitionTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "inhibition.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.DB.Close() })
	return s
}
func inhibitionTestRuntime(t *testing.T, s *Store, instances []model.AlertInstance, at int64) {
	t.Helper()
	ctx := context.Background()
	v, err := s.AlertRule(ctx, "source")
	if err != nil {
		v, err = s.SaveAlertRule(ctx, model.AlertRule{UID: "source", Title: "Source", Expr: "vector(1)", Condition: "nonzero"})
	}
	require.NoError(t, err)
	require.NoError(t, s.CommitAlertEvaluation(ctx, v.UID, v.Version, model.AlertRuntime{LastEvaluation: at, Health: "ok", Instances: instances}, nil))
}
func previewRule(source, target string) model.InhibitionPreviewInput {
	return model.InhibitionPreviewInput{SourceMatchers: []model.NotificationMatcher{{Name: "role", Operator: "=~", Value: source}}, TargetMatchers: []model.NotificationMatcher{{Name: "role", Operator: "=~", Value: target}}, Equal: []string{"rack"}}
}

func TestInhibitionAMTwoSidedAndMissingEmptySemantics(t *testing.T) {
	s := inhibitionTestStore(t)
	ctx := context.Background()
	instances := []model.AlertInstance{{Key: "both-1", State: "Firing", Labels: map[string]string{"role": "both"}}, {Key: "both-2", State: "Recovering", Labels: map[string]string{"role": "both", "rack": ""}}, {Key: "target", State: "Firing", Labels: map[string]string{"role": "target"}}, {Key: "pending", State: "Pending", Labels: map[string]string{"role": "source"}}}
	inhibitionTestRuntime(t, s, instances, 1000)
	input := previewRule("source|both", "target|both")
	candidate := map[string]string{"role": "both", "rack": ""}
	input.Labels = &candidate
	out, err := s.PreviewInhibition(ctx, input)
	require.NoError(t, err)
	assert.True(t, out.TargetMatches)
	assert.True(t, out.SourceMatches)
	assert.True(t, out.ExcludedSelfMatch)
	assert.False(t, out.Inhibited)
	assert.Zero(t, out.SourceCount)
	for _, v := range out.Targets {
		if v.Target.InstanceKey == "target" {
			assert.True(t, v.Inhibited)
			assert.Equal(t, 2, v.SourceCount, "a target-only instance may be inhibited by two-sided sources")
		}
	}
	instances = append(instances, model.AlertInstance{Key: "source-only", State: "Error", Labels: map[string]string{"role": "source"}})
	inhibitionTestRuntime(t, s, instances, 2000)
	out, err = s.PreviewInhibition(ctx, input)
	require.NoError(t, err)
	assert.True(t, out.Inhibited)
	assert.Equal(t, 1, out.SourceCount)
	require.Len(t, out.Sources, 1)
	assert.Equal(t, "source-only", out.Sources[0].InstanceKey)
	input.Disabled = true
	out, err = s.PreviewInhibition(ctx, input)
	require.NoError(t, err)
	assert.False(t, out.Inhibited)
	assert.Equal(t, 1, out.SourceCount, "disabled preview still reports real matching source count")
	// Missing and empty equal labels share the same key; a different value does not.
	input.Disabled = false
	candidate["rack"] = "different"
	out, err = s.PreviewInhibition(ctx, input)
	require.NoError(t, err)
	assert.False(t, out.Inhibited)
	input.Labels = nil
	out, err = s.PreviewInhibition(ctx, input)
	require.NoError(t, err)
	assert.False(t, out.TargetMatches)
	assert.False(t, out.SourceMatches)
	input.Labels = &map[string]string{}
	out, err = s.PreviewInhibition(ctx, input)
	require.NoError(t, err)
	assert.False(t, out.TargetMatches)
}

func TestInhibitionCompiledMatchersMatchPinnedAlertmanager(t *testing.T) {
	types := map[string]amlabels.MatchType{"=": amlabels.MatchEqual, "!=": amlabels.MatchNotEqual, "=~": amlabels.MatchRegexp, "!~": amlabels.MatchNotRegexp}
	for _, operator := range []string{"=", "!=", "=~", "!~"} {
		for _, value := range []string{"", "a|b", ".*", "(?s:.*)", ".*\\n.*"} {
			matcher := model.NotificationMatcher{Name: "job", Operator: operator, Value: value}
			c, err := compileInhibition(model.InhibitionRule{SourceMatchers: []model.NotificationMatcher{matcher}})
			require.NoError(t, err)
			upstream, err := amlabels.NewMatcher(types[operator], "job", value)
			require.NoError(t, err)
			for _, candidate := range []string{"", "a", "aa", "b", "line\nline", "missing"} {
				got := inhibitionMatches(c.source, map[string]string{"job": candidate})
				assert.Equal(t, upstream.Matches(candidate), got, "operator=%s value=%q candidate=%q", operator, value, candidate)
			}
		}
	}
	// Cache eviction is bounded and safe during draft preview churn.
	for i := range 300 {
		_, err := compileInhibition(model.InhibitionRule{SourceMatchers: []model.NotificationMatcher{{Name: "job", Operator: "=", Value: fmt.Sprint(i)}}})
		require.NoError(t, err)
	}
	inhibitionMatcherCache.Lock()
	assert.LessOrEqual(t, len(inhibitionMatcherCache.values), 256)
	assert.LessOrEqual(t, len(inhibitionMatcherCache.order), 256)
	inhibitionMatcherCache.Unlock()
}

func TestInhibitionDurableIndexCASAndIndexedLookup(t *testing.T) {
	s := inhibitionTestStore(t)
	ctx := context.Background()
	v, err := s.SaveInhibitionRule(ctx, model.InhibitionRule{UID: "causes", Name: "Causes", SourceMatchers: []model.NotificationMatcher{{Name: "role", Operator: "=", Value: "source"}}, TargetMatchers: []model.NotificationMatcher{{Name: "role", Operator: "!=", Value: "source"}}, Equal: []string{}})
	require.NoError(t, err)
	inhibitionTestRuntime(t, s, []model.AlertInstance{{Key: "s", State: "NoData", Labels: map[string]string{"role": "source"}}}, 1000)
	var count int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_inhibition_sources`).Scan(&count))
	assert.Equal(t, 1, count)
	rule, err := s.AlertRule(ctx, "source")
	require.NoError(t, err)
	err = s.CommitAlertEvaluation(ctx, rule.UID, rule.Version+1, model.AlertRuntime{LastEvaluation: 2000, Instances: []model.AlertInstance{}}, nil)
	assert.ErrorIs(t, err, ErrStaleEvaluation)
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_inhibition_sources`).Scan(&count))
	assert.Equal(t, 1, count)
	err = s.CommitAlertEvaluation(ctx, rule.UID, rule.Version, model.AlertRuntime{LastEvaluation: 1000, Instances: []model.AlertInstance{}}, nil)
	assert.ErrorIs(t, err, ErrStaleEvaluation)
	var id, parent, unused int
	var detail string
	require.NoError(t, s.DB.QueryRow(`EXPLAIN QUERY PLAN SELECT 1 FROM alert_inhibition_sources WHERE inhibition_uid=? AND equal_key=? AND (?=0 OR two_sided=0)`, v.UID, "key", true).Scan(&id, &parent, &unused, &detail))
	assert.Contains(t, detail, "alert_inhibition_equal")
	assert.Contains(t, strings.ToUpper(detail), "SEARCH")
	// Source visibility is independent of alert history and completed outbox retention.
	_, err = s.DB.Exec(`DELETE FROM alert_events`)
	require.NoError(t, err)
	active, err := s.InhibitionActive(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, active.Total)
	assert.Equal(t, "NoData", active.Instances[0].State)
	view, err := s.SaveAlertRule(ctx, rule.AlertRule)
	require.NoError(t, err)
	assert.Empty(t, view.Runtime.Instances)
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_inhibition_sources`).Scan(&count))
	assert.Zero(t, count)
	_, err = s.SaveInhibitionRule(ctx, v)
	require.NoError(t, err)
	assert.ErrorIs(t, s.DeleteInhibitionRule(ctx, v.UID, v.Version), ErrNotificationConflict)
}

func TestInhibitionPreviewCountsBoundedAndReadOnly(t *testing.T) {
	s := inhibitionTestStore(t)
	instances := []model.AlertInstance{}
	for i := range 72 {
		instances = append(instances, model.AlertInstance{Key: fmt.Sprintf("s%02d", i), State: "Firing", Labels: map[string]string{"role": "source"}})
	}
	for i := range 60 {
		instances = append(instances, model.AlertInstance{Key: fmt.Sprintf("t%02d", i), State: "Firing", Labels: map[string]string{"role": "target"}})
	}
	inhibitionTestRuntime(t, s, instances, 1000)
	input := previewRule("source", "target")
	candidate := map[string]string{"role": "target"}
	input.Labels = &candidate
	out, err := s.PreviewInhibition(context.Background(), input)
	require.NoError(t, err)
	assert.Equal(t, 72, out.SourceCount)
	require.Len(t, out.Sources, 20)
	assert.Equal(t, 60, out.TotalTargets)
	require.Len(t, out.Targets, 50)
	assert.True(t, out.Truncated)
	for _, target := range out.Targets {
		assert.Equal(t, 72, target.SourceCount)
		assert.Len(t, target.Sources, 20)
	}
	var count int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_notification_outbox`).Scan(&count))
	assert.Zero(t, count)
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_inhibition_rules`).Scan(&count))
	assert.Zero(t, count)
	active, err := s.InhibitionActive(context.Background(), 200)
	require.NoError(t, err)
	assert.Equal(t, 132, active.Total)
	require.Len(t, active.Instances, 132)
	assert.False(t, active.Truncated)
	input.TargetMatchers[0].Value = "("
	_, err = s.PreviewInhibition(context.Background(), input)
	require.Error(t, err)
	_, err = s.InhibitionRule(context.Background(), "absent")
	assert.True(t, errors.Is(err, sql.ErrNoRows))
}
