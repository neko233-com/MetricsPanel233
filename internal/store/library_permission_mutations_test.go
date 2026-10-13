package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/instance"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/require"
)

const libraryPermissionRaw = `{"type":"text","opaque":900719925474099312345,"options":{"content":"original","unknown":900719925474099312346}}`

func permissionLibraryElement(uid, owner string) model.LibraryElement {
	return model.LibraryElement{UID: uid, Name: uid, Kind: 1, FolderUID: owner, Model: json.RawMessage(libraryPermissionRaw)}
}

func createPermissionLibrary(t *testing.T, s *store.Store, ctx context.Context, uid, owner string) model.LibraryElement {
	t.Helper()
	refs := []model.PermissionResource{}
	if model.RootFolderUID(owner) != "" {
		refs = append(refs, permissionRef(s, model.PermissionFolder, owner))
	}
	fence := mutationFence(t, s, ctx, refs, uid)
	e, err := s.SaveLibraryElementWithPermissions(ctx, permissionLibraryElement(uid, owner), true, fence)
	require.NoError(t, err)
	return e
}

func TestLibraryPermissionMutationPreservesRawConnectedDashboardsAndRestart(t *testing.T) {
	s := permissionStore(t, 2)
	ctx := mutationContext(s)
	a := mutationFolder(t, s, ctx, "owner-a", "")
	b := mutationFolder(t, s, ctx, "owner-b", "")
	var initialACLCount int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM resource_acl_resources`).Scan(&initialACLCount))
	e := createPermissionLibrary(t, s, ctx, "library", a.UID)
	require.Equal(t, int64(2), e.OrgID)
	require.Equal(t, libraryPermissionRaw, string(e.Model))
	raw := json.RawMessage(`{"uid":"external-linked","opaque":900719925474099312347,"panels":[{"id":1,"type":"text","libraryPanel":{"uid":"library","name":"embedded original"},"options":{"opaque":900719925474099312348}}]}`)
	dashboard, err := s.SaveDashboard(ctx, model.Dashboard{ID: "linked", Name: "Connected", Grafana: raw})
	require.NoError(t, err)
	connections, err := s.LibraryConnections(ctx, e.UID)
	require.NoError(t, err)
	require.Len(t, connections, 1)
	versions, err := s.DashboardVersions(ctx, dashboard.ID, 0, 20, "")
	require.NoError(t, err)
	fence := mutationFence(t, s, ctx, []model.PermissionResource{permissionRef(s, model.PermissionFolder, b.UID)}, e.UID)
	e.Name, e.FolderUID = "Moved library", b.UID
	e.Model = json.RawMessage(strings.Replace(libraryPermissionRaw, "original", "updated", 1))
	e, err = s.SaveLibraryElementWithPermissions(ctx, e, false, fence)
	require.NoError(t, err)
	require.Equal(t, int64(2), e.Version)
	require.Equal(t, b.UID, e.FolderUID)
	require.Equal(t, int64(1), e.Meta.ConnectedDashboards)
	require.Contains(t, string(e.Model), "900719925474099312345")
	loaded, err := s.Dashboard(ctx, dashboard.ID)
	require.NoError(t, err)
	require.Equal(t, dashboard, loaded, "standalone library saves never rewrite connected dashboard content or revision")
	actualConnections, err := s.LibraryConnections(ctx, e.UID)
	require.NoError(t, err)
	require.Equal(t, connections, actualConnections)
	actualVersions, err := s.DashboardVersions(ctx, dashboard.ID, 0, 20, "")
	require.NoError(t, err)
	require.Equal(t, versions, actualVersions)
	root := createPermissionLibrary(t, s, ctx, "general-library", "general")
	require.Empty(t, root.FolderUID)
	var aclCount int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM resource_acl_resources`).Scan(&aclCount))
	require.Equal(t, initialACLCount, aclCount, "library mutations do not seed invented independent ACLs")
	path := s.Path
	require.NoError(t, s.DB.Close())
	reopened, err := store.OpenForOrganization(path, 2)
	require.NoError(t, err)
	defer reopened.DB.Close()
	restored, err := reopened.LibraryElement(ctx, e.UID)
	require.NoError(t, err)
	require.Equal(t, e, restored)
	_, err = reopened.SaveLibraryElementWithPermissions(ctx, e, false, fence)
	require.ErrorIs(t, err, store.ErrPermissionFence, "even the same path reopened Store cannot reuse a previous issuing capability")
	fresh := mutationFence(t, reopened, ctx, nil, e.UID)
	require.ErrorIs(t, reopened.DeleteLibraryElementWithPermissions(ctx, e.UID, e.Version, fresh), store.ErrLibraryConnected)
}

func TestLibraryPermissionMutationRequiresCapturedUIDOwnerAndActor(t *testing.T) {
	s := permissionStore(t, 2)
	ctx := mutationContext(s)
	owner := mutationFolder(t, s, ctx, "owner", "")
	input := permissionLibraryElement("new-library", owner.UID)
	_, err := s.SaveLibraryElementWithPermissions(ctx, input, true, store.PermissionMutationFence{})
	require.ErrorIs(t, err, store.ErrPermissionFence)
	uncovered := mutationFence(t, s, ctx, nil, input.UID)
	_, err = s.SaveLibraryElementWithPermissions(ctx, input, true, uncovered)
	require.ErrorIs(t, err, store.ErrPermissionFence, "capturing a missing library does not capture an arbitrary destination owner")
	refs := []model.PermissionResource{permissionRef(s, model.PermissionFolder, owner.UID)}
	fence := mutationFence(t, s, ctx, refs, input.UID)
	for _, bad := range []model.LibraryElement{
		{UID: "uncaptured", Name: "Other", Kind: 1, FolderUID: owner.UID, Model: input.Model},
		{UID: input.UID, Name: input.Name, Kind: 1, FolderUID: owner.UID, OrgID: 1, Model: input.Model},
	} {
		_, err = s.SaveLibraryElementWithPermissions(ctx, bad, true, fence)
		require.Error(t, err)
	}
	wrongActor := identity.WithPrincipal(context.Background(), permissionActor(2, 8, identity.TypeUser, identity.RoleNone))
	_, err = s.SaveLibraryElementWithPermissions(wrongActor, input, true, fence)
	require.ErrorIs(t, err, store.ErrPermissionFence)
	wrongOrganization := identity.WithPrincipal(context.Background(), permissionActor(1, 7, identity.TypeUser, identity.RoleNone))
	_, err = s.SaveLibraryElementWithPermissions(wrongOrganization, input, true, fence)
	require.ErrorIs(t, err, identity.ErrForbidden)
	_, err = s.SaveLibraryElementWithPermissions(context.Background(), input, true, fence)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	foreign := permissionStore(t, 2)
	_, err = foreign.SaveLibraryElementWithPermissions(mutationContext(foreign), input, true, fence)
	require.ErrorIs(t, err, store.ErrPermissionFence)
	created, err := s.SaveLibraryElementWithPermissions(ctx, input, true, fence)
	require.NoError(t, err)
	updateFence := mutationFence(t, s, ctx, nil, created.UID)
	require.ErrorIs(t, s.DeleteLibraryElementWithPermissions(ctx, created.UID, created.Version, store.PermissionMutationFence{}), store.ErrPermissionFence)
	require.ErrorIs(t, s.DeleteLibraryElementWithPermissions(wrongActor, created.UID, created.Version, updateFence), store.ErrPermissionFence)
	require.ErrorIs(t, s.DeleteLibraryElementWithPermissions(wrongOrganization, created.UID, created.Version, updateFence), identity.ErrForbidden)
	require.ErrorIs(t, foreign.DeleteLibraryElementWithPermissions(mutationContext(foreign), created.UID, created.Version, updateFence), store.ErrPermissionFence)
	destination := mutationFolder(t, s, ctx, "uncaptured-destination", "")
	moved := created
	moved.FolderUID = destination.UID
	_, err = s.SaveLibraryElementWithPermissions(ctx, moved, false, updateFence)
	require.ErrorIs(t, err, store.ErrPermissionFence, "automatic stored-owner capture cannot authorize a new uncaptured destination")
	_, err = s.SaveLibraryElementWithPermissions(ctx, created, true, updateFence)
	require.ErrorIs(t, err, store.ErrResourceExists)
	for _, version := range []int64{0, -1, 2} {
		bad := created
		bad.Version = version
		_, err = s.SaveLibraryElementWithPermissions(ctx, bad, false, updateFence)
		require.ErrorIs(t, err, store.ErrResourceConflict)
		require.ErrorIs(t, s.DeleteLibraryElementWithPermissions(ctx, created.UID, version, updateFence), store.ErrResourceConflict)
	}
	badID := created
	badID.ID++
	_, err = s.SaveLibraryElementWithPermissions(ctx, badID, false, updateFence)
	require.ErrorIs(t, err, store.ErrResourceConflict)
	loaded, err := s.LibraryElement(ctx, created.UID)
	require.NoError(t, err)
	require.Equal(t, created, loaded)
}

func TestLibraryPermissionMutationCASOwnerAncestryAndReincarnation(t *testing.T) {
	for _, change := range []string{"library-version", "library-owner", "old-owner-acl", "ancestor-move", "destination-change", "reincarnation"} {
		t.Run(change, func(t *testing.T) {
			s := permissionStore(t, 2)
			ctx := mutationContext(s)
			parent := mutationFolder(t, s, ctx, "parent", "")
			owner := mutationFolder(t, s, ctx, "owner", parent.UID)
			destination := mutationFolder(t, s, ctx, "destination", "")
			e := createPermissionLibrary(t, s, ctx, "library", owner.UID)
			fence := mutationFence(t, s, ctx, []model.PermissionResource{permissionRef(s, model.PermissionFolder, destination.UID)}, e.UID)
			switch change {
			case "library-version":
				changed := e
				changed.Name = "Other writer"
				_, err := s.SaveLibraryElement(ctx, changed, false)
				require.NoError(t, err)
			case "library-owner":
				_, err := s.DB.Exec(`UPDATE library_elements SET folder_uid=? WHERE uid=?`, destination.UID, e.UID)
				require.NoError(t, err, "owner is independently fenced even without a version change")
			case "old-owner-acl":
				ref := permissionRef(s, model.PermissionFolder, owner.UID)
				doc, err := s.ResourcePermissions(ctx, ref)
				require.NoError(t, err)
				_, err = s.ReplaceResourcePermissions(ctx, ref, doc.Version, nil, permissionFixtureResolver())
				require.NoError(t, err)
			case "ancestor-move":
				_, err := s.MoveFolder(ctx, parent.UID, destination.UID)
				require.NoError(t, err)
			case "destination-change":
				destination.Title = "Changed destination"
				_, err := s.UpdateFolder(ctx, destination, false)
				require.NoError(t, err)
			case "reincarnation":
				require.NoError(t, s.DeleteLibraryElement(ctx, e.UID))
				fresh, err := s.SaveLibraryElement(ctx, permissionLibraryElement(e.UID, owner.UID), true)
				require.NoError(t, err)
				require.Equal(t, e.Version, fresh.Version)
				require.NotEqual(t, e.ID, fresh.ID)
			}
			before, err := s.LibraryElement(ctx, e.UID)
			require.NoError(t, err)
			changed := e
			changed.Name, changed.FolderUID = "Stale edit", destination.UID
			_, err = s.SaveLibraryElementWithPermissions(ctx, changed, false, fence)
			require.ErrorIs(t, err, store.ErrPermissionFence)
			require.ErrorIs(t, s.DeleteLibraryElementWithPermissions(ctx, e.UID, e.Version, fence), store.ErrPermissionFence)
			after, err := s.LibraryElement(ctx, e.UID)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestLibraryPermissionMutationLateConnectionsAndAtomicRollback(t *testing.T) {
	s := permissionStore(t, 1)
	ctx := mutationContext(s)
	e := createPermissionLibrary(t, s, ctx, "library", "")
	fence := mutationFence(t, s, ctx, nil, e.UID)
	raw := json.RawMessage(`{"panels":[{"id":1,"type":"text","libraryPanel":{"uid":"library"}}]}`)
	d, err := s.SaveDashboard(ctx, model.Dashboard{ID: "late-connection", Name: "Late", Grafana: raw})
	require.NoError(t, err)
	require.ErrorIs(t, s.DeleteLibraryElementWithPermissions(ctx, e.UID, e.Version, fence), store.ErrLibraryConnected)
	require.NoError(t, s.DeleteDashboard(ctx, d.ID))
	_, err = s.DB.Exec(`CREATE TABLE library_fixture_marker(value TEXT); CREATE TRIGGER library_reject_create AFTER INSERT ON library_elements WHEN NEW.uid='failed' BEGIN INSERT INTO library_fixture_marker VALUES('insert'); SELECT RAISE(ABORT,'library create fixture'); END`)
	require.NoError(t, err)
	failed := permissionLibraryElement("failed", "")
	createFence := mutationFence(t, s, ctx, nil, failed.UID)
	_, err = s.SaveLibraryElementWithPermissions(ctx, failed, true, createFence)
	require.ErrorContains(t, err, "library create fixture")
	_, err = s.LibraryElement(ctx, failed.UID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	var count int
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM library_fixture_marker`).Scan(&count))
	require.Zero(t, count)
	_, err = s.DB.Exec(`CREATE TRIGGER library_reject_update AFTER UPDATE ON library_elements WHEN OLD.uid='library' BEGIN INSERT INTO library_fixture_marker VALUES('update'); SELECT RAISE(ABORT,'library update fixture'); END`)
	require.NoError(t, err)
	changed := e
	changed.Name = "Must roll back"
	_, err = s.SaveLibraryElementWithPermissions(ctx, changed, false, fence)
	require.ErrorContains(t, err, "library update fixture")
	loaded, err := s.LibraryElement(ctx, e.UID)
	require.NoError(t, err)
	require.Equal(t, e, loaded)
	_, err = s.DB.Exec(`CREATE TRIGGER library_reject_delete AFTER DELETE ON library_elements WHEN OLD.uid='library' BEGIN INSERT INTO library_fixture_marker VALUES('delete'); SELECT RAISE(ABORT,'library delete fixture'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, s.DeleteLibraryElementWithPermissions(ctx, e.UID, e.Version, fence), "library delete fixture")
	loaded, err = s.LibraryElement(ctx, e.UID)
	require.NoError(t, err)
	require.Equal(t, e, loaded)
	require.NoError(t, s.DB.QueryRow(`SELECT count(*) FROM library_fixture_marker`).Scan(&count))
	require.Zero(t, count)
	_, err = s.DB.Exec(`DROP TRIGGER library_reject_create; DROP TRIGGER library_reject_update; DROP TRIGGER library_reject_delete`)
	require.NoError(t, err)
	_, err = s.SaveLibraryElementWithPermissions(ctx, failed, true, createFence)
	require.NoError(t, err, "a failed transaction consumes neither identity nor captured absence")
	require.NoError(t, s.DeleteLibraryElementWithPermissions(ctx, e.UID, e.Version, fence))
}

func TestLibraryPermissionMutationConcurrentCASAndCreate(t *testing.T) {
	for _, create := range []bool{false, true} {
		t.Run(map[bool]string{false: "update", true: "create"}[create], func(t *testing.T) {
			s := permissionStore(t, 2)
			ctx := mutationContext(s)
			e := permissionLibraryElement("library", "")
			if !create {
				e = createPermissionLibrary(t, s, ctx, e.UID, "")
			}
			fence := mutationFence(t, s, ctx, nil, e.UID)
			var results [2]error
			var wg sync.WaitGroup
			for i := range 2 {
				wg.Go(func() {
					changed := e
					changed.Name = []string{"First", "Second"}[i]
					_, results[i] = s.SaveLibraryElementWithPermissions(ctx, changed, create, fence)
				})
			}
			wg.Wait()
			var successes int
			for _, err := range results {
				if err == nil {
					successes++
				} else {
					require.ErrorIs(t, err, store.ErrPermissionFence)
				}
			}
			require.Equal(t, 1, successes)
			loaded, err := s.LibraryElement(ctx, e.UID)
			require.NoError(t, err)
			if create {
				require.Equal(t, int64(1), loaded.Version)
			} else {
				require.Equal(t, int64(2), loaded.Version)
			}
		})
	}
}

func TestLibraryPermissionMutationCopiesRawBeforeResolverAndAvoidsWriterCallbacks(t *testing.T) {
	s := permissionStore(t, 2)
	ctx := mutationContext(s)
	owner := mutationFolder(t, s, ctx, "owner", "")
	ref := permissionRef(s, model.PermissionFolder, owner.UID)
	resolver := permissionFixtureResolver()
	var calls atomic.Int64
	var mutate atomic.Bool
	input := permissionLibraryElement("library", owner.UID)
	original := string(input.Model)
	resolver.CurrentPrincipal = func(ctx context.Context, p identity.Principal) (identity.Principal, error) {
		calls.Add(1)
		if _, err := s.ResourcePermissions(ctx, ref); err != nil {
			return identity.Principal{}, err
		}
		if mutate.Load() {
			copy(input.Model, []byte(strings.Replace(original, "original", "changed!", 1)))
		}
		return p, nil
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	fence, err := s.CapturePermissionMutation(bounded, []model.PermissionResource{ref}, resolver, input.UID)
	require.NoError(t, err)
	mutate.Store(true)
	e, err := s.SaveLibraryElementWithPermissions(bounded, input, true, fence)
	require.NoError(t, err, "callback organization reads cannot run under its one-connection writer")
	require.NotEqual(t, original, string(input.Model))
	require.Equal(t, original, string(e.Model), "caller JSON mutation from a resolver cannot alter the frozen draft")
	fence, err = s.CapturePermissionMutation(bounded, nil, resolver, input.UID)
	require.NoError(t, err)
	require.NoError(t, s.DeleteLibraryElementWithPermissions(bounded, e.UID, e.Version, fence))
	require.Greater(t, calls.Load(), int64(5))
}

func TestLibraryPermissionMutationCatalogChangesAndOrganizationIsolation(t *testing.T) {
	for _, change := range []string{"role", "removed", "rejoined", "revoked"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			catalog, err := identity.Open(filepath.Join(t.TempDir(), "identity.db"))
			require.NoError(t, err)
			defer catalog.Close()
			_, err = catalog.BootstrapAdmin(ctx, identity.UserInput{Login: "admin", Password: "library-catalog-admin-password"})
			require.NoError(t, err)
			user, err := catalog.CreateUser(ctx, identity.UserInput{Login: "member", Password: "library-catalog-member-password", OrgID: 1, Role: identity.RoleNone})
			require.NoError(t, err)
			p, session, key, err := catalog.Login(ctx, user.Login, "library-catalog-member-password", 1, time.Hour, time.Hour)
			require.NoError(t, err)
			s := permissionStore(t, 1)
			controlCtx := mutationContext(s)
			owner := mutationFolder(t, s, controlCtx, "owner", "")
			e := createPermissionLibrary(t, s, controlCtx, "same-uid", owner.UID)
			foreign := permissionStore(t, 2)
			foreignCtx := mutationContext(foreign)
			foreignOwner := mutationFolder(t, foreign, foreignCtx, owner.UID, "")
			foreignLibrary := createPermissionLibrary(t, foreign, foreignCtx, e.UID, foreignOwner.UID)
			resolver, err := instance.NewPermissionIdentityResolver(catalog, instance.PermissionResolverOptions{Revalidate: func(ctx context.Context, _ identity.Principal) (identity.Principal, error) {
				// A real second catalog and a control read prove callback ordering.
				if _, err := s.ResourcePermissions(ctx, permissionRef(s, model.PermissionFolder, owner.UID)); err != nil {
					return identity.Principal{}, err
				}
				return catalog.AuthenticateSession(ctx, key, 1)
			}})
			require.NoError(t, err)
			requestCtx, cancel := context.WithTimeout(identity.WithPrincipal(ctx, p), 5*time.Second)
			defer cancel()
			fence, err := s.CapturePermissionMutation(requestCtx, nil, resolver, e.UID)
			require.NoError(t, err)
			member, err := catalog.Membership(ctx, 1, user.ID)
			require.NoError(t, err)
			switch change {
			case "role":
				_, err = catalog.ChangeOrganizationUserRole(ctx, 1, user.ID, member.Version, identity.RoleViewer)
			case "removed", "rejoined":
				member, err = catalog.RemoveOrganizationUser(ctx, 1, user.ID, member.Version)
				require.NoError(t, err)
				if change == "rejoined" {
					err = catalog.CompleteMembershipRemoval(ctx, 1, user.ID, member.Version)
					require.NoError(t, err)
					_, err = catalog.AddOrganizationUser(ctx, 1, user.ID, identity.RoleNone)
					require.NoError(t, err)
					_, _, key, err = catalog.Login(ctx, user.Login, "library-catalog-member-password", 1, time.Hour, time.Hour)
				}
			case "revoked":
				err = catalog.RevokeSession(ctx, user.ID, session.ID)
			}
			require.NoError(t, err)
			changed := e
			changed.Name = "Stale catalog actor"
			_, err = s.SaveLibraryElementWithPermissions(requestCtx, changed, false, fence)
			require.True(t, errors.Is(err, store.ErrPermissionFence) || errors.Is(err, identity.ErrUnauthenticated), "%v", err)
			err = s.DeleteLibraryElementWithPermissions(requestCtx, e.UID, e.Version, fence)
			require.True(t, errors.Is(err, store.ErrPermissionFence) || errors.Is(err, identity.ErrUnauthenticated), "%v", err)
			loaded, err := s.LibraryElement(controlCtx, e.UID)
			require.NoError(t, err)
			require.Equal(t, e, loaded)
			other, err := foreign.LibraryElement(foreignCtx, e.UID)
			require.NoError(t, err)
			require.Equal(t, foreignLibrary, other)
		})
	}
}
