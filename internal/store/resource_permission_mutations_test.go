package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/instance"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mutationContext(s *store.Store) context.Context {
	return identity.WithPrincipal(context.Background(), permissionActor(s.OrganizationID(), 7, identity.TypeUser, identity.RoleNone))
}

func mutationFence(t *testing.T, s *store.Store, ctx context.Context, refs []model.PermissionResource, libs ...string) store.PermissionMutationFence {
	t.Helper()
	f, err := s.CapturePermissionMutation(ctx, refs, permissionFixtureResolver(), libs...)
	require.NoError(t, err)
	return f
}

func mutationFolder(t *testing.T, s *store.Store, ctx context.Context, uid, parent string) model.Folder {
	t.Helper()
	refs := []model.PermissionResource{}
	if parent != "" {
		refs = append(refs, permissionRef(s, model.PermissionFolder, parent))
	}
	f := mutationFence(t, s, ctx, refs)
	folder, err := s.CreateFolderWithPermissions(ctx, model.Folder{UID: uid, Title: uid, ParentUID: parent}, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, f)
	require.NoError(t, err)
	return folder
}

func mutationDashboard(t *testing.T, s *store.Store, ctx context.Context, id, folder string) model.Dashboard {
	t.Helper()
	refs := []model.PermissionResource{}
	if folder != "" {
		refs = append(refs, permissionRef(s, model.PermissionFolder, folder))
	}
	d, err := s.SaveDashboardWithPermissions(ctx, model.Dashboard{ID: id, Name: id, FolderUID: folder}, nil, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, mutationFence(t, s, ctx, refs))
	require.NoError(t, err)
	return d
}

func TestPermissionMutationsAtomicDefaultsRawAndStoreBinding(t *testing.T) {
	s := permissionStore(t, 2)
	ctx := mutationContext(s)
	root := mutationFolder(t, s, ctx, "root", "")
	nested := mutationFolder(t, s, ctx, "nested", root.UID)
	rootDoc, err := s.ResourcePermissions(ctx, permissionRef(s, model.PermissionFolder, root.UID))
	require.NoError(t, err)
	require.Len(t, rootDoc.Items, 3)
	nestedDoc, err := s.ResourcePermissions(ctx, permissionRef(s, model.PermissionFolder, nested.UID))
	require.NoError(t, err)
	require.Empty(t, nestedDoc.Items)
	refs := []model.PermissionResource{permissionRef(s, model.PermissionFolder, nested.UID)}
	f := mutationFence(t, s, ctx, refs)
	clone, err := f.Effective(refs[0])
	require.NoError(t, err)
	require.Equal(t, model.PermissionAdmin, clone.Permission)
	for key := range clone.Versions {
		clone.Versions[key] = 999
	}
	clone.Items[0].Permission = model.PermissionView
	original, err := f.Effective(refs[0])
	require.NoError(t, err)
	require.NotEqual(t, clone, original)
	raw := []byte(`{"uid":"external-id","version":1,"opaque":900719925474099312345,"panels":[]}`)
	d, err := s.SaveDashboardWithPermissions(ctx, model.Dashboard{ID: "native-id", Name: "Raw", FolderUID: nested.UID, Grafana: raw}, nil, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, f)
	require.NoError(t, err)
	doc, err := s.ResourcePermissions(ctx, permissionRef(s, model.PermissionDashboard, d.ID))
	require.NoError(t, err)
	require.Empty(t, doc.Items)
	_, err = s.ResourcePermissions(ctx, permissionRef(s, model.PermissionDashboard, "external-id"))
	require.Error(t, err)
	loaded, err := s.Dashboard(ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, string(raw), string(loaded.Grafana))
	legacy := mutationFolder(t, s, ctx, "legacy-parent", root.UID)
	f = mutationFence(t, s, ctx, []model.PermissionResource{permissionRef(s, model.PermissionFolder, legacy.UID)})
	created, err := s.CreateFolderWithPermissions(ctx, model.Folder{UID: "legacy-nested", Title: "Legacy", ParentUID: legacy.UID}, model.ResourcePermissionSeed{Policy: model.PermissionLegacyDefaults}, f)
	require.NoError(t, err)
	legacyDoc, err := s.ResourcePermissions(ctx, permissionRef(s, model.PermissionFolder, created.UID))
	require.NoError(t, err)
	require.Len(t, legacyDoc.Items, 1)
	require.Equal(t, model.PermissionUser, legacyDoc.Items[0].Subject.Kind)
	foreign := permissionStore(t, 1)
	foreignCtx := mutationContext(foreign)
	foreignD := mutationDashboard(t, foreign, foreignCtx, d.ID, "")
	require.Equal(t, d.ID, foreignD.ID)
	_, err = foreign.CreateFolderWithPermissions(mutationContext(foreign), model.Folder{UID: "foreign", Title: "Foreign"}, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, f)
	require.ErrorIs(t, err, store.ErrPermissionFence)
	foreignLoaded, err := foreign.Dashboard(foreignCtx, d.ID)
	require.NoError(t, err)
	require.Empty(t, foreignLoaded.Grafana, "identical native paths/UIDs use independent organization data")
	_, err = s.CreateFolderWithPermissions(ctx, model.Folder{UID: "zero", Title: "Zero"}, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, store.PermissionMutationFence{})
	require.ErrorIs(t, err, store.ErrPermissionFence)
	wrong := identity.WithPrincipal(context.Background(), permissionActor(2, 8, identity.TypeUser, identity.RoleNone))
	_, err = s.CreateFolderWithPermissions(wrong, model.Folder{UID: "fake", Title: "Fake"}, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, f)
	require.ErrorIs(t, err, store.ErrPermissionFence)
	path := s.Path
	require.NoError(t, s.DB.Close())
	reopened, err := store.OpenForOrganization(path, 2)
	require.NoError(t, err)
	defer reopened.DB.Close()
	loaded, err = reopened.Dashboard(ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, string(raw), string(loaded.Grafana))
	_, err = reopened.CreateFolderWithPermissions(ctx, model.Folder{UID: "stale-store", Title: "Stale"}, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, f)
	require.ErrorIs(t, err, store.ErrPermissionFence)
}

func TestPermissionMutationsSeedFailureRollsBackLibraryHistoryAndContent(t *testing.T) {
	s := permissionStore(t, 1)
	ctx := mutationContext(s)
	anchor := mutationDashboard(t, s, ctx, "anchor", "")
	ref := permissionRef(s, model.PermissionDashboard, anchor.ID)
	var callbacks atomic.Int64
	resolver := permissionFixtureResolver()
	resolver.CurrentPrincipal = func(ctx context.Context, p identity.Principal) (identity.Principal, error) {
		callbacks.Add(1)
		_, err := s.ResourcePermissions(ctx, ref)
		return p, err
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	f, err := s.CapturePermissionMutation(bounded, []model.PermissionResource{ref}, resolver, "new-library")
	require.NoError(t, err)
	_, err = s.DB.Exec(`CREATE TRIGGER reject_creator BEFORE INSERT ON resource_acl_grants WHEN NEW.resource_uid IN ('failed-dashboard','failed-folder') AND NEW.subject_kind='user' BEGIN SELECT RAISE(ABORT,'fixture rejects seed'); END`)
	require.NoError(t, err)
	raw := []byte(`{"uid":"failed-dashboard","opaque":900719925474099312345,"panels":[{"id":1,"type":"text","libraryPanel":{"uid":"new-library"}}]}`)
	d := model.Dashboard{ID: "failed-dashboard", Name: "Failed", Grafana: raw}
	e := model.LibraryElement{UID: "new-library", Name: "Library", Kind: 1, Model: []byte(`{"type":"text","options":{"content":"fixture"}}`)}
	_, err = s.SaveDashboardWithLibraryElementsAndPermissions(bounded, d, []model.LibraryElement{e}, nil, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, f)
	require.ErrorContains(t, err, "fixture rejects seed")
	_, err = s.CreateFolderWithPermissions(bounded, model.Folder{UID: "failed-folder", Title: "Failed"}, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, f)
	require.ErrorContains(t, err, "fixture rejects seed")
	for _, query := range []string{
		`SELECT count(*) FROM dashboards WHERE id='failed-dashboard'`, `SELECT count(*) FROM dashboard_identity WHERE dashboard_id='failed-dashboard'`,
		`SELECT count(*) FROM dashboard_versions WHERE dashboard_id='failed-dashboard'`, `SELECT count(*) FROM library_elements WHERE uid='new-library'`,
		`SELECT count(*) FROM library_panel_connections WHERE dashboard_id='failed-dashboard'`, `SELECT count(*) FROM folders WHERE uid='failed-folder'`,
		`SELECT count(*) FROM resource_acl_revision_ledger WHERE resource_uid IN ('failed-dashboard','failed-folder')`,
	} {
		var count int
		require.NoError(t, s.DB.QueryRow(query).Scan(&count))
		require.Zero(t, count, query)
	}
	require.Greater(t, callbacks.Load(), int64(3), "real callback control reads would deadlock under the one-connection writer")
	_, err = s.DB.Exec(`DROP TRIGGER reject_creator`)
	require.NoError(t, err)
	d, err = s.SaveDashboardWithLibraryElementsAndPermissions(bounded, d, []model.LibraryElement{e}, nil, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, f)
	require.NoError(t, err)
	_, err = s.DashboardVersion(ctx, d.ID, d.GrafanaVersion)
	require.NoError(t, err)
	connections, err := s.LibraryConnections(ctx, e.UID)
	require.NoError(t, err)
	require.Len(t, connections, 1)
	f = mutationFence(t, s, ctx, []model.PermissionResource{ref})
	_, err = s.SaveDashboardWithPermissions(ctx, model.Dashboard{ID: "unguarded-library", Name: "Unguarded", Grafana: raw}, nil, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, f)
	require.ErrorIs(t, err, store.ErrPermissionFence)
}

func TestPermissionMutationsCASDestinationAndDeletedIncarnation(t *testing.T) {
	s := permissionStore(t, 2)
	ctx := mutationContext(s)
	a := mutationFolder(t, s, ctx, "a", "")
	b := mutationFolder(t, s, ctx, "b", "")
	d := mutationDashboard(t, s, ctx, "edit", a.UID)
	ref := permissionRef(s, model.PermissionDashboard, d.ID)
	dest := permissionRef(s, model.PermissionFolder, b.UID)
	f := mutationFence(t, s, ctx, []model.PermissionResource{ref, dest})
	doc, err := s.ResourcePermissions(ctx, ref)
	require.NoError(t, err)
	_, err = s.ReplaceResourcePermissions(ctx, ref, doc.Version, nil, permissionFixtureResolver())
	require.NoError(t, err)
	changed := d
	changed.Name = "Denied"
	changed.FolderUID = b.UID
	_, err = s.SaveDashboardWithPermissions(ctx, changed, &d.UpdatedAt, model.ResourcePermissionSeed{}, f)
	require.ErrorIs(t, err, store.ErrPermissionFence)
	loaded, err := s.Dashboard(ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, d, loaded)
	f = mutationFence(t, s, ctx, []model.PermissionResource{ref, dest})
	b.Title = "New destination title"
	b, err = s.UpdateFolder(ctx, b, false)
	require.NoError(t, err)
	_, err = s.SaveDashboardWithPermissions(ctx, changed, &d.UpdatedAt, model.ResourcePermissionSeed{}, f)
	require.ErrorIs(t, err, store.ErrPermissionFence)
	f = mutationFence(t, s, ctx, []model.PermissionResource{ref, dest})
	moved, err := s.SaveDashboardWithPermissions(ctx, changed, &d.UpdatedAt, model.ResourcePermissionSeed{}, f)
	require.NoError(t, err)
	require.Equal(t, b.UID, moved.FolderUID)
	preserved, err := s.ResourcePermissions(ctx, ref)
	require.NoError(t, err)
	require.Empty(t, preserved.Items)
	f = mutationFence(t, s, ctx, []model.PermissionResource{ref, dest})
	_, err = s.SaveDashboardWithPermissions(ctx, moved, nil, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, f)
	require.ErrorIs(t, err, store.ErrResourceExists, "nil revision is create-only")
	oldTime := moved.UpdatedAt
	require.NoError(t, s.DeleteDashboard(ctx, moved.ID))
	fresh := mutationDashboard(t, s, ctx, moved.ID, b.UID)
	_, err = s.DB.Exec(`UPDATE dashboards SET updated_at=? WHERE id=?`, oldTime, fresh.ID)
	require.NoError(t, err)
	_, err = s.SaveDashboardWithPermissions(ctx, moved, &oldTime, model.ResourcePermissionSeed{}, f)
	require.ErrorIs(t, err, store.ErrPermissionFence, "ACL ledger detects reincarnation even when content revision is forced equal")
}

func TestPermissionMutationsSubtreeCoverageLibraryOwnershipAndScheduler(t *testing.T) {
	s := permissionStore(t, 2)
	ctx := mutationContext(s)
	source := mutationFolder(t, s, ctx, "source", "")
	child := mutationFolder(t, s, ctx, "child", source.UID)
	destination := mutationFolder(t, s, ctx, "destination", "")
	d := mutationDashboard(t, s, ctx, "child-dashboard", child.UID)
	refs := []model.PermissionResource{permissionRef(s, model.PermissionFolder, destination.UID)}
	f, err := s.CapturePermissionSubtreeMutation(ctx, source.UID, refs, permissionFixtureResolver())
	require.NoError(t, err)
	late, err := s.SaveDashboard(ctx, model.Dashboard{ID: "late", Name: "Late", FolderUID: child.UID})
	require.NoError(t, err)
	_, err = s.MoveFolderWithPermissions(ctx, source.UID, destination.UID, source.Version, f)
	require.ErrorIs(t, err, store.ErrPermissionFence, "new descendants cannot be erased or moved without capture")
	seedPermission(t, s, permissionRef(s, model.PermissionDashboard, late.ID), model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults})
	f, err = s.CapturePermissionSubtreeMutation(ctx, source.UID, refs, permissionFixtureResolver())
	require.NoError(t, err)
	source, err = s.MoveFolderWithPermissions(ctx, source.UID, destination.UID, source.Version, f)
	require.NoError(t, err)
	f, err = s.CapturePermissionSubtreeMutation(ctx, source.UID, nil, permissionFixtureResolver())
	require.NoError(t, err)
	e, err := s.SaveLibraryElement(ctx, model.LibraryElement{UID: "late-library", Name: "Late library", FolderUID: child.UID, Kind: 1, Model: []byte(`{"type":"text"}`)}, true)
	require.NoError(t, err)
	require.ErrorIs(t, s.DeleteFolderWithPermissions(ctx, source.UID, source.Version, false, f, nil), store.ErrPermissionFence, "new owned libraries require capture even if no dashboard links them")
	f, err = s.CapturePermissionSubtreeMutation(ctx, source.UID, nil, permissionFixtureResolver())
	require.NoError(t, err)
	e.FolderUID = destination.UID
	e, err = s.SaveLibraryElement(ctx, e, false)
	require.NoError(t, err)
	require.ErrorIs(t, s.DeleteFolderWithPermissions(ctx, source.UID, source.Version, false, f, nil), store.ErrPermissionFence, "captured library owner/version changes invalidate the operation")
	f, err = s.CapturePermissionSubtreeMutation(ctx, source.UID, nil, permissionFixtureResolver())
	require.NoError(t, err)
	_, err = s.DB.Exec(`CREATE TRIGGER fail_folder_delete BEFORE DELETE ON folders WHEN OLD.uid='source' BEGIN SELECT RAISE(ABORT,'fixture rejects delete'); END`)
	require.NoError(t, err)
	var releases int
	expectRows := 1
	gate := func([]string) (func(), error) {
		return func() {
			bounded, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var count int
			assert.NoError(t, s.DB.QueryRowContext(bounded, `SELECT count(*) FROM folders WHERE uid='source'`).Scan(&count))
			assert.Equal(t, expectRows, count, "scheduler resumes only after commit/rollback")
			releases++
		}, nil
	}
	require.ErrorContains(t, s.DeleteFolderWithPermissions(ctx, source.UID, source.Version, false, f, gate), "fixture rejects delete")
	require.Equal(t, 1, releases)
	_, err = s.Dashboard(ctx, d.ID)
	require.NoError(t, err)
	_, err = s.ResourcePermissions(ctx, permissionRef(s, model.PermissionDashboard, d.ID))
	require.NoError(t, err)
	_, err = s.DB.Exec(`DROP TRIGGER fail_folder_delete`)
	require.NoError(t, err)
	expectRows = 0
	require.NoError(t, s.DeleteFolderWithPermissions(ctx, source.UID, source.Version, false, f, gate))
	require.Equal(t, 2, releases)
	for _, id := range []string{d.ID, late.ID} {
		requirePermissionRowsAbsent(t, s, permissionRef(s, model.PermissionDashboard, id))
	}
	_, err = s.LibraryElement(ctx, e.UID)
	require.NoError(t, err, "a library moved outside the subtree survives")
}

func TestPermissionMutationsMigrationResumeReadinessAndExistingACL(t *testing.T) {
	s := permissionStore(t, 2)
	ctx := identity.WithPrincipal(context.Background(), identity.BackgroundPrincipal(2))
	resolver := permissionFixtureResolver()
	_, err := s.CreateFolder(ctx, model.Folder{UID: "legacy-root", Title: "Legacy root"})
	require.NoError(t, err)
	_, err = s.CreateFolder(ctx, model.Folder{UID: "legacy-nested", Title: "Nested", ParentUID: "legacy-root"})
	require.NoError(t, err)
	raw := []byte(`{"uid":"different-import-uid","version":1,"opaque":900719925474099312345,"templating":{"unknown":900719925474099312346}}`)
	_, err = s.SaveDashboard(ctx, model.Dashboard{ID: "nested-d", Name: "Nested", FolderUID: "legacy-nested", Grafana: raw})
	require.NoError(t, err)
	_, err = s.SaveDashboard(ctx, model.Dashboard{ID: "edited", Name: "Edited"})
	require.NoError(t, err)
	ref := permissionRef(s, model.PermissionDashboard, "edited")
	edited := seedPermission(t, s, ref, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults})
	edited, err = s.ReplaceResourcePermissions(ctx, ref, edited.Version, nil, resolver)
	require.NoError(t, err)
	status, err := s.ResourcePermissionReadiness(ctx)
	require.NoError(t, err)
	require.False(t, status.Ready)
	status, err = s.MigrateResourcePermissions(ctx, 1, resolver)
	require.NoError(t, err)
	require.False(t, status.Ready)
	require.Equal(t, "running", status.Phase)
	path := s.Path
	require.NoError(t, s.DB.Close())
	s, err = store.OpenForOrganization(path, 2)
	require.NoError(t, err)
	defer s.DB.Close()
	restored, err := s.ResourcePermissionReadiness(ctx)
	require.NoError(t, err)
	require.Equal(t, status.CursorUID, restored.CursorUID)
	_, err = s.SaveDashboard(ctx, model.Dashboard{ID: "aaa-before-cursor", Name: "Added during migration"})
	require.NoError(t, err)
	for attempts := 0; attempts < 20 && !status.Ready; attempts++ {
		status, err = s.MigrateResourcePermissions(ctx, 1, resolver)
		require.NoError(t, err)
	}
	require.True(t, status.Ready)
	require.Zero(t, status.Remaining)
	actual, err := s.ResourcePermissions(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, edited, actual, "migration never resets edited existing defaults")
	nested, err := s.ResourcePermissions(ctx, permissionRef(s, model.PermissionFolder, "legacy-nested"))
	require.NoError(t, err)
	require.Empty(t, nested.Items)
	root, err := s.ResourcePermissions(ctx, permissionRef(s, model.PermissionFolder, "legacy-root"))
	require.NoError(t, err)
	require.Len(t, root.Items, 2, "unknown historical creator never receives an invented grant")
	d, err := s.Dashboard(ctx, "nested-d")
	require.NoError(t, err)
	require.Equal(t, string(raw), string(d.Grafana))
	_, err = s.SaveDashboard(ctx, model.Dashboard{ID: "later-legacy", Name: "Later legacy"})
	require.NoError(t, err)
	status, err = s.ResourcePermissionReadiness(ctx)
	require.NoError(t, err)
	require.False(t, status.Ready)
	require.Equal(t, "pending", status.Phase)
	status, err = s.MigrateResourcePermissions(ctx, 1, resolver)
	require.NoError(t, err)
	require.True(t, status.Ready)
	mutationFolder(t, s, mutationContext(s), "atomic-after-ready", "")
	status, err = s.ResourcePermissionReadiness(ctx)
	require.NoError(t, err)
	require.True(t, status.Ready, "joint content/default creation does not invalidate an already complete migration")
	_, err = s.DB.Exec(`UPDATE folders SET parent_uid='legacy-nested' WHERE uid='legacy-root'`)
	require.NoError(t, err)
	status, err = s.ResourcePermissionReadiness(ctx)
	require.ErrorIs(t, err, store.ErrPermissionAncestry)
	require.False(t, status.Ready)
}

func TestPermissionMutationsCreatorProvisioningAndServiceTokenRevocation(t *testing.T) {
	s := permissionStore(t, 1)
	ctx := mutationContext(s)
	f := mutationFence(t, s, ctx, nil)
	other := permissionSubject(model.PermissionUser, 8)
	_, err := s.CreateFolderWithPermissions(ctx, model.Folder{UID: "wrong-creator", Title: "Wrong creator"}, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults, Creator: &other}, f)
	require.ErrorIs(t, err, identity.ErrForbidden)
	provisioned, err := s.CreateFolderWithPermissions(ctx, model.Folder{UID: "provisioned", Title: "Provisioned"}, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults, Provisioned: true}, f)
	require.NoError(t, err)
	doc, err := s.ResourcePermissions(ctx, permissionRef(s, model.PermissionFolder, provisioned.UID))
	require.NoError(t, err)
	require.Len(t, doc.Items, 2, "provisioned resources omit the creator even with an authenticated human")
	_, err = s.MigrateResourcePermissions(ctx, 100, permissionFixtureResolver())
	require.ErrorIs(t, err, identity.ErrForbidden, "None plus one resource Admin grant cannot migrate the organization")
	catalog, err := identity.Open(filepath.Join(t.TempDir(), "service-catalog.db"))
	require.NoError(t, err)
	defer catalog.Close()
	account, err := catalog.CreateServiceAccount(context.Background(), 1, identity.ServiceAccountInput{Name: "agent", Role: identity.RoleNone})
	require.NoError(t, err)
	token, key, err := catalog.CreateServiceAccountToken(context.Background(), 1, account.ID, "fixture", 0)
	require.NoError(t, err)
	p, err := catalog.AuthenticateServiceToken(context.Background(), key, 1)
	require.NoError(t, err)
	resolver, err := instance.NewPermissionIdentityResolver(catalog, instance.PermissionResolverOptions{Revalidate: func(ctx context.Context, incoming identity.Principal) (identity.Principal, error) {
		return catalog.AuthenticateServiceToken(ctx, key, incoming.OrgID)
	}})
	require.NoError(t, err)
	ctx = identity.WithPrincipal(context.Background(), p)
	f, err = s.CapturePermissionMutation(ctx, nil, resolver)
	require.NoError(t, err)
	d, err := s.SaveDashboardWithPermissions(ctx, model.Dashboard{ID: "sa-created", Name: "SA created"}, nil, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, f)
	require.NoError(t, err)
	ref := permissionRef(s, model.PermissionDashboard, d.ID)
	doc, err = s.ResourcePermissions(ctx, ref)
	require.NoError(t, err)
	var creator model.ResourcePermissionGrant
	for _, item := range doc.Items {
		if item.Subject.Kind != model.PermissionRole {
			creator = item
		}
	}
	require.Equal(t, model.PermissionServiceAccount, creator.Subject.Kind)
	require.Equal(t, account.ID, creator.Subject.ID)
	f, err = s.CapturePermissionMutation(ctx, []model.PermissionResource{ref}, resolver)
	require.NoError(t, err)
	require.NoError(t, catalog.RevokeServiceAccountToken(ctx, 1, account.ID, token.ID))
	require.ErrorIs(t, s.DeleteDashboardWithPermissions(ctx, d.ID, d.UpdatedAt, f), identity.ErrUnauthenticated)
	_, err = s.Dashboard(context.Background(), d.ID)
	require.NoError(t, err)
}

func TestPermissionMutationsMigrationIntegrityFailureRollsBackBatch(t *testing.T) {
	s := permissionStore(t, 2)
	ctx := identity.WithPrincipal(context.Background(), identity.BackgroundPrincipal(2))
	_, err := s.CreateFolder(ctx, model.Folder{UID: "cycle-a", Title: "A"})
	require.NoError(t, err)
	_, err = s.CreateFolder(ctx, model.Folder{UID: "cycle-b", Title: "B", ParentUID: "cycle-a"})
	require.NoError(t, err)
	_, err = s.DB.Exec(`UPDATE folders SET parent_uid='cycle-b' WHERE uid='cycle-a'`)
	require.NoError(t, err)
	status, err := s.MigrateResourcePermissions(ctx, 1000, permissionFixtureResolver())
	require.ErrorIs(t, err, store.ErrPermissionAncestry)
	require.False(t, status.Ready)
	for _, table := range []string{"resource_acl_resources", "resource_acl_revision_ledger", "resource_acl_migration"} {
		var count int
		require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM `+table).Scan(&count))
		require.Zero(t, count, "the failed final integrity check rolls back defaults, counters and readiness together")
	}
	_, err = s.DB.Exec(`UPDATE folders SET parent_uid='' WHERE uid='cycle-a'`)
	require.NoError(t, err)
	status, err = s.MigrateResourcePermissions(ctx, 1000, permissionFixtureResolver())
	require.NoError(t, err)
	require.True(t, status.Ready)
	_, err = s.DB.Exec(`UPDATE resource_acl_resources SET defaults_initialized=0 WHERE resource_kind='folder' AND resource_uid='cycle-a'`)
	require.NoError(t, err)
	status, err = s.MigrateResourcePermissions(ctx, 1000, permissionFixtureResolver())
	require.ErrorIs(t, err, store.ErrPermissionUninitialized, "an ambiguous partial registration is not silently reset or retried forever")
	require.False(t, status.Ready)
}

func TestPermissionMutationsConnectionSetAndMetadataParentGuards(t *testing.T) {
	s := permissionStore(t, 2)
	ctx := mutationContext(s)
	root := mutationFolder(t, s, ctx, "root", "")
	child := mutationFolder(t, s, ctx, "child", root.UID)
	d := mutationDashboard(t, s, ctx, "linked", child.UID)
	ref := permissionRef(s, model.PermissionDashboard, d.ID)
	f := mutationFence(t, s, ctx, []model.PermissionResource{ref})
	lib, err := s.SaveLibraryElement(ctx, model.LibraryElement{UID: "late-link", Name: "Late link", Kind: 1, Model: []byte(`{"type":"text"}`)}, true)
	require.NoError(t, err)
	_, err = s.DB.Exec(`INSERT INTO library_panel_connections(library_uid,dashboard_id,created) VALUES(?,?,?)`, lib.UID, d.ID, time.Now().UnixMilli())
	require.NoError(t, err)
	require.ErrorIs(t, s.DeleteDashboardWithPermissions(ctx, d.ID, d.UpdatedAt, f), store.ErrPermissionFence, "a late connection cannot rely on unchanged dashboard revision")
	f = mutationFence(t, s, ctx, []model.PermissionResource{ref})
	require.ErrorIs(t, s.DeleteDashboardWithPermissions(ctx, d.ID, d.UpdatedAt+1, f), store.ErrDashboardConflict)
	require.NoError(t, s.DeleteDashboardWithPermissions(ctx, d.ID, d.UpdatedAt, f))
	requirePermissionRowsAbsent(t, s, ref)
	_, err = s.LibraryElement(ctx, lib.UID)
	require.NoError(t, err, "dashboard deletion unlinks rather than deleting shared library content")
	f = mutationFence(t, s, ctx, []model.PermissionResource{permissionRef(s, model.PermissionFolder, child.UID)})
	wrong := child
	wrong.ParentUID = ""
	wrong.Title = "Wrong implicit move"
	_, err = s.UpdateFolderWithPermissions(ctx, wrong, f)
	require.ErrorIs(t, err, store.ErrPermissionFence)
	child.Title = "Metadata rename"
	child, err = s.UpdateFolderWithPermissions(ctx, child, f)
	require.NoError(t, err)
	require.Equal(t, root.UID, child.ParentUID)
	_, err = s.UpdateFolderWithPermissions(ctx, child, f)
	require.ErrorIs(t, err, store.ErrPermissionFence, "metadata changes consume the captured content revision")
}

func TestPermissionMutationsMoveCannotExceedDescendantDepth(t *testing.T) {
	s := permissionStore(t, 2)
	ctx := mutationContext(s)
	parent := ""
	for depth := 1; depth <= 63; depth++ {
		folder := mutationFolder(t, s, ctx, fmt.Sprintf("deep-%02d", depth), parent)
		parent = folder.UID
	}
	source := mutationFolder(t, s, ctx, "source", "")
	child := mutationFolder(t, s, ctx, "child", source.UID)
	f, err := s.CapturePermissionSubtreeMutation(ctx, source.UID, []model.PermissionResource{permissionRef(s, model.PermissionFolder, parent)}, permissionFixtureResolver())
	require.NoError(t, err)
	_, err = s.MoveFolderWithPermissions(ctx, source.UID, parent, source.Version, f)
	require.ErrorIs(t, err, store.ErrPermissionAncestry)
	actual, err := s.Folder(ctx, source.UID)
	require.NoError(t, err)
	require.Equal(t, source, actual, "prospective depth rejection rolls back source version and parent")
	actual, err = s.Folder(ctx, child.UID)
	require.NoError(t, err)
	require.Equal(t, child, actual)
}

func TestPermissionMutationsRealCatalogRoleGenerationAndCredentialRevocation(t *testing.T) {
	catalog, err := identity.Open(filepath.Join(t.TempDir(), "catalog.db"))
	require.NoError(t, err)
	defer catalog.Close()
	_, err = catalog.BootstrapAdmin(context.Background(), identity.UserInput{Login: "admin", Password: "mutation-admin-password"})
	require.NoError(t, err)
	u, err := catalog.CreateUser(context.Background(), identity.UserInput{Login: "member", Password: "mutation-member-password", OrgID: 1, Role: identity.RoleNone})
	require.NoError(t, err)
	p, err := catalog.AuthenticatePassword(context.Background(), u.Login, "mutation-member-password", 1)
	require.NoError(t, err)
	resolver, err := instance.NewPermissionIdentityResolver(catalog, instance.PermissionResolverOptions{Revalidate: func(ctx context.Context, incoming identity.Principal) (identity.Principal, error) {
		return catalog.AuthenticatePassword(ctx, u.Login, "mutation-member-password", incoming.OrgID)
	}})
	require.NoError(t, err)
	s := permissionStore(t, 1)
	ctx := identity.WithPrincipal(context.Background(), p)
	f, err := s.CapturePermissionMutation(ctx, nil, resolver)
	require.NoError(t, err)
	d, err := s.SaveDashboardWithPermissions(ctx, model.Dashboard{ID: "live", Name: "Live"}, nil, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, f)
	require.NoError(t, err)
	ref := permissionRef(s, model.PermissionDashboard, d.ID)
	f, err = s.CapturePermissionMutation(ctx, []model.PermissionResource{ref}, resolver)
	require.NoError(t, err)
	member, err := catalog.Membership(ctx, 1, u.ID)
	require.NoError(t, err)
	member, err = catalog.ChangeOrganizationUserRole(ctx, 1, u.ID, member.Version, identity.RoleViewer)
	require.NoError(t, err)
	_, err = s.SaveDashboardWithPermissions(ctx, d, &d.UpdatedAt, model.ResourcePermissionSeed{}, f)
	require.ErrorIs(t, err, store.ErrPermissionFence)
	member, err = catalog.ChangeOrganizationUserRole(ctx, 1, u.ID, member.Version, identity.RoleNone)
	require.NoError(t, err)
	f, err = s.CapturePermissionMutation(ctx, []model.PermissionResource{ref}, resolver)
	require.NoError(t, err)
	removing, err := catalog.RemoveOrganizationUser(ctx, 1, u.ID, member.Version)
	require.NoError(t, err)
	require.NoError(t, catalog.CompleteMembershipRemoval(ctx, 1, u.ID, removing.Version))
	member, err = catalog.AddOrganizationUser(ctx, 1, u.ID, identity.RoleNone)
	require.NoError(t, err)
	require.Greater(t, member.Generation, removing.Generation)
	_, err = s.SaveDashboardWithPermissions(ctx, d, &d.UpdatedAt, model.ResourcePermissionSeed{}, f)
	require.ErrorIs(t, err, store.ErrPermissionFence, "password is still valid after rejoin, but captured generation is not")
	p, session, key, err := catalog.Login(ctx, u.Login, "mutation-member-password", 1, time.Hour, 30*time.Minute)
	require.NoError(t, err)
	requestResolver, err := instance.NewPermissionIdentityResolver(catalog, instance.PermissionResolverOptions{Revalidate: func(ctx context.Context, incoming identity.Principal) (identity.Principal, error) {
		return catalog.AuthenticateSession(ctx, key, incoming.OrgID)
	}})
	require.NoError(t, err)
	ctx = identity.WithPrincipal(context.Background(), p)
	f, err = s.CapturePermissionMutation(ctx, []model.PermissionResource{ref}, requestResolver)
	require.NoError(t, err)
	require.NoError(t, catalog.RevokeSession(ctx, u.ID, session.ID))
	_, err = s.SaveDashboardWithPermissions(ctx, d, &d.UpdatedAt, model.ResourcePermissionSeed{}, f)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	loaded, err := s.Dashboard(context.Background(), d.ID)
	require.NoError(t, err)
	require.Equal(t, d, loaded)
}

func TestPermissionMutationsExpectedRevisionIsCopiedBeforeCallbacks(t *testing.T) {
	s := permissionStore(t, 1)
	ctx := mutationContext(s)
	d := mutationDashboard(t, s, ctx, "revision-copy", "")
	ref := permissionRef(s, model.PermissionDashboard, d.ID)
	revision := int64(0)
	modify := false
	resolver := permissionFixtureResolver()
	resolver.CurrentPrincipal = func(_ context.Context, p identity.Principal) (identity.Principal, error) {
		if modify {
			revision = d.UpdatedAt
		}
		return p, p.Validate()
	}
	f, err := s.CapturePermissionMutation(ctx, []model.PermissionResource{ref}, resolver)
	require.NoError(t, err)
	modify = true
	changed := d
	changed.Name = "Must not save invalid initial revision"
	_, err = s.SaveDashboardWithPermissions(ctx, changed, &revision, model.ResourcePermissionSeed{}, f)
	require.ErrorIs(t, err, store.ErrDashboardConflict)
	actual, err := s.Dashboard(ctx, d.ID)
	require.NoError(t, err)
	require.Equal(t, d, actual)
}

func TestPermissionMutationsMigrationAndReadinessValidateLibraryOwners(t *testing.T) {
	s := permissionStore(t, 2)
	ctx := identity.WithPrincipal(context.Background(), identity.BackgroundPrincipal(2))
	_, err := s.CreateFolder(ctx, model.Folder{UID: "library-owner", Title: "Owner"})
	require.NoError(t, err)
	_, err = s.SaveLibraryElement(ctx, model.LibraryElement{UID: "legacy-lib", Name: "Legacy", Kind: 1, FolderUID: "library-owner", Model: []byte(`{"type":"text"}`)}, true)
	require.NoError(t, err)
	_, err = s.DB.Exec(`UPDATE library_elements SET folder_uid='missing-owner' WHERE uid='legacy-lib'`)
	require.NoError(t, err)
	status, err := s.MigrateResourcePermissions(ctx, 1000, permissionFixtureResolver())
	require.Error(t, err)
	require.False(t, status.Ready)
	var count int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM resource_acl_migration`).Scan(&count))
	require.Zero(t, count, "orphan library ownership cannot commit a readiness marker")
	_, err = s.DB.Exec(`UPDATE library_elements SET folder_uid='library-owner' WHERE uid='legacy-lib'`)
	require.NoError(t, err)
	status, err = s.MigrateResourcePermissions(ctx, 1000, permissionFixtureResolver())
	require.NoError(t, err)
	require.True(t, status.Ready)
	_, err = s.DB.Exec(`UPDATE library_elements SET folder_uid='missing-owner' WHERE uid='legacy-lib'`)
	require.NoError(t, err)
	status, err = s.ResourcePermissionReadiness(ctx)
	require.Error(t, err)
	require.False(t, status.Ready, "a previous completion marker cannot hide newly invalid library ownership")
	_, err = s.DB.Exec(`UPDATE library_elements SET folder_uid='general' WHERE uid='legacy-lib'`)
	require.NoError(t, err)
	status, err = s.ResourcePermissionReadiness(ctx)
	require.NoError(t, err)
	require.True(t, status.Ready, "the normalized General owner is a valid root scope")
}
