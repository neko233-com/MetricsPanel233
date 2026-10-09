package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlertHistoryBoundAndScheduleLifecycle(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "alerts.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	ctx := context.Background()
	r := model.AlertRule{UID: "history", Title: "History test", Expr: "vector(1)", IntervalSeconds: 5}
	r.Defaults()
	view, err := s.SaveAlertRule(ctx, r)
	require.NoError(t, err)
	now := time.Now().UnixMilli()
	due, err := s.DueAlertRules(ctx, now)
	require.NoError(t, err)
	assert.Equal(t, []string{"history"}, due)
	_, err = s.DB.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<100005) INSERT INTO alert_events(uid,timestamp,payload) SELECT 'history',i,'{"uid":"history","from":"Normal","to":"Firing"}' FROM n`)
	require.NoError(t, err)
	view.Paused = true
	updated, err := s.SaveAlertRule(ctx, view.AlertRule)
	require.NoError(t, err)
	assert.Equal(t, 2, updated.Version)
	var count, minID int64
	require.NoError(t, s.DB.QueryRow(`SELECT count(*),min(id) FROM alert_events`).Scan(&count, &minID))
	assert.Equal(t, int64(100000), count)
	assert.Equal(t, int64(6), minID)
	due, err = s.DueAlertRules(ctx, now)
	require.NoError(t, err)
	assert.Empty(t, due)
	history, err := s.AlertHistory(ctx, r.UID, 2)
	require.NoError(t, err)
	require.Len(t, history, 2)
	assert.Greater(t, history[0].ID, history[1].ID)
	require.NoError(t, s.DeleteAlertRule(ctx, r.UID))
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_schedule`).Scan(&count))
	assert.Zero(t, count, "deleted rule left a scheduled job")
	history, err = s.AlertHistory(ctx, r.UID, 2)
	require.NoError(t, err)
	require.Len(t, history, 2, "rule deletion must retain its transition history")
}
