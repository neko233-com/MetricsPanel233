package instance_test

import (
	"context"
	"errors"
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

type permissionCatalogFixture struct {
	catalog  *identity.Store
	control  *store.Store
	admin    identity.User
	orgID    int64
	internal identity.Principal
	resolver store.PermissionIdentityResolver
}

func newPermissionCatalogFixture(t *testing.T, child bool) *permissionCatalogFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	catalog, err := identity.Open(filepath.Join(dir, "catalog.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = catalog.Close() })
	admin, err := catalog.BootstrapAdmin(ctx, identity.UserInput{Login: "admin", Password: "test-admin-password-233"})
	require.NoError(t, err)
	orgID := int64(1)
	if child {
		org, err := catalog.BeginOrganization(ctx, "Child")
		require.NoError(t, err)
		org, err = catalog.ActivateOrganization(ctx, org.ID, org.Version)
		require.NoError(t, err)
		orgID = org.ID
	}
	control, err := store.OpenForOrganization(filepath.Join(dir, "control.db"), orgID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = control.DB.Close() })
	internal := identity.BackgroundPrincipal(orgID)
	resolver, err := instance.NewPermissionIdentityResolver(catalog, instance.PermissionResolverOptions{TrustedInternalPrincipal: &internal})
	require.NoError(t, err)
	return &permissionCatalogFixture{catalog: catalog, control: control, admin: admin, orgID: orgID, internal: internal, resolver: resolver}
}

func (f *permissionCatalogFixture) internalContext() context.Context {
	return identity.WithPrincipal(context.Background(), f.internal)
}

func (f *permissionCatalogFixture) resource(t *testing.T, id string, grants ...model.ResourcePermissionGrant) model.PermissionResource {
	t.Helper()
	ctx := f.internalContext()
	_, err := f.control.SaveDashboard(ctx, model.Dashboard{ID: id, Name: id})
	require.NoError(t, err)
	ref := model.PermissionResource{OrgID: f.orgID, Kind: model.PermissionDashboard, UID: id}
	doc, err := f.control.InitializeResourcePermissions(ctx, ref, model.ResourcePermissionSeed{Policy: model.PermissionRootDefaults}, f.resolver)
	require.NoError(t, err)
	_, err = f.control.ReplaceResourcePermissions(ctx, ref, doc.Version, grants, f.resolver)
	require.NoError(t, err)
	return ref
}

func (f *permissionCatalogFixture) user(t *testing.T, login string, role identity.Role) identity.User {
	t.Helper()
	user, err := f.catalog.CreateUser(context.Background(), identity.UserInput{Login: login, Name: "Original name", Password: "test-member-password-233", OrgID: f.orgID, Role: role})
	require.NoError(t, err)
	return user
}

func (f *permissionCatalogFixture) session(t *testing.T, user identity.User) (identity.Principal, identity.Session, store.PermissionIdentityResolver) {
	t.Helper()
	p, session, key, err := f.catalog.Login(context.Background(), user.Login, "test-member-password-233", f.orgID, time.Hour, 30*time.Minute)
	require.NoError(t, err)
	resolver, err := instance.NewPermissionIdentityResolver(f.catalog, instance.PermissionResolverOptions{Revalidate: func(ctx context.Context, incoming identity.Principal) (identity.Principal, error) {
		return f.catalog.AuthenticateSession(ctx, key, incoming.OrgID)
	}})
	require.NoError(t, err)
	return p, session, resolver
}

func requireEffective(t *testing.T, f *permissionCatalogFixture, ref model.PermissionResource, p identity.Principal, resolver store.PermissionIdentityResolver, want model.ResourcePermissionLevel) {
	t.Helper()
	effective, err := f.control.EffectiveResourcePermission(identity.WithPrincipal(context.Background(), p), ref, p, resolver)
	require.NoError(t, err)
	require.Equal(t, want, effective.Permission)
}

func permissionGrant(kind model.PermissionSubjectKind, id int64, level model.ResourcePermissionLevel) model.ResourcePermissionGrant {
	return model.ResourcePermissionGrant{Subject: model.ResourcePermissionSubject{Kind: kind, ID: id}, Permission: level}
}

func TestPermissionCatalogResolverRequiresExplicitTrust(t *testing.T) {
	f := newPermissionCatalogFixture(t, false)
	canonical := identity.LocalPrincipal()
	callback := func(_ context.Context, p identity.Principal) (identity.Principal, error) { return p, nil }
	for _, opts := range []instance.PermissionResolverOptions{
		{}, {Revalidate: callback, TrustedInternalPrincipal: &canonical},
	} {
		_, err := instance.NewPermissionIdentityResolver(f.catalog, opts)
		require.Error(t, err)
	}
	_, err := instance.NewPermissionIdentityResolver(nil, instance.PermissionResolverOptions{Revalidate: callback})
	require.Error(t, err)
	for _, p := range []identity.Principal{
		{OrgID: 1, Type: identity.TypeSystem, Login: "request", Role: identity.RoleAdmin},
		{OrgID: 1, ID: 9, Type: identity.TypeLocal, Login: "request", Role: identity.RoleAdmin},
		{OrgID: 1, ID: 9, Type: identity.TypeUser, Login: "request", Role: identity.RoleAdmin},
	} {
		_, err := instance.NewPermissionIdentityResolver(f.catalog, instance.PermissionResolverOptions{TrustedInternalPrincipal: &p})
		require.Error(t, err)
	}
	var calls atomic.Int64
	httpResolver, err := instance.NewPermissionIdentityResolver(f.catalog, instance.PermissionResolverOptions{Revalidate: func(_ context.Context, p identity.Principal) (identity.Principal, error) {
		calls.Add(1)
		return p, nil
	}})
	require.NoError(t, err)
	ref := f.resource(t, "trust")
	// A JSON-constructed type/role never bypasses CurrentPrincipal in the store.
	for _, p := range []identity.Principal{identity.LocalPrincipal(), identity.BackgroundPrincipal(1)} {
		_, err := f.control.EffectiveResourcePermission(identity.WithPrincipal(context.Background(), p), ref, p, httpResolver)
		require.ErrorIs(t, err, identity.ErrForbidden)
	}
	require.Zero(t, calls.Load(), "internal types cannot enter a credential-authenticated resolver")
	localResolver, err := instance.NewPermissionIdentityResolver(f.catalog, instance.PermissionResolverOptions{TrustedInternalPrincipal: &canonical})
	require.NoError(t, err)
	canonical.Role = identity.RoleNone // Constructor captured a copy, not this mutable pointer.
	p := identity.LocalPrincipal()
	requireEffective(t, f, ref, p, localResolver, model.PermissionAdmin)
	_, err = localResolver.CurrentPrincipal(context.Background(), p)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	changed := p
	changed.Name = "changed"
	_, err = localResolver.CurrentPrincipal(identity.WithPrincipal(context.Background(), changed), p)
	require.ErrorIs(t, err, identity.ErrForbidden)
	_, err = localResolver.CurrentPrincipal(identity.WithPrincipal(context.Background(), p), identity.BackgroundPrincipal(1))
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	_, err = f.control.EffectiveResourcePermission(f.internalContext(), ref, f.internal, store.PermissionIdentityResolver{})
	require.ErrorIs(t, err, store.ErrPermissionIdentity)
}

func TestPermissionCatalogResolverLiveHumanFieldsAndExactRole(t *testing.T) {
	f := newPermissionCatalogFixture(t, false)
	u := f.user(t, "human", identity.RoleNone)
	p, session, resolver := f.session(t, u)
	direct := f.resource(t, "direct", permissionGrant(model.PermissionUser, u.ID, model.PermissionEdit))
	roles := f.resource(t, "roles",
		model.ResourcePermissionGrant{Subject: model.ResourcePermissionSubject{Kind: model.PermissionRole, Role: "Viewer"}, Permission: model.PermissionView},
		model.ResourcePermissionGrant{Subject: model.ResourcePermissionSubject{Kind: model.PermissionRole, Role: "Editor"}, Permission: model.PermissionEdit})
	// Stale/forged incoming role and server-admin metadata cannot enlarge grants.
	p.Role, p.IsServerAdmin, p.Name = identity.RoleAdmin, true, "Stale name"
	ctx := identity.WithPrincipal(context.Background(), p)
	fresh, err := resolver.CurrentPrincipal(ctx, p)
	require.NoError(t, err)
	require.Equal(t, identity.RoleNone, fresh.Role)
	require.False(t, fresh.IsServerAdmin)
	require.Equal(t, u.Name, fresh.Name)
	requireEffective(t, f, direct, p, resolver, model.PermissionEdit)
	requireEffective(t, f, roles, p, resolver, model.PermissionNone)
	member, err := f.catalog.Membership(ctx, 1, u.ID)
	require.NoError(t, err)
	generation := member.Generation
	for _, role := range []identity.Role{identity.RoleViewer, identity.RoleEditor, identity.RoleNone} {
		oldVersion := member.Version
		member, err = f.catalog.ChangeOrganizationUserRole(ctx, 1, u.ID, member.Version, role)
		require.NoError(t, err)
		_, err = f.catalog.ChangeOrganizationUserRole(ctx, 1, u.ID, oldVersion, identity.RoleAdmin)
		require.ErrorIs(t, err, identity.ErrVersionConflict)
		require.Equal(t, generation, member.Generation)
		want := model.PermissionNone
		if role == identity.RoleViewer {
			want = model.PermissionView
		}
		if role == identity.RoleEditor {
			want = model.PermissionEdit
		}
		requireEffective(t, f, roles, p, resolver, want)
		requireEffective(t, f, direct, p, resolver, model.PermissionEdit)
	}
	name := "Live renamed human"
	u, err = f.catalog.UpdateUser(ctx, u.ID, u.Version, identity.UserProfileUpdate{Name: &name})
	require.NoError(t, err)
	fresh, err = resolver.CurrentPrincipal(ctx, p)
	require.NoError(t, err)
	require.Equal(t, name, fresh.Name)
	require.NoError(t, f.catalog.RevokeSession(ctx, u.ID, session.ID))
	_, err = f.control.EffectiveResourcePermission(ctx, direct, p, resolver)
	require.ErrorIs(t, err, identity.ErrUnauthenticated, "catalog membership alone cannot replace live credential revocation")
}

func TestPermissionCatalogResolverRejoinGenerationAndDisabledHuman(t *testing.T) {
	f := newPermissionCatalogFixture(t, false)
	u := f.user(t, "rejoin", identity.RoleNone)
	p, _, resolver := f.session(t, u)
	ref := f.resource(t, "rejoin", permissionGrant(model.PermissionUser, u.ID, model.PermissionEdit))
	requireEffective(t, f, ref, p, resolver, model.PermissionEdit)
	ctx := context.Background()
	member, err := f.catalog.Membership(ctx, f.orgID, u.ID)
	require.NoError(t, err)
	firstGeneration := member.Generation
	removing, err := f.catalog.RemoveOrganizationUser(ctx, f.orgID, u.ID, member.Version)
	require.NoError(t, err)
	_, err = f.resolver.ValidateSubject(f.internalContext(), f.orgID, model.ResourcePermissionSubject{Kind: model.PermissionUser, ID: u.ID})
	require.Error(t, err)
	_, err = resolver.CurrentPrincipal(identity.WithPrincipal(ctx, p), p)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	// Omit ACL cleanup deliberately: generation matching must itself fail closed.
	require.NoError(t, f.catalog.CompleteMembershipRemoval(ctx, f.orgID, u.ID, removing.Version))
	member, err = f.catalog.AddOrganizationUser(ctx, f.orgID, u.ID, identity.RoleNone)
	require.NoError(t, err)
	require.Greater(t, member.Generation, firstGeneration)
	p, _, resolver = f.session(t, u)
	requireEffective(t, f, ref, p, resolver, model.PermissionNone)
	doc, err := f.control.ResourcePermissions(ctx, ref)
	require.NoError(t, err)
	_, err = f.control.ReplaceResourcePermissions(f.internalContext(), ref, doc.Version, []model.ResourcePermissionGrant{
		{Subject: model.ResourcePermissionSubject{Kind: model.PermissionUser, ID: u.ID}, Permission: model.PermissionEdit, IdentityGeneration: firstGeneration},
	}, f.resolver)
	require.ErrorIs(t, err, store.ErrPermissionIdentity)
	doc, err = f.control.ReplaceResourcePermissions(f.internalContext(), ref, doc.Version, []model.ResourcePermissionGrant{permissionGrant(model.PermissionUser, u.ID, model.PermissionEdit)}, f.resolver)
	require.NoError(t, err)
	require.Equal(t, member.Generation, doc.Items[0].IdentityGeneration)
	requireEffective(t, f, ref, p, resolver, model.PermissionEdit)
	u, err = f.catalog.SetUserDisabled(ctx, u.ID, u.Version, true)
	require.NoError(t, err)
	_, err = resolver.CurrentPrincipal(identity.WithPrincipal(ctx, p), p)
	require.Error(t, err)
	_, err = f.resolver.ValidateSubject(f.internalContext(), f.orgID, model.ResourcePermissionSubject{Kind: model.PermissionUser, ID: u.ID})
	require.Error(t, err)
	u, err = f.catalog.SetUserDisabled(ctx, u.ID, u.Version, false)
	require.NoError(t, err)
	p, _, resolver = f.session(t, u)
	requireEffective(t, f, ref, p, resolver, model.PermissionEdit)
}

func TestPermissionCatalogResolverTypedServiceAccountsAndLiveTeams(t *testing.T) {
	f := newPermissionCatalogFixture(t, false)
	u := f.user(t, "typed", identity.RoleNone)
	human, _, humanResolver := f.session(t, u)
	ctx := context.Background()
	_, err := f.catalog.CreateServiceAccount(ctx, 1, identity.ServiceAccountInput{Name: "first account", Role: identity.RoleNone})
	require.NoError(t, err)
	sa, err := f.catalog.CreateServiceAccount(ctx, 1, identity.ServiceAccountInput{Name: "typed account", Role: identity.RoleNone})
	require.NoError(t, err)
	require.Equal(t, u.ID, sa.ID, "independent ID sequences intentionally collide")
	token, key, err := f.catalog.CreateServiceAccountToken(ctx, 1, sa.ID, "fixture", 0)
	require.NoError(t, err)
	account, err := f.catalog.AuthenticateServiceToken(ctx, key, 1)
	require.NoError(t, err)
	accountResolver, err := instance.NewPermissionIdentityResolver(f.catalog, instance.PermissionResolverOptions{Revalidate: func(ctx context.Context, incoming identity.Principal) (identity.Principal, error) {
		return f.catalog.AuthenticateServiceToken(ctx, key, incoming.OrgID)
	}})
	require.NoError(t, err)
	team, err := f.catalog.CreateTeam(ctx, 1, identity.TeamInput{Name: "Readers"})
	require.NoError(t, err)
	humanMember, err := f.catalog.AddTeamMember(ctx, 1, team.ID, team.Version, identity.TeamPrincipal{Kind: identity.TypeUser, ID: u.ID}, identity.TeamMemberRole)
	require.NoError(t, err)
	team, err = f.catalog.Team(ctx, 1, team.ID)
	require.NoError(t, err)
	_, err = f.catalog.AddTeamMember(ctx, 1, team.ID, team.Version, identity.TeamPrincipal{Kind: identity.TypeServiceAccount, ID: sa.ID}, identity.TeamAdminRole)
	require.NoError(t, err)
	ref := f.resource(t, "typed", permissionGrant(model.PermissionUser, u.ID, model.PermissionEdit), permissionGrant(model.PermissionServiceAccount, sa.ID, model.PermissionView))
	teamRef := f.resource(t, "team", permissionGrant(model.PermissionTeam, team.ID, model.PermissionEdit))
	requireEffective(t, f, ref, human, humanResolver, model.PermissionEdit)
	requireEffective(t, f, ref, account, accountResolver, model.PermissionView)
	requireEffective(t, f, teamRef, human, humanResolver, model.PermissionEdit)
	requireEffective(t, f, teamRef, account, accountResolver, model.PermissionEdit)
	_, err = humanResolver.TeamsForPrincipal(identity.WithPrincipal(ctx, human), 1, model.ResourcePermissionSubject{Kind: model.PermissionServiceAccount, ID: sa.ID})
	require.ErrorIs(t, err, identity.ErrForbidden, "same numeric ID is not the same typed principal")
	require.NoError(t, f.catalog.RemoveTeamMember(ctx, 1, team.ID, humanMember.Version, humanMember.Principal))
	requireEffective(t, f, teamRef, human, humanResolver, model.PermissionNone)
	requireEffective(t, f, teamRef, account, accountResolver, model.PermissionEdit)
	team, err = f.catalog.Team(ctx, 1, team.ID)
	require.NoError(t, err)
	_, err = f.catalog.DeleteTeam(ctx, 1, team.ID, team.Version)
	require.NoError(t, err)
	requireEffective(t, f, teamRef, account, accountResolver, model.PermissionNone)
	_, err = f.resolver.ValidateSubject(f.internalContext(), 1, model.ResourcePermissionSubject{Kind: model.PermissionTeam, ID: team.ID})
	require.Error(t, err)
	name, role := "Live account name", identity.RoleEditor
	oldVersion := sa.Version
	sa, err = f.catalog.UpdateServiceAccount(ctx, 1, sa.ID, sa.Version, identity.ServiceAccountUpdate{Name: &name, Role: &role})
	require.NoError(t, err)
	_, err = f.catalog.UpdateServiceAccount(ctx, 1, sa.ID, oldVersion, identity.ServiceAccountUpdate{Name: &name})
	require.ErrorIs(t, err, identity.ErrVersionConflict)
	account.Role, account.Name = identity.RoleAdmin, "Stale"
	fresh, err := accountResolver.CurrentPrincipal(identity.WithPrincipal(ctx, account), account)
	require.NoError(t, err)
	require.Equal(t, role, fresh.Role)
	require.Equal(t, name, fresh.Name)
	require.False(t, fresh.IsServerAdmin)
	requireEffective(t, f, ref, account, accountResolver, model.PermissionView)
	disabled := true
	sa, err = f.catalog.UpdateServiceAccount(ctx, 1, sa.ID, sa.Version, identity.ServiceAccountUpdate{Disabled: &disabled})
	require.NoError(t, err)
	_, err = accountResolver.CurrentPrincipal(identity.WithPrincipal(ctx, account), account)
	require.Error(t, err)
	_, err = f.resolver.ValidateSubject(f.internalContext(), 1, model.ResourcePermissionSubject{Kind: model.PermissionServiceAccount, ID: sa.ID})
	require.Error(t, err)
	disabled = false
	_, err = f.catalog.UpdateServiceAccount(ctx, 1, sa.ID, sa.Version, identity.ServiceAccountUpdate{Disabled: &disabled})
	require.NoError(t, err)
	requireEffective(t, f, ref, account, accountResolver, model.PermissionView)
	require.NoError(t, f.catalog.RevokeServiceAccountToken(ctx, 1, sa.ID, token.ID))
	_, err = f.control.EffectiveResourcePermission(identity.WithPrincipal(ctx, account), ref, account, accountResolver)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
}

func TestPermissionCatalogResolverOrganizationAndCredentialBinding(t *testing.T) {
	f := newPermissionCatalogFixture(t, true)
	u := f.user(t, "child-reader", identity.RoleNone)
	p, _, resolver := f.session(t, u)
	ref := f.resource(t, "org-local", permissionGrant(model.PermissionUser, u.ID, model.PermissionView))
	ctx := identity.WithPrincipal(context.Background(), p)
	requireEffective(t, f, ref, p, resolver, model.PermissionView)
	_, err := resolver.CurrentPrincipal(context.Background(), p)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	foreign := p
	foreign.OrgID = 1
	_, err = resolver.CurrentPrincipal(identity.WithPrincipal(context.Background(), foreign), p)
	require.ErrorIs(t, err, identity.ErrUnauthenticated)
	_, err = resolver.ValidateSubject(ctx, 1, model.ResourcePermissionSubject{Kind: model.PermissionUser, ID: u.ID})
	require.ErrorIs(t, err, identity.ErrForbidden)
	_, err = resolver.TeamsForPrincipal(ctx, f.orgID, model.ResourcePermissionSubject{Kind: model.PermissionUser, ID: f.admin.ID})
	require.ErrorIs(t, err, identity.ErrForbidden)
	for _, change := range []func(identity.Principal) identity.Principal{
		func(p identity.Principal) identity.Principal { p.ID++; return p },
		func(p identity.Principal) identity.Principal { p.OrgID = 1; return p },
		func(p identity.Principal) identity.Principal { p.Type = identity.TypeServiceAccount; return p },
		func(p identity.Principal) identity.Principal { p.Role = "unknown"; return p },
	} {
		wrongResolver, err := instance.NewPermissionIdentityResolver(f.catalog, instance.PermissionResolverOptions{Revalidate: func(_ context.Context, incoming identity.Principal) (identity.Principal, error) {
			return change(incoming), nil
		}})
		require.NoError(t, err)
		_, err = wrongResolver.CurrentPrincipal(ctx, p)
		require.ErrorIs(t, err, identity.ErrUnauthenticated)
	}
	credentialError := errors.New("request credential revoked")
	revoked, err := instance.NewPermissionIdentityResolver(f.catalog, instance.PermissionResolverOptions{Revalidate: func(context.Context, identity.Principal) (identity.Principal, error) {
		return identity.Principal{}, credentialError
	}})
	require.NoError(t, err)
	_, err = revoked.CurrentPrincipal(ctx, p)
	require.ErrorIs(t, err, credentialError)
	org, err := f.catalog.Organization(ctx, f.orgID)
	require.NoError(t, err)
	org, err = f.catalog.SetOrganizationState(ctx, org.ID, org.Version, identity.OrganizationDisabled)
	require.NoError(t, err)
	_, err = resolver.CurrentPrincipal(ctx, p)
	require.Error(t, err)
	_, err = f.resolver.CurrentPrincipal(f.internalContext(), f.internal)
	require.ErrorIs(t, err, instance.ErrUnavailable)
	org, err = f.catalog.SetOrganizationState(ctx, org.ID, org.Version, identity.OrganizationActive)
	require.NoError(t, err)
	requireEffective(t, f, ref, p, resolver, model.PermissionView)
	_, err = f.catalog.SetOrganizationState(ctx, org.ID, org.Version, identity.OrganizationDeleting)
	require.NoError(t, err)
	_, err = resolver.CurrentPrincipal(ctx, p)
	require.Error(t, err)
	_, err = f.resolver.ValidateSubject(f.internalContext(), f.orgID, model.ResourcePermissionSubject{Kind: model.PermissionUser, ID: u.ID})
	require.ErrorIs(t, err, instance.ErrUnavailable)
	missing := identity.BackgroundPrincipal(999)
	missingResolver, err := instance.NewPermissionIdentityResolver(f.catalog, instance.PermissionResolverOptions{TrustedInternalPrincipal: &missing})
	require.NoError(t, err)
	_, err = missingResolver.CurrentPrincipal(identity.WithPrincipal(context.Background(), missing), missing)
	require.Error(t, err)
	cancelled, cancel := context.WithCancel(f.internalContext())
	cancel()
	_, err = f.resolver.CurrentPrincipal(cancelled, f.internal)
	require.ErrorIs(t, err, context.Canceled)
}

func TestPermissionCatalogResolverCallbacksOutsideControlTransactions(t *testing.T) {
	f := newPermissionCatalogFixture(t, false)
	u := f.user(t, "concurrent", identity.RoleViewer)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, _, key, err := f.catalog.Login(ctx, u.Login, "test-member-password-233", 1, time.Hour, 30*time.Minute)
	require.NoError(t, err)
	ref := f.resource(t, "concurrent",
		model.ResourcePermissionGrant{Subject: model.ResourcePermissionSubject{Kind: model.PermissionRole, Role: "Viewer"}, Permission: model.PermissionView},
		model.ResourcePermissionGrant{Subject: model.ResourcePermissionSubject{Kind: model.PermissionRole, Role: "Editor"}, Permission: model.PermissionEdit})
	f.control.DB.SetMaxOpenConns(1)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var once atomic.Bool
	resolver, err := instance.NewPermissionIdentityResolver(f.catalog, instance.PermissionResolverOptions{Revalidate: func(ctx context.Context, incoming identity.Principal) (identity.Principal, error) {
		// Reads both independent databases with the control pool limited to one
		// connection. Calling this while holding its transaction would deadlock.
		if _, err := f.control.ResourcePermissions(ctx, ref); err != nil {
			return identity.Principal{}, err
		}
		if _, err := f.catalog.Membership(ctx, incoming.OrgID, incoming.ID); err != nil {
			return identity.Principal{}, err
		}
		if once.CompareAndSwap(false, true) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return identity.Principal{}, ctx.Err()
			}
		}
		return f.catalog.AuthenticateSession(ctx, key, incoming.OrgID)
	}})
	require.NoError(t, err)
	type result struct {
		permission model.ResourcePermissionLevel
		err        error
	}
	done := make(chan result, 1)
	go func() {
		effective, err := f.control.EffectiveResourcePermission(identity.WithPrincipal(ctx, p), ref, p, resolver)
		done <- result{effective.Permission, err}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("credential callback could not read both databases")
	}
	_, err = f.control.SaveDashboard(ctx, model.Dashboard{ID: "parallel-write", Name: "parallel-write"})
	require.NoError(t, err, "permission callback must not hold a control DB lock")
	member, err := f.catalog.Membership(ctx, 1, u.ID)
	require.NoError(t, err)
	_, err = f.catalog.ChangeOrganizationUserRole(ctx, 1, u.ID, member.Version, identity.RoleEditor)
	require.NoError(t, err, "permission callback must not hold a catalog DB lock")
	close(release)
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.Equal(t, model.PermissionEdit, got.permission, "the role changed before credential revalidation completed")
	case <-ctx.Done():
		t.Fatal("permission evaluation deadlocked")
	}
	doc, err := f.control.ResourcePermissions(ctx, ref)
	require.NoError(t, err)
	_, err = f.control.ReplaceResourcePermissions(identity.WithPrincipal(ctx, p), ref, doc.Version, []model.ResourcePermissionGrant{permissionGrant(model.PermissionUser, u.ID, model.PermissionView)}, resolver)
	require.NoError(t, err, "grant validation callback must also run before the writer transaction")
}

func TestPermissionCatalogResolverRestartRetainsLiveMembershipGeneration(t *testing.T) {
	f := newPermissionCatalogFixture(t, false)
	u := f.user(t, "durable", identity.RoleNone)
	p, _, key, err := f.catalog.Login(context.Background(), u.Login, "test-member-password-233", 1, time.Hour, 30*time.Minute)
	require.NoError(t, err)
	ref := f.resource(t, "durable", permissionGrant(model.PermissionUser, u.ID, model.PermissionEdit))
	original, err := f.control.ResourcePermissions(context.Background(), ref)
	require.NoError(t, err)
	catalogPath, controlPath := f.catalog.Path(), f.control.Path
	require.NotEqual(t, catalogPath, controlPath)
	require.NoError(t, f.catalog.Close())
	require.NoError(t, f.control.DB.Close())
	catalog, err := identity.Open(catalogPath)
	require.NoError(t, err)
	defer catalog.Close()
	control, err := store.Open(controlPath)
	require.NoError(t, err)
	defer control.DB.Close()
	restored, err := control.ResourcePermissions(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, original, restored)
	resolver, err := instance.NewPermissionIdentityResolver(catalog, instance.PermissionResolverOptions{Revalidate: func(ctx context.Context, incoming identity.Principal) (identity.Principal, error) {
		return catalog.AuthenticateSession(ctx, key, incoming.OrgID)
	}})
	require.NoError(t, err)
	effective, err := control.EffectiveResourcePermission(identity.WithPrincipal(context.Background(), p), ref, p, resolver)
	require.NoError(t, err)
	require.Equal(t, model.PermissionEdit, effective.Permission)
	member, err := catalog.Membership(context.Background(), 1, u.ID)
	require.NoError(t, err)
	require.Equal(t, original.Items[0].IdentityGeneration, member.Generation)
}
