package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/stretchr/testify/require"
)

func TestDashboardResourcesAtomicPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "resources.db")
	s, err := Open(path)
	require.NoError(t, err)
	defer func() { s.DB.Close() }()
	parent, err := s.CreateFolder(ctx, model.Folder{UID: "team", Title: "Team"})
	require.NoError(t, err)
	child, err := s.CreateFolder(ctx, model.Folder{UID: "database", Title: "Databases", ParentUID: parent.UID})
	require.NoError(t, err)
	_, err = s.MoveFolder(ctx, parent.UID, child.UID)
	require.ErrorContains(t, err, "descendants")
	_, err = s.CreateFolder(ctx, model.Folder{UID: "duplicate", Title: "databases", ParentUID: parent.UID})
	require.ErrorIs(t, err, ErrResourceExists)
	parent.Title = "Engineering"
	parent, err = s.UpdateFolder(ctx, parent, false)
	require.NoError(t, err)
	require.Equal(t, int64(2), parent.Version)
	parent.Version = 1
	_, err = s.UpdateFolder(ctx, parent, false)
	require.ErrorIs(t, err, ErrResourceConflict)
	library := model.LibraryElement{UID: "load", Name: "Load", Kind: 1, FolderUID: child.UID, Model: []byte(`{"type":"stat","title":"Load","opaque":900719925474099312345,"targets":[{"refId":"A","expr":"up"}]}`)}
	d := model.Dashboard{ID: "db", Name: "Database dashboard", FolderUID: child.UID, Panels: []model.Panel{{ID: "one", Title: "Load", Aggregation: "last", Config: []byte(`{"id":1,"type":"stat","libraryPanel":{"uid":"load"}}`)}}}
	stale := int64(12)
	_, err = s.SaveDashboardWithLibraryElements(ctx, d, []model.LibraryElement{library}, &stale)
	require.ErrorIs(t, err, ErrDashboardConflict)
	_, err = s.LibraryElement(ctx, "load")
	require.ErrorIs(t, err, sql.ErrNoRows)
	d, err = s.SaveDashboardWithLibraryElements(ctx, d, []model.LibraryElement{library}, nil)
	require.NoError(t, err)
	require.Positive(t, d.GrafanaID)
	loaded, err := s.DashboardByGrafanaID(ctx, d.GrafanaID)
	require.NoError(t, err)
	require.Equal(t, child.UID, loaded.FolderUID)
	e, err := s.LibraryElement(ctx, "load")
	require.NoError(t, err)
	require.Equal(t, int64(1), e.Meta.ConnectedDashboards)
	require.Contains(t, string(e.Model), "900719925474099312345")
	require.ErrorIs(t, s.DeleteLibraryElement(ctx, e.UID), ErrLibraryConnected)
	require.ErrorIs(t, s.DeleteFolder(ctx, parent.UID, false), ErrLibraryConnected)
	e.Model = []byte(`{"type":"stat","title":"Updated","opaque":900719925474099312345}`)
	e, err = s.SaveLibraryElement(ctx, e, false)
	require.NoError(t, err)
	require.Equal(t, int64(2), e.Version)
	e.Version = 1
	_, err = s.SaveLibraryElement(ctx, e, false)
	require.ErrorIs(t, err, ErrResourceConflict)
	require.NoError(t, s.DB.Close())
	s, err = Open(path)
	require.NoError(t, err)
	loaded, err = s.Dashboard(ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, d.GrafanaID, loaded.GrafanaID)
	e, err = s.LibraryElement(ctx, "load")
	require.NoError(t, err)
	require.Equal(t, int64(2), e.Version)
	require.Equal(t, int64(1), e.Meta.ConnectedDashboards)
	require.NoError(t, s.DeleteDashboard(ctx, d.ID))
	e, err = s.LibraryElement(ctx, "load")
	require.NoError(t, err)
	require.Zero(t, e.Meta.ConnectedDashboards)
	require.NoError(t, s.DeleteFolder(ctx, parent.UID, false))
	_, err = s.Folder(ctx, child.UID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = s.LibraryElement(ctx, "load")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestFolderDeletionProtectsRulesAndSystem(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "control.db"))
	require.NoError(t, err)
	defer s.DB.Close()
	f, err := s.CreateFolder(ctx, model.Folder{UID: "operations", Title: "Operations"})
	require.NoError(t, err)
	_, err = s.DB.Exec(`INSERT INTO alert_rules(uid,version,config,runtime) VALUES('rule',1,?, '{}')`, `{"folder_uid":"operations"}`)
	require.NoError(t, err)
	require.ErrorIs(t, s.DeleteFolder(ctx, f.UID, false), ErrFolderAlertRules)
	d, err := s.Dashboard(ctx, "system")
	require.NoError(t, err)
	d.FolderUID = f.UID
	d, err = s.SaveDashboard(ctx, d)
	require.NoError(t, err)
	require.ErrorContains(t, s.DeleteFolder(ctx, f.UID, true), "system dashboard")
	d.FolderUID = "general"
	_, err = s.SaveDashboard(ctx, d)
	require.NoError(t, err)
	require.NoError(t, s.DeleteFolder(ctx, f.UID, true))
	var count int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM alert_rules WHERE uid='rule'`).Scan(&count))
	require.Zero(t, count)
	_, err = s.Dashboard(ctx, "system")
	require.NoError(t, err)
}

func TestDashboardIdentityMigrationDoesNotRewriteJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	require.NoError(t, err)
	raw := `{"grafana":{"uid":"old","title":"Old","opaque":900719925474099312345}}`
	_, err = s.DB.Exec(`INSERT INTO dashboards(id,name,panels,updated_at,extras) VALUES('old','Old','[]',233,?)`, raw)
	require.NoError(t, err)
	require.NoError(t, s.DB.Close())
	s, err = Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	d, err := s.Dashboard(context.Background(), "old")
	require.NoError(t, err)
	require.Positive(t, d.GrafanaID)
	require.Equal(t, int64(233), d.UpdatedAt)
	var extras string
	require.NoError(t, s.DB.QueryRow(`SELECT extras FROM dashboards WHERE id='old'`).Scan(&extras))
	require.Equal(t, raw, extras)
	d.GrafanaID = 999999
	d, err = s.SaveDashboard(context.Background(), d)
	require.NoError(t, err)
	require.NotEqual(t, int64(999999), d.GrafanaID)
	_, err = s.DashboardByGrafanaID(context.Background(), 999999)
	require.True(t, errors.Is(err, sql.ErrNoRows))
}
