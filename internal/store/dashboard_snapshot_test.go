package store_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/grafana"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/require"
)

func TestDashboardSnapshotSurvivesRestartAndSourceDeletion(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "snapshots.db")
	original := json.RawMessage(`{"uid":"snapshot-durable","title":"Saved panels","time":{"from":"2026-10-05T08:00:00Z","to":"2026-10-05T18:00:00Z"},"refresh":"","snapshot":{"created":1791183600000,"sourceUID":"deleted-source"},"annotations":{"list":[]},"panels":[{"id":1,"title":"Orders","type":"stat","datasource":{"uid":"grafana","type":"grafana"},"targets":[{"refId":"Snapshot","queryType":"snapshot","snapshot":[{"schema":{"refId":"A","fields":[{"name":"Value","type":"number","config":{}}]},"data":{"values":[[233]]}}]}],"fieldConfig":{"defaults":{},"overrides":[]},"gridPos":{"x":0,"y":0,"w":12,"h":8}},{"id":2,"title":"Table","type":"table","datasource":{"uid":"grafana","type":"grafana"},"targets":[{"refId":"Snapshot","queryType":"snapshot","snapshot":[{"schema":{"refId":"B","fields":[{"name":"Value","type":"number","config":{}}]},"data":{"values":[[777]]}}]}],"gridPos":{"x":12,"y":0,"w":12,"h":8}}]}`)
	imported, err := grafana.Import(original)
	require.NoError(t, err)
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	_, err = db.SaveDashboard(ctx, model.Dashboard{ID: "deleted-source", Name: "Live orders", Panels: []model.Panel{{ID: "orders", Title: "Orders", Metric: "orders", Aggregation: "last"}}})
	require.NoError(t, err)
	saved, err := db.SaveDashboard(ctx, imported.Dashboard)
	require.NoError(t, err)
	require.NoError(t, db.DeleteDashboard(ctx, "deleted-source"))
	require.NoError(t, db.DB.Close())
	db, err = store.Open(dbPath)
	require.NoError(t, err)
	defer db.DB.Close()
	dashboards, err := db.Dashboards(ctx)
	require.NoError(t, err)
	found := false
	for _, dashboard := range dashboards {
		if dashboard.ID != saved.ID {
			continue
		}
		found = true
		require.JSONEq(t, string(original), string(dashboard.Grafana))
		before, err := json.Marshal(saved.Panels)
		require.NoError(t, err)
		after, err := json.Marshal(dashboard.Panels)
		require.NoError(t, err)
		require.JSONEq(t, string(before), string(after))
		require.Len(t, dashboard.Panels, 2)
	}
	require.True(t, found)
	metrics, err := db.Metrics(ctx)
	require.NoError(t, err)
	require.Empty(t, metrics, "snapshot values must not become monitoring samples")
}
