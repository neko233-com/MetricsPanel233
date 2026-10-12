package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/neko233-com/MetricsPanel233/internal/identity"
	"github.com/neko233-com/MetricsPanel233/internal/model"
	"github.com/neko233-com/MetricsPanel233/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func permissionStore(t *testing.T, orgID int64) *store.Store {
	t.Helper()
	s, err := store.OpenForOrganization(filepath.Join(t.TempDir(), "permissions.db"), orgID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.DB.Close() })
	require.NoError(t, s.InitializeResourcePermissionSchema(context.Background()))
	return s
}

func permissionRef(s *store.Store, kind model.PermissionResourceKind, uid string) model.PermissionResource {
	return model.PermissionResource{OrgID: s.OrganizationID(), Kind: kind, UID: uid}
}

func permissionFixtureResolver() store.PermissionIdentityResolver {
	return store.PermissionIdentityResolver{
		CurrentPrincipal: func(_ context.Context, p identity.Principal) (identity.Principal, error) { return p, p.Validate() },
		ValidateSubject: func(_ context.Context, _ int64, subject model.ResourcePermissionSubject) (int64, error) {
			return 1, subject.Validate()
		},
		TeamsForPrincipal: func(context.Context, int64, model.ResourcePermissionSubject) ([]identity.TeamGrant, error) {
			return nil, nil
		},
	}
}

func permissionSubject(kind model.PermissionSubjectKind, id int64) model.ResourcePermissionSubject {
	return model.ResourcePermissionSubject{Kind: kind, ID: id}
}

func permissionActor(orgID, id int64, kind identity.PrincipalType, role identity.Role) identity.Principal {
	return identity.Principal{OrgID: orgID, ID: id, Type: kind, Role: role, Login: "fixture"}
}

func seedPermission(t *testing.T, s *store.Store, ref model.PermissionResource, seed model.ResourcePermissionSeed) model.ResourcePermissionDocument {
	t.Helper()
	doc, err := s.InitializeResourcePermissions(context.Background(), ref, seed, permissionFixtureResolver())
	require.NoError(t, err)
	return doc
}

func TestResourcePermissionsCreationPoliciesAndRestart(t *testing.T) {
	ctx := context.Background()
	s := permissionStore(t, 1)
	creator := permissionSubject(model.PermissionUser, 7)
	_, err := s.CreateFolder(ctx, model.Folder{UID: "root", Title: "Root"})
	require.NoError(t, err)
	_, err = s.CreateFolder(ctx, model.Folder{UID: "nested", ParentUID: "root", Title: "Nested"})
	require.NoError(t, err)
	root := permissionRef(s, model.PermissionFolder, "root")
	doc := seedPermission(t, s, root, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults, Creator: &creator})
	require.Equal(t, int64(1), doc.Version)
	require.Len(t, doc.Items, 3)
	assert.True(t, doc.DefaultsInitialized)
	nested := permissionRef(s, model.PermissionFolder, "nested")
	doc = seedPermission(t, s, nested, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults, Creator: &creator})
	assert.Empty(t, doc.Items, "App Platform nested creation inherits without a creator grant")
	_, err = s.SaveDashboard(ctx, model.Dashboard{ID: "native-id", Name: "Imported", FolderUID: "nested", Grafana: []byte(`{"uid":"external-uid","opaque":900719925474099312345}`)})
	require.NoError(t, err)
	native := permissionRef(s, model.PermissionDashboard, "native-id")
	doc = seedPermission(t, s, native, model.ResourcePermissionSeed{Policy: model.PermissionLegacyDefaults, Creator: &creator})
	require.Len(t, doc.Items, 1)
	assert.Equal(t, creator, doc.Items[0].Subject)
	assert.Equal(t, model.PermissionAdmin, doc.Items[0].Permission)
	_, err = s.ResourcePermissions(ctx, permissionRef(s, model.PermissionDashboard, "external-uid"))
	assert.ErrorIs(t, err, sql.ErrNoRows, "the external source UID is not a second ACL identity")
	doc, err = s.ReplaceResourcePermissions(ctx, native, doc.Version, nil, permissionFixtureResolver())
	require.NoError(t, err)
	assert.Empty(t, doc.Items)
	seeded := seedPermission(t, s, native, model.ResourcePermissionSeed{Policy: model.PermissionLegacyDefaults, Creator: &creator})
	assert.Equal(t, doc, seeded, "initializer cannot reset a deliberately edited empty ACL")
	seeded, err = s.InitializeResourcePermissions(ctx, native, model.ResourcePermissionSeed{Policy: model.PermissionLegacyDefaults, Creator: &creator}, store.PermissionIdentityResolver{})
	require.NoError(t, err, "an already seeded resource does not depend on the former creator still existing")
	assert.Equal(t, doc, seeded)
	_, err = s.SaveDashboard(ctx, model.Dashboard{ID: "provisioned", Name: "Provisioned", FolderUID: "nested"})
	require.NoError(t, err)
	provisioned := seedPermission(t, s, permissionRef(s, model.PermissionDashboard, "provisioned"), model.ResourcePermissionSeed{Policy: model.PermissionLegacyDefaults, Creator: &creator, Provisioned: true})
	assert.Empty(t, provisioned.Items)
	path := s.Path
	require.NoError(t, s.DB.Close())
	s, err = store.Open(path)
	require.NoError(t, err)
	defer s.DB.Close()
	require.NoError(t, s.InitializeResourcePermissionSchema(ctx))
	restored, err := s.ResourcePermissions(ctx, native)
	require.NoError(t, err)
	assert.Equal(t, doc, restored)
	rows, err := s.Dashboards(ctx)
	require.NoError(t, err)
	for _, dashboard := range rows {
		if dashboard.ID == "native-id" {
			assert.Contains(t, string(dashboard.Grafana), "900719925474099312345", "permission storage never rewrites source JSON")
		}
	}
}

func TestResourcePermissionsTypedNoneAndExactRoleGrants(t *testing.T) {
	ctx := context.Background()
	s := permissionStore(t, 2)
	ref := permissionRef(s, model.PermissionDashboard, "system")
	doc := seedPermission(t, s, ref, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults})
	grants := []model.ResourcePermissionGrant{
		{Subject: permissionSubject(model.PermissionUser, 1), Permission: model.PermissionEdit},
		{Subject: permissionSubject(model.PermissionServiceAccount, 1), Permission: model.PermissionView},
		{Subject: model.ResourcePermissionSubject{Kind: model.PermissionRole, Role: "Viewer"}, Permission: model.PermissionView},
	}
	_, err := s.ReplaceResourcePermissions(ctx, ref, doc.Version, grants, permissionFixtureResolver())
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		p    identity.Principal
		want model.ResourcePermissionLevel
	}{
		{"None human", permissionActor(2, 1, identity.TypeUser, identity.RoleNone), model.PermissionEdit},
		{"None account with same ID", permissionActor(2, 1, identity.TypeServiceAccount, identity.RoleNone), model.PermissionView},
		{"Viewer role", permissionActor(2, 2, identity.TypeUser, identity.RoleViewer), model.PermissionView},
		{"Editor is not exact Viewer subject", permissionActor(2, 2, identity.TypeUser, identity.RoleEditor), model.PermissionNone},
		{"Organization admin baseline", permissionActor(2, 2, identity.TypeUser, identity.RoleAdmin), model.PermissionAdmin},
		{"Server flag is not OrgAdmin", identity.Principal{OrgID: 2, ID: 2, Type: identity.TypeUser, Login: "server-admin", Role: identity.RoleNone, IsServerAdmin: true}, model.PermissionNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := s.EffectiveResourcePermission(ctx, ref, tc.p, permissionFixtureResolver())
			require.NoError(t, err)
			assert.Equal(t, tc.want, result.Permission)
		})
	}
	bad := permissionActor(2, 1, identity.TypeServiceAccount, identity.RoleAdmin)
	bad.IsServerAdmin = true
	_, err = s.EffectiveResourcePermission(ctx, ref, bad, permissionFixtureResolver())
	assert.ErrorIs(t, err, identity.ErrUnauthenticated)
}

func TestResourcePermissionsInheritanceMoveAndIntegrity(t *testing.T) {
	ctx := context.Background()
	s := permissionStore(t, 1)
	actor := permissionActor(1, 5, identity.TypeUser, identity.RoleNone)
	for _, folder := range []model.Folder{{UID: "root", Title: "Root"}, {UID: "parent", Title: "Parent", ParentUID: "root"}, {UID: "other", Title: "Other"}} {
		_, err := s.CreateFolder(ctx, folder)
		require.NoError(t, err)
		seedPermission(t, s, permissionRef(s, model.PermissionFolder, folder.UID), model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults})
	}
	_, err := s.SaveDashboard(ctx, model.Dashboard{ID: "child", Name: "Child", FolderUID: "parent"})
	require.NoError(t, err)
	ref := permissionRef(s, model.PermissionDashboard, "child")
	doc := seedPermission(t, s, ref, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults})
	_, err = s.ReplaceResourcePermissions(ctx, ref, doc.Version, []model.ResourcePermissionGrant{{Subject: permissionSubject(model.PermissionUser, 5), Permission: model.PermissionView}}, permissionFixtureResolver())
	require.NoError(t, err)
	parent := permissionRef(s, model.PermissionFolder, "root")
	doc, err = s.ResourcePermissions(ctx, parent)
	require.NoError(t, err)
	_, err = s.ReplaceResourcePermissions(ctx, parent, doc.Version, []model.ResourcePermissionGrant{{Subject: permissionSubject(model.PermissionUser, 5), Permission: model.PermissionAdmin}}, permissionFixtureResolver())
	require.NoError(t, err)
	result, err := s.EffectiveResourcePermission(ctx, ref, actor, permissionFixtureResolver())
	require.NoError(t, err)
	assert.Equal(t, model.PermissionAdmin, result.Permission, "child View cannot reduce grandparent Admin")
	require.Len(t, result.Items, 2)
	assert.True(t, result.Items[1].Inherited)
	assert.Len(t, result.Versions, 6, "ACL and resource revisions include the complete stored ancestry")
	_, err = s.MoveFolder(ctx, "parent", "other")
	require.NoError(t, err)
	result, err = s.EffectiveResourcePermission(ctx, ref, actor, permissionFixtureResolver())
	require.NoError(t, err)
	assert.Equal(t, model.PermissionView, result.Permission, "evaluation reads new ownership rather than cached old parents")
	_, err = s.DB.ExecContext(ctx, `UPDATE folders SET parent_uid='parent' WHERE uid='other'`)
	require.NoError(t, err)
	_, err = s.EffectiveResourcePermission(ctx, ref, actor, permissionFixtureResolver())
	assert.ErrorIs(t, err, store.ErrPermissionAncestry)
	_, err = s.DB.ExecContext(ctx, `UPDATE folders SET parent_uid='absent' WHERE uid='other'`)
	require.NoError(t, err)
	_, err = s.EffectiveResourcePermission(ctx, ref, actor, permissionFixtureResolver())
	assert.ErrorIs(t, err, sql.ErrNoRows, "missing parent cannot become General access")
}

func TestResourcePermissionsValidationConflictAndOwner(t *testing.T) {
	ctx := context.Background()
	s := permissionStore(t, 2)
	ref := permissionRef(s, model.PermissionDashboard, "system")
	doc := seedPermission(t, s, ref, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults})
	invalid := []model.ResourcePermissionGrant{
		{Subject: permissionSubject(model.PermissionUser, 5), Permission: model.PermissionEdit},
		{Subject: permissionSubject(model.PermissionUser, 6), Permission: 3},
	}
	_, err := s.ReplaceResourcePermissions(ctx, ref, doc.Version, invalid, permissionFixtureResolver())
	require.Error(t, err)
	unchanged, err := s.ResourcePermissions(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, doc, unchanged)
	duplicate := []model.ResourcePermissionGrant{{Subject: permissionSubject(model.PermissionUser, 5), Permission: model.PermissionView}, {Subject: permissionSubject(model.PermissionUser, 5), Permission: model.PermissionAdmin}}
	_, err = s.ReplaceResourcePermissions(ctx, ref, doc.Version, duplicate, permissionFixtureResolver())
	assert.Error(t, err)
	foreign := ref
	foreign.OrgID = 1
	_, err = s.ResourcePermissions(ctx, foreign)
	assert.ErrorIs(t, err, identity.ErrForbidden)
	foreignCtx := identity.WithPrincipal(ctx, identity.LocalPrincipal())
	_, err = s.ResourcePermissions(foreignCtx, ref)
	assert.ErrorIs(t, err, identity.ErrForbidden)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO resource_acl_resources VALUES(1,'dashboard','system',1,1,1,1)`)
	assert.Error(t, err, "physical owner trigger prevents foreign organization rows")
	_, err = s.DB.ExecContext(ctx, `UPDATE resource_acl_resources SET org_id=1 WHERE resource_uid='system'`)
	assert.Error(t, err, "owner cannot be changed through an ACL update")
	_, err = s.ReplaceResourcePermissions(ctx, ref, doc.Version, []model.ResourcePermissionGrant{{Subject: permissionSubject(model.PermissionTeam, 4), Permission: model.PermissionView}}, store.PermissionIdentityResolver{})
	assert.ErrorIs(t, err, store.ErrPermissionIdentity)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := s.ReplaceResourcePermissions(ctx, ref, doc.Version, nil, permissionFixtureResolver())
			results <- err
		})
	}
	wg.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, store.ErrPermissionVersion) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, conflicts)
}

func TestResourcePermissionsFailClosedMissingDepthAndMalformedOwnership(t *testing.T) {
	ctx := context.Background()
	s := permissionStore(t, 1)
	actor := permissionActor(1, 8, identity.TypeUser, identity.RoleNone)
	ref := permissionRef(s, model.PermissionDashboard, "system")
	_, err := s.EffectiveResourcePermission(ctx, ref, actor, permissionFixtureResolver())
	assert.ErrorIs(t, err, store.ErrPermissionUninitialized)
	_, err = s.ResourcePermissions(ctx, permissionRef(s, model.PermissionFolder, "general"))
	assert.Error(t, err, "General cannot receive an inheritable role grant")
	var parent string
	for i := range 65 {
		uid := fmt.Sprintf("depth-%02d", i)
		_, err := s.CreateFolder(ctx, model.Folder{UID: uid, Title: uid, ParentUID: parent})
		require.NoError(t, err)
		seedPermission(t, s, permissionRef(s, model.PermissionFolder, uid), model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults})
		parent = uid
	}
	_, err = s.SaveDashboard(ctx, model.Dashboard{ID: "deep", Name: "Deep", FolderUID: parent})
	require.NoError(t, err)
	deep := permissionRef(s, model.PermissionDashboard, "deep")
	seedPermission(t, s, deep, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults})
	_, err = s.EffectiveResourcePermission(ctx, deep, actor, permissionFixtureResolver())
	assert.ErrorIs(t, err, store.ErrPermissionAncestry, "65 ancestors exceed the bounded resolver")
	_, err = s.DB.ExecContext(ctx, `UPDATE dashboards SET extras=json_set(extras,'$.folder_uid',7) WHERE id='deep'`)
	require.NoError(t, err)
	_, err = s.EffectiveResourcePermission(ctx, deep, actor, permissionFixtureResolver())
	assert.ErrorIs(t, err, store.ErrPermissionAncestry, "corrupt numeric ownership cannot be cast to a folder grant")
	resolver := permissionFixtureResolver()
	resolver.CurrentPrincipal = func(_ context.Context, p identity.Principal) (identity.Principal, error) { p.OrgID = 2; return p, nil }
	_, err = s.EffectiveResourcePermission(ctx, ref, actor, resolver)
	assert.ErrorIs(t, err, store.ErrPermissionIdentity, "catalog callback cannot change the authenticated identity's organization")
}

func TestResourcePermissionsCurrentIdentityAndTeamRevocation(t *testing.T) {
	ctx := context.Background()
	s := permissionStore(t, 1)
	catalog, err := identity.Open(filepath.Join(t.TempDir(), "identity.db"))
	require.NoError(t, err)
	defer catalog.Close()
	_, err = catalog.BootstrapAdmin(ctx, identity.UserInput{Login: "admin", Password: "test-admin-password", Name: "Admin"})
	require.NoError(t, err)
	user, err := catalog.CreateUser(ctx, identity.UserInput{Login: "member", Password: "test-member-password", Name: "Member", OrgID: 1, Role: identity.RoleNone})
	require.NoError(t, err)
	team, err := catalog.CreateTeam(ctx, 1, identity.TeamInput{Name: "Readers"})
	require.NoError(t, err)
	member, err := catalog.AddTeamMember(ctx, 1, team.ID, team.Version, identity.TeamPrincipal{Kind: identity.TypeUser, ID: user.ID}, identity.TeamMemberRole)
	require.NoError(t, err)
	resolver := store.PermissionIdentityResolver{
		CurrentPrincipal: func(ctx context.Context, p identity.Principal) (identity.Principal, error) {
			return catalog.UserPrincipal(ctx, p.ID, p.OrgID)
		},
		ValidateSubject: func(ctx context.Context, orgID int64, subject model.ResourcePermissionSubject) (int64, error) {
			if subject.Kind == model.PermissionUser {
				if _, err := catalog.UserPrincipal(ctx, subject.ID, orgID); err != nil {
					return 0, err
				}
				membership, err := catalog.Membership(ctx, orgID, subject.ID)
				return membership.Generation, err
			}
			if subject.Kind != model.PermissionTeam {
				return 0, identity.ErrForbidden
			}
			current, err := catalog.Team(ctx, orgID, subject.ID)
			if err != nil {
				return 0, err
			}
			if current.State != identity.TeamActive {
				return 0, identity.ErrForbidden
			}
			return 1, nil
		},
		TeamsForPrincipal: func(ctx context.Context, orgID int64, subject model.ResourcePermissionSubject) ([]identity.TeamGrant, error) {
			return catalog.TeamsForPrincipal(ctx, orgID, identity.TeamPrincipal{Kind: identity.PrincipalType(subject.Kind), ID: subject.ID})
		},
	}
	ref := permissionRef(s, model.PermissionDashboard, "system")
	doc := seedPermission(t, s, ref, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults})
	_, err = s.ReplaceResourcePermissions(ctx, ref, doc.Version, []model.ResourcePermissionGrant{{Subject: permissionSubject(model.PermissionTeam, team.ID), Permission: model.PermissionEdit}}, resolver)
	require.NoError(t, err)
	stale := permissionActor(1, user.ID, identity.TypeUser, identity.RoleAdmin)
	result, err := s.EffectiveResourcePermission(ctx, ref, stale, resolver)
	require.NoError(t, err)
	assert.Equal(t, model.PermissionEdit, result.Permission, "catalog None supersedes stale token Admin")
	require.NoError(t, catalog.RemoveTeamMember(ctx, 1, team.ID, member.Version, member.Principal))
	result, err = s.EffectiveResourcePermission(ctx, ref, stale, resolver)
	require.NoError(t, err)
	assert.Equal(t, model.PermissionNone, result.Permission, "team grant is recomputed each call")
	_, err = catalog.SetUserDisabled(ctx, user.ID, user.Version, true)
	require.NoError(t, err)
	_, err = s.EffectiveResourcePermission(ctx, ref, stale, resolver)
	assert.Error(t, err, "disabled principal cannot reuse a stale authenticated role")
}

func TestResourcePermissionsCleanupIsTypedAtomicAndReplaySafe(t *testing.T) {
	ctx := context.Background()
	s := permissionStore(t, 1)
	ref := permissionRef(s, model.PermissionDashboard, "system")
	doc := seedPermission(t, s, ref, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults})
	grants := []model.ResourcePermissionGrant{{Subject: permissionSubject(model.PermissionUser, 3), Permission: model.PermissionAdmin}, {Subject: permissionSubject(model.PermissionServiceAccount, 3), Permission: model.PermissionView}, {Subject: permissionSubject(model.PermissionTeam, 3), Permission: model.PermissionEdit}}
	doc, err := s.ReplaceResourcePermissions(ctx, ref, doc.Version, grants, permissionFixtureResolver())
	require.NoError(t, err)
	job := model.ResourcePermissionCleanup{JobID: "membership-1-3-v2", OrgID: 1, Subject: permissionSubject(model.PermissionUser, 3), IdentityVersion: 2, IdentityGeneration: 1}
	require.NoError(t, s.CleanupResourcePermissionSubject(ctx, job))
	cleaned, err := s.ResourcePermissions(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, doc.Version+1, cleaned.Version)
	require.Len(t, cleaned.Items, 2)
	for _, grant := range cleaned.Items {
		assert.NotEqual(t, model.PermissionUser, grant.Subject.Kind)
	}
	require.NoError(t, s.CleanupResourcePermissionSubject(ctx, job))
	replayed, err := s.ResourcePermissions(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, cleaned, replayed, "same cleanup receipt must not repeatedly increment ACL versions")
	wrong := job
	wrong.Subject = permissionSubject(model.PermissionServiceAccount, 3)
	assert.ErrorIs(t, s.CleanupResourcePermissionSubject(ctx, wrong), store.ErrPermissionCleanup)
	wrong = job
	wrong.OrgID = 2
	assert.ErrorIs(t, s.CleanupResourcePermissionSubject(ctx, wrong), identity.ErrForbidden)
	wrong = job
	wrong.IdentityVersion = 3
	assert.ErrorIs(t, s.CleanupResourcePermissionSubject(ctx, wrong), store.ErrPermissionCleanup)
	wrong = job
	wrong.IdentityGeneration = 2
	assert.ErrorIs(t, s.CleanupResourcePermissionSubject(ctx, wrong), store.ErrPermissionCleanup)
}

func TestResourcePermissionsDelayedOldGenerationDoesNotReviveAfterRejoin(t *testing.T) {
	ctx := context.Background()
	s := permissionStore(t, 1)
	ref := permissionRef(s, model.PermissionDashboard, "system")
	creator := permissionSubject(model.PermissionUser, 9)
	var generation atomic.Int64
	generation.Store(1)
	validated, resume := make(chan struct{}), make(chan struct{})
	delayed := permissionFixtureResolver()
	delayed.ValidateSubject = func(context.Context, int64, model.ResourcePermissionSubject) (int64, error) {
		captured := generation.Load()
		close(validated)
		<-resume
		return captured, nil
	}
	type result struct {
		doc model.ResourcePermissionDocument
		err error
	}
	seeded := make(chan result, 1)
	go func() {
		doc, err := s.InitializeResourcePermissions(ctx, ref, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults, Creator: &creator}, delayed)
		seeded <- result{doc, err}
	}()
	<-validated // Catalog lookup completed, but no org writer is held.
	job := model.ResourcePermissionCleanup{JobID: "remove-member-9-generation-1", OrgID: 1, Subject: creator, IdentityVersion: 2, IdentityGeneration: 1}
	require.NoError(t, s.CleanupResourcePermissionSubject(ctx, job))
	generation.Store(2) // Same human rejoined with a new durable membership incarnation.
	close(resume)
	late := <-seeded
	require.NoError(t, late.err)
	var oldGrant model.ResourcePermissionGrant
	for _, grant := range late.doc.Items {
		if grant.Subject == creator {
			oldGrant = grant
		}
	}
	assert.Equal(t, int64(1), oldGrant.IdentityGeneration)
	fresh := permissionFixtureResolver()
	fresh.ValidateSubject = func(context.Context, int64, model.ResourcePermissionSubject) (int64, error) {
		return generation.Load(), nil
	}
	actor := permissionActor(1, 9, identity.TypeUser, identity.RoleNone)
	effective, err := s.EffectiveResourcePermission(ctx, ref, actor, fresh)
	require.NoError(t, err)
	assert.Equal(t, model.PermissionNone, effective.Permission, "late old write after cleanup cannot authorize the rejoined membership")
	_, err = s.ReplaceResourcePermissions(ctx, ref, late.doc.Version, []model.ResourcePermissionGrant{oldGrant}, fresh)
	assert.ErrorIs(t, err, store.ErrPermissionIdentity, "explicit stale-generation resubmission is refused")
	doc, err := s.ReplaceResourcePermissions(ctx, ref, late.doc.Version, []model.ResourcePermissionGrant{{Subject: creator, Permission: model.PermissionEdit}}, fresh)
	require.NoError(t, err)
	require.Len(t, doc.Items, 1)
	assert.Equal(t, int64(2), doc.Items[0].IdentityGeneration)
	require.NoError(t, s.CleanupResourcePermissionSubject(ctx, job))
	oldJob := job
	oldJob.JobID = "another-old-generation-cleanup"
	require.NoError(t, s.CleanupResourcePermissionSubject(ctx, oldJob))
	effective, err = s.EffectiveResourcePermission(ctx, ref, actor, fresh)
	require.NoError(t, err)
	assert.Equal(t, model.PermissionEdit, effective.Permission, "old cleanup cannot remove a legitimate newer incarnation")
	persisted, err := s.ResourcePermissions(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, doc.Version, persisted.Version, "no matched stale grants means no spurious ACL revision")
}

func TestResourcePermissionsRealCatalogMembershipRejoinGeneration(t *testing.T) {
	ctx := context.Background()
	s := permissionStore(t, 1)
	catalog, err := identity.Open(filepath.Join(t.TempDir(), "identity.db"))
	require.NoError(t, err)
	defer catalog.Close()
	_, err = catalog.BootstrapAdmin(ctx, identity.UserInput{Login: "admin", Name: "Admin", Password: "real-catalog-admin"})
	require.NoError(t, err)
	user, err := catalog.CreateUser(ctx, identity.UserInput{Login: "rejoin", Name: "Rejoined", Password: "real-catalog-member", OrgID: 1, Role: identity.RoleNone})
	require.NoError(t, err)
	membership, err := catalog.Membership(ctx, 1, user.ID)
	require.NoError(t, err)
	require.Positive(t, membership.Generation)
	creator := permissionSubject(model.PermissionUser, user.ID)
	resolver := permissionFixtureResolver()
	resolver.CurrentPrincipal = func(ctx context.Context, p identity.Principal) (identity.Principal, error) {
		return catalog.UserPrincipal(ctx, p.ID, p.OrgID)
	}
	resolver.ValidateSubject = func(ctx context.Context, orgID int64, subject model.ResourcePermissionSubject) (int64, error) {
		if subject.Kind != model.PermissionUser {
			return 0, identity.ErrForbidden
		}
		if _, err := catalog.UserPrincipal(ctx, subject.ID, orgID); err != nil {
			return 0, err
		}
		membership, err := catalog.Membership(ctx, orgID, subject.ID)
		return membership.Generation, err
	}
	ref := permissionRef(s, model.PermissionDashboard, "system")
	validated, resume := make(chan struct{}), make(chan struct{})
	delayed := resolver
	delayed.ValidateSubject = func(ctx context.Context, orgID int64, subject model.ResourcePermissionSubject) (int64, error) {
		generation, err := resolver.ValidateSubject(ctx, orgID, subject)
		close(validated)
		<-resume
		return generation, err
	}
	type result struct {
		doc model.ResourcePermissionDocument
		err error
	}
	done := make(chan result, 1)
	go func() {
		doc, err := s.InitializeResourcePermissions(ctx, ref, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults, Creator: &creator}, delayed)
		done <- result{doc, err}
	}()
	<-validated
	removing, err := catalog.RemoveOrganizationUser(ctx, 1, user.ID, membership.Version)
	require.NoError(t, err)
	job := model.ResourcePermissionCleanup{JobID: "real-membership-remove", OrgID: 1, Subject: creator, IdentityVersion: removing.Version, IdentityGeneration: removing.Generation}
	require.NoError(t, s.CleanupResourcePermissionSubject(ctx, job))
	require.NoError(t, catalog.CompleteMembershipRemoval(ctx, 1, user.ID, removing.Version))
	rejoined, err := catalog.AddOrganizationUser(ctx, 1, user.ID, identity.RoleNone)
	require.NoError(t, err)
	assert.Greater(t, rejoined.Generation, membership.Generation)
	close(resume)
	late := <-done
	require.NoError(t, late.err)
	effective, err := s.EffectiveResourcePermission(ctx, ref, permissionActor(1, user.ID, identity.TypeUser, identity.RoleNone), resolver)
	require.NoError(t, err)
	assert.Equal(t, model.PermissionNone, effective.Permission)
	doc, err := s.ReplaceResourcePermissions(ctx, ref, late.doc.Version, []model.ResourcePermissionGrant{{Subject: creator, Permission: model.PermissionView}}, resolver)
	require.NoError(t, err)
	require.Len(t, doc.Items, 1)
	assert.Equal(t, rejoined.Generation, doc.Items[0].IdentityGeneration)
	effective, err = s.EffectiveResourcePermission(ctx, ref, permissionActor(1, user.ID, identity.TypeUser, identity.RoleNone), resolver)
	require.NoError(t, err)
	assert.Equal(t, model.PermissionView, effective.Permission)
	changed, err := catalog.ChangeOrganizationUserRole(ctx, 1, user.ID, rejoined.Version, identity.RoleEditor)
	require.NoError(t, err)
	assert.Equal(t, rejoined.Generation, changed.Generation, "changing basic role is not a new membership")
	effective, err = s.EffectiveResourcePermission(ctx, ref, permissionActor(1, user.ID, identity.TypeUser, identity.RoleNone), resolver)
	require.NoError(t, err)
	assert.Equal(t, model.PermissionView, effective.Permission, "a valid direct grant survives role-version changes")
}
