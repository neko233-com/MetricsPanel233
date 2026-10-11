package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDashboardHistoryAllSavePathsAndImmutablePrecision(t *testing.T) {
	for _, source := range []string{
		`{"uid":"shared","version":3,"title":"Classic","opaque":900719925474099312345}`,
		`{"apiVersion":"dashboard.grafana.app/v1beta1","metadata":{"name":"shared","opaque":18446744073709551615},"spec":{"version":3,"title":"V1","opaque":900719925474099312345}}`,
		`{"apiVersion":"dashboard.grafana.app/v2beta1","metadata":{"name":"shared","generation":3},"spec":{"title":"V2","elements":{},"opaque":900719925474099312345}}`,
	} {
		t.Run(source, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "history.db")
			s, err := store.Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = s.DB.Close() })
			ctx := context.Background()
			folder, err := s.CreateFolder(ctx, model.Folder{UID: "saved-folder", Title: "Saved folder"})
			require.NoError(t, err)
			d := model.Dashboard{ID: "history", Name: "Saved", FolderUID: folder.UID, PluginID: "clock-panel", PluginPath: "dashboard.json", Grafana: json.RawMessage(source), Annotations: json.RawMessage(`[{"enable":true,"opaque":18446744073709551615}]`), Panels: []model.Panel{}}
			initialCtx := store.WithDashboardHistoryMetadata(ctx, model.DashboardHistoryMetadata{Message: "Original", CreatedBy: "admin"})
			d, err = s.SaveDashboard(initialCtx, d)
			require.NoError(t, err)
			assert.Equal(t, int64(3), d.GrafanaVersion)
			original := d
			d.Name = "Revised"
			d, err = s.SaveDashboardAtRevision(ctx, d, d.UpdatedAt)
			require.NoError(t, err)
			assert.Equal(t, int64(4), d.GrafanaVersion)
			d.Name = "Library path"
			revision := d.UpdatedAt
			d, err = s.SaveDashboardWithLibraryElements(ctx, d, nil, &revision)
			require.NoError(t, err)
			assert.Equal(t, int64(5), d.GrafanaVersion)
			list, err := s.DashboardVersions(ctx, d.ID, 1000, 0, "")
			require.NoError(t, err)
			require.Len(t, list.Versions, 3)
			assert.Equal(t, []int64{5, 4, 3}, historyVersionNumbers(list))
			assert.Equal(t, int64(4), list.Versions[0].ParentVersion)
			assert.Equal(t, int64(3), list.Versions[1].ParentVersion)
			assert.Zero(t, list.Versions[2].ParentVersion)
			assert.Empty(t, list.Versions[0].Snapshot.ID, "list must return metadata without decoding snapshot")
			assert.NotEqual(t, list.Versions[2].ID, list.Versions[2].Version, "version lookup must use local version, not global history ID")
			v, err := s.DashboardVersion(ctx, d.ID, 3)
			require.NoError(t, err)
			assert.Equal(t, original, v.Snapshot)
			assert.Contains(t, string(v.Snapshot.Grafana), "900719925474099312345")
			assert.Contains(t, string(v.Snapshot.Annotations), "18446744073709551615")
			assert.Equal(t, "Original", v.Message)
			assert.Equal(t, "admin", v.CreatedBy)
			assert.Equal(t, original.GrafanaID, v.DashboardID)
			assert.Equal(t, "shared", v.DashboardUID)
			// A restored save advances history rather than replacing the old revision.
			draft := original
			restoredCtx := store.WithDashboardHistoryMetadata(ctx, model.DashboardHistoryMetadata{RestoredFrom: 3})
			draft, err = s.SaveDashboardAtRevision(restoredCtx, draft, d.UpdatedAt)
			require.NoError(t, err)
			assert.Equal(t, int64(6), draft.GrafanaVersion)
			v, err = s.DashboardVersion(ctx, d.ID, 6)
			require.NoError(t, err)
			assert.Equal(t, int64(5), v.ParentVersion)
			assert.Equal(t, int64(3), v.RestoredFrom)
			assert.Equal(t, "Restored from version 3", v.Message)
			draft.Name = "Restored with a note"
			notedCtx := store.WithDashboardHistoryMetadata(ctx, model.DashboardHistoryMetadata{RestoredFrom: 3, Message: "Return to the verified layout"})
			draft, err = s.SaveDashboardAtRevision(notedCtx, draft, draft.UpdatedAt)
			require.NoError(t, err)
			v, err = s.DashboardVersion(ctx, d.ID, 7)
			require.NoError(t, err)
			assert.Equal(t, "Return to the verified layout", v.Message)
			assert.Equal(t, int64(3), v.RestoredFrom)
			require.NoError(t, s.DB.Close())
			s, err = store.Open(path)
			require.NoError(t, err)
			v, err = s.DashboardVersion(ctx, d.ID, 3)
			require.NoError(t, err)
			assert.Equal(t, original, v.Snapshot)
			list, err = s.DashboardVersions(ctx, d.ID, 1000, 0, "")
			require.NoError(t, err)
			assert.Len(t, list.Versions, 5, "reopening must not duplicate current backfill")
		})
	}
}

func historyVersionNumbers(list model.DashboardVersionList) []int64 {
	versions := []int64{}
	for _, v := range list.Versions {
		versions = append(versions, v.Version)
	}
	return versions
}

func TestDashboardHistoryStableContinuationAndResourceScope(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "history.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	ctx := context.Background()
	d := model.Dashboard{ID: "pages", Name: "Paged", Panels: []model.Panel{}}
	for range 5 {
		d, err = s.SaveDashboard(ctx, d)
		require.NoError(t, err)
	}
	first, err := s.DashboardVersions(ctx, d.ID, 2, 1, "")
	require.NoError(t, err)
	assert.Equal(t, []int64{4, 3}, historyVersionNumbers(first))
	require.NotEmpty(t, first.ContinueToken)
	d, err = s.SaveDashboard(ctx, d)
	require.NoError(t, err)
	next, err := s.DashboardVersions(ctx, d.ID, 2, 1, first.ContinueToken)
	require.NoError(t, err)
	assert.Equal(t, []int64{2, 1}, historyVersionNumbers(next))
	assert.Empty(t, next.ContinueToken)
	_, err = s.DashboardVersions(ctx, "system", 2, 0, first.ContinueToken)
	require.ErrorContains(t, err, "continuation token")
	_, err = s.DashboardVersions(ctx, d.ID, 2, 0, "invalid")
	require.ErrorContains(t, err, "continuation token")
	_, err = s.DashboardVersions(ctx, d.ID, 1001, 0, "")
	require.Error(t, err)
	_, err = s.DashboardVersions(ctx, d.ID, 2, -1, "")
	require.Error(t, err)
	_, err = s.DashboardVersions(ctx, "absent", 2, 0, "")
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = s.DashboardVersion(ctx, d.ID, 999)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = s.DashboardVersion(ctx, d.ID, 0)
	require.Error(t, err)
}

func TestDashboardHistoryBackfillRetainsOnlyAvailableRowWithoutRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	raw := `{"grafana":{"uid":"old","version":17,"title":"Old","opaque":900719925474099312345},"annotations":[{"opaque":18446744073709551615}]}`
	_, err = s.DB.Exec(`INSERT INTO dashboards(id,name,panels,updated_at,extras) VALUES('old','Old','[]',233,?)`, raw)
	require.NoError(t, err)
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	ctx := context.Background()
	list, err := s.DashboardVersions(ctx, "old", 1000, 0, "")
	require.NoError(t, err)
	require.Len(t, list.Versions, 1)
	v, err := s.DashboardVersion(ctx, "old", 17)
	require.NoError(t, err)
	assert.Zero(t, v.ParentVersion)
	assert.Equal(t, int64(233), v.Created.UnixMilli())
	assert.Equal(t, "Current version imported during upgrade", v.Message)
	assert.Contains(t, string(v.Snapshot.Grafana), "900719925474099312345")
	assert.Contains(t, string(v.Snapshot.Annotations), "18446744073709551615")
	var stored string
	require.NoError(t, s.DB.QueryRow(`SELECT extras FROM dashboards WHERE id='old'`).Scan(&stored))
	assert.Equal(t, raw, stored, "migration must leave raw current source byte-for-byte unchanged")
	_, err = s.DashboardVersion(ctx, "old", 16)
	require.ErrorIs(t, err, sql.ErrNoRows, "migration must not invent earlier history")
	require.NoError(t, s.DeleteDashboard(ctx, "old"))
	var count int
	require.NoError(t, s.DB.QueryRow(`SELECT COUNT(*) FROM dashboard_versions WHERE dashboard_id='old'`).Scan(&count))
	assert.Zero(t, count, "history must cascade with dashboard deletion")
}

func TestDashboardHistoryFailedSavesAndConcurrentCASAreAtomic(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "atomic.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	ctx := context.Background()
	d, err := s.SaveDashboard(ctx, model.Dashboard{ID: "atomic", Name: "Initial", Panels: []model.Panel{}})
	require.NoError(t, err)
	badCtx := store.WithDashboardHistoryMetadata(ctx, model.DashboardHistoryMetadata{RestoredFrom: 999})
	_, err = s.SaveDashboardAtRevision(badCtx, d, d.UpdatedAt)
	require.ErrorIs(t, err, sql.ErrNoRows)
	current, err := s.Dashboard(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, d, current, "history failure must roll back the already-executed dashboard write")
	badCtx = store.WithDashboardHistoryMetadata(ctx, model.DashboardHistoryMetadata{Message: strings.Repeat("字", 501)})
	_, err = s.SaveDashboard(badCtx, d)
	require.ErrorContains(t, err, "history metadata")
	library := model.LibraryElement{UID: "rolled-back", Name: "Rollback", Kind: 1, Model: json.RawMessage(`{"type":"timeseries"}`)}
	stale := d.UpdatedAt - 1
	_, err = s.SaveDashboardWithLibraryElements(ctx, d, []model.LibraryElement{library}, &stale)
	require.ErrorIs(t, err, store.ErrDashboardConflict)
	_, err = s.LibraryElement(ctx, library.UID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	const writers = 8
	results := make(chan error, writers)
	var group sync.WaitGroup
	for i := range writers {
		group.Go(func() {
			draft := d
			draft.Name = fmt.Sprintf("Writer %d", i)
			_, err := s.SaveDashboardAtRevision(ctx, draft, d.UpdatedAt)
			results <- err
		})
	}
	group.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else {
			assert.ErrorIs(t, err, store.ErrDashboardConflict)
		}
	}
	assert.Equal(t, 1, winners)
	list, err := s.DashboardVersions(ctx, d.ID, 1000, 0, "")
	require.NoError(t, err)
	assert.Equal(t, []int64{2, 1}, historyVersionNumbers(list), "only the successful transaction creates history")
}
