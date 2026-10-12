package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func adminTestUser(t *testing.T, store *Store, login, email, name string, orgID int64, serverAdmin bool) User {
	t.Helper()
	user, err := store.CreateUser(context.Background(), UserInput{Login: login, Email: email, Name: name, Password: "profile-password-233", OrgID: orgID, Role: RoleViewer, IsServerAdmin: serverAdmin})
	require.NoError(t, err)
	return user
}

func TestUserProfilePartialFieldsDurableAndAuthenticationRechecks(t *testing.T) {
	ctx := context.Background()
	location := filepath.Join(t.TempDir(), "identity.db")
	store, err := Open(location)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	user := adminTestUser(t, store, "alice", "alice@example.test", "Alice", 1, false)
	_, session, key, err := store.Login(ctx, user.Login, "profile-password-233", 1, time.Hour, time.Hour)
	require.NoError(t, err)
	var format string
	var salt, hash []byte
	var credentialVersion int64
	require.NoError(t, store.db.QueryRow(`SELECT format,salt,hash,version FROM password_credentials WHERE user_id=?`, user.ID).Scan(&format, &salt, &hash, &credentialVersion))
	updated, err := store.UpdateUser(ctx, user.ID, user.Version, UserProfileUpdate{Name: serviceValue(" Alice Operations ")})
	require.NoError(t, err)
	require.Equal(t, "Alice Operations", updated.Name)
	require.Equal(t, user.Login, updated.Login)
	require.Equal(t, user.Email, updated.Email)
	require.Equal(t, user.ID, updated.ID)
	require.Equal(t, user.CurrentOrgID, updated.CurrentOrgID)
	require.Equal(t, user.CreatedAt, updated.CreatedAt)
	require.False(t, updated.Disabled)
	require.False(t, updated.IsServerAdmin)
	require.Greater(t, updated.UpdatedAt, user.UpdatedAt)
	unchanged, err := store.UpdateUser(ctx, user.ID, updated.Version, UserProfileUpdate{})
	require.NoError(t, err)
	require.Equal(t, updated, unchanged)
	updated, err = store.UpdateUser(ctx, user.ID, updated.Version, UserProfileUpdate{Name: serviceValue(""), Email: serviceValue("")})
	require.NoError(t, err)
	require.Empty(t, updated.Name)
	require.Empty(t, updated.Email)
	require.Equal(t, "alice", updated.Login)
	_, err = store.UserByIdentifier(ctx, "alice@example.test")
	require.ErrorIs(t, err, sql.ErrNoRows)
	// Capture a successfully verified password candidate before renaming. The
	// later principal check must reject it rather than finish a stale login.
	candidate, err := store.passwordCandidate(ctx, "alice", "profile-password-233")
	require.NoError(t, err)
	updated, err = store.UpdateUser(ctx, user.ID, updated.Version, UserProfileUpdate{Login: serviceValue(" BETA "), Email: serviceValue(" beta@example.test "), Name: serviceValue("Updated profile")})
	require.NoError(t, err)
	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = candidate.principal(ctx, tx, 1)
	require.ErrorIs(t, err, ErrUnauthenticated)
	require.NoError(t, tx.Rollback())
	_, err = store.AuthenticatePassword(ctx, "alice", "profile-password-233", 1)
	require.ErrorIs(t, err, ErrUnauthenticated)
	for _, alias := range []string{"beta", "BETA@example.test"} {
		principal, err := store.AuthenticatePassword(ctx, alias, "profile-password-233", 1)
		require.NoError(t, err)
		require.Equal(t, user.ID, principal.ID)
		require.Equal(t, updated.Login, principal.Login)
		require.Equal(t, RoleViewer, principal.Role)
	}
	principal, err := store.AuthenticateSession(ctx, key, 0)
	require.NoError(t, err)
	require.Equal(t, updated.Login, principal.Login)
	require.Equal(t, updated.Name, principal.Name)
	var revokedAt int64
	require.NoError(t, store.db.QueryRow(`SELECT revoked_at FROM browser_sessions WHERE id=?`, session.ID).Scan(&revokedAt))
	require.Zero(t, revokedAt, "an ordinary profile update keeps identity-bound sessions valid")
	var afterFormat string
	var afterSalt, afterHash []byte
	var afterCredentialVersion int64
	require.NoError(t, store.db.QueryRow(`SELECT format,salt,hash,version FROM password_credentials WHERE user_id=?`, user.ID).Scan(&afterFormat, &afterSalt, &afterHash, &afterCredentialVersion))
	require.Equal(t, format, afterFormat)
	require.Equal(t, salt, afterSalt)
	require.Equal(t, hash, afterHash)
	require.Equal(t, credentialVersion, afterCredentialVersion)
	require.NoError(t, store.Close())
	store, err = Open(location)
	require.NoError(t, err)
	restored, err := store.UserByIdentifier(ctx, " BETA@EXAMPLE.TEST ")
	require.NoError(t, err)
	require.Equal(t, updated, restored)
	principal, err = store.AuthenticateSession(ctx, key, 1)
	require.NoError(t, err)
	require.Equal(t, updated.Login, principal.Login)
	serialized, err := json.Marshal(restored)
	require.NoError(t, err)
	for _, private := range []string{"profile-password-233", key, `"password"`, `"hash"`, `"salt"`} {
		require.NotContains(t, string(serialized), private)
	}
}

func TestUserProfileUnifiedIdentifiersAndFailedWritesRollback(t *testing.T) {
	ctx := context.Background()
	store, _ := openServiceAccountTestStore(t)
	user := adminTestUser(t, store, "alice@example.test", "a@example.test", "Alice", 1, false)
	other := adminTestUser(t, store, "bob@example.test", "b@example.test", "Bob", 1, false)
	for _, update := range []UserProfileUpdate{
		{Login: serviceValue("BOB@example.test"), Name: serviceValue("Should roll back")},
		{Login: serviceValue("B@EXAMPLE.TEST")},
		{Email: serviceValue("BOB@EXAMPLE.TEST")},
		{Email: serviceValue("B@example.test")},
		{Login: serviceValue("new@example.test"), Email: serviceValue("b@example.test")},
	} {
		_, err := store.UpdateUser(ctx, user.ID, user.Version, update)
		require.ErrorIs(t, err, ErrAccountExists)
		current, err := store.User(ctx, user.ID)
		require.NoError(t, err)
		require.Equal(t, user, current)
		for _, alias := range []string{user.Login, user.Email} {
			owner, err := store.UserByIdentifier(ctx, alias)
			require.NoError(t, err)
			require.Equal(t, user.ID, owner.ID)
		}
	}
	_, err := store.UserByIdentifier(ctx, "new@example.test")
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = store.db.Exec(`CREATE TRIGGER reject_profile BEFORE UPDATE OF login ON users BEGIN SELECT RAISE(ABORT,'injected profile failure'); END`)
	require.NoError(t, err)
	_, err = store.UpdateUser(ctx, user.ID, user.Version, UserProfileUpdate{Login: serviceValue("new@example.test"), Email: serviceValue("")})
	require.Error(t, err)
	_, err = store.db.Exec(`DROP TRIGGER reject_profile`)
	require.NoError(t, err)
	owner, err := store.UserByIdentifier(ctx, user.Email)
	require.NoError(t, err)
	require.Equal(t, user, owner)
	_, err = store.UserByIdentifier(ctx, "new@example.test")
	require.ErrorIs(t, err, sql.ErrNoRows)
	// A user can swap two of their own aliases without a transient collision.
	updated, err := store.UpdateUser(ctx, user.ID, user.Version, UserProfileUpdate{Login: &user.Email, Email: &user.Login})
	require.NoError(t, err)
	require.Equal(t, user.Email, updated.Login)
	require.Equal(t, user.Login, updated.Email)
	updated, err = store.UpdateUser(ctx, user.ID, updated.Version, UserProfileUpdate{Login: serviceValue("ALICE@EXAMPLE.TEST"), Email: serviceValue("alice@example.test")})
	require.NoError(t, err)
	var aliases int
	require.NoError(t, store.db.QueryRow(`SELECT count(*) FROM user_identifiers WHERE user_id=?`, user.ID).Scan(&aliases))
	require.Equal(t, 1, aliases, "one normalized authentication key is registered once")
	_, err = store.UserByIdentifier(ctx, "a@example.test")
	require.ErrorIs(t, err, sql.ErrNoRows)
	reclaimed := adminTestUser(t, store, "a@example.test", "", "Reclaimed alias", 1, false)
	require.NotEqual(t, user.ID, reclaimed.ID)
	for _, update := range []UserProfileUpdate{{Login: serviceValue("")}, {Login: serviceValue("bad\nlogin")}, {Name: serviceValue(string([]byte{0xff}))}, {Name: serviceValue(strings.Repeat("n", 257))}, {Email: serviceValue("Name <valid@example.test>")}, {Email: serviceValue("not-email")}} {
		_, err := store.UpdateUser(ctx, user.ID, updated.Version, update)
		require.Error(t, err)
	}
	_, err = store.UpdateUser(ctx, user.ID, user.Version, UserProfileUpdate{Name: serviceValue("Stale")})
	require.ErrorIs(t, err, ErrVersionConflict)
	_, err = store.UpdateUser(ctx, user.ID, 0, UserProfileUpdate{})
	require.ErrorIs(t, err, ErrVersionConflict)
	_, err = store.UpdateUser(ctx, 999, 1, UserProfileUpdate{})
	require.ErrorIs(t, err, sql.ErrNoRows)
	otherCurrent, err := store.UserByIdentifier(ctx, other.Email)
	require.NoError(t, err)
	require.Equal(t, other, otherCurrent)
	// EqualFold(sigma, final sigma) is true, but authentication lowercase keys
	// differ. Creation and updates must keep both actual keys addressable.
	greek := adminTestUser(t, store, "Σ@example.test", "ς@example.test", "Greek aliases", 1, false)
	for _, alias := range []string{"σ@example.test", "ς@example.test"} {
		owner, err := store.UserByIdentifier(ctx, alias)
		require.NoError(t, err)
		require.Equal(t, greek.ID, owner.ID)
	}
}

func TestUserServerAdminLastEnabledGuardAndCurrentSessionState(t *testing.T) {
	ctx := context.Background()
	location := filepath.Join(t.TempDir(), "identity.db")
	store, err := Open(location)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	admin, err := store.BootstrapAdmin(ctx, UserInput{Login: "admin", Password: "profile-password-233"})
	require.NoError(t, err)
	secondary := adminTestUser(t, store, "recovery", "", "Recovery", 1, false)
	_, _, secondaryKey, err := store.Login(ctx, secondary.Login, "profile-password-233", 1, time.Hour, time.Hour)
	require.NoError(t, err)
	_, _, adminKey, err := store.Login(ctx, admin.Login, "profile-password-233", 1, time.Hour, time.Hour)
	require.NoError(t, err)
	account, err := store.CreateServiceAccount(ctx, 1, ServiceAccountInput{Name: "Admin agent", Role: RoleAdmin})
	require.NoError(t, err)
	_, tokenKey, err := store.CreateServiceAccountToken(ctx, 1, account.ID, "Admin token", 0)
	require.NoError(t, err)
	_, err = store.SetUserServerAdmin(ctx, admin.ID, admin.Version, false)
	require.ErrorIs(t, err, ErrLastAdministrator, "service accounts and org Admin do not replace an instance administrator")
	unchanged, err := store.SetUserServerAdmin(ctx, admin.ID, admin.Version, true)
	require.NoError(t, err)
	require.Equal(t, admin, unchanged)
	secondary, err = store.SetUserServerAdmin(ctx, secondary.ID, secondary.Version, true)
	require.NoError(t, err)
	principal, err := store.AuthenticateSession(ctx, secondaryKey, 1)
	require.NoError(t, err)
	require.True(t, principal.IsServerAdmin)
	require.Equal(t, RoleViewer, principal.Role)
	_, err = store.SetUserServerAdmin(ctx, secondary.ID, secondary.Version-1, false)
	require.ErrorIs(t, err, ErrVersionConflict)
	admin, err = store.SetUserServerAdmin(ctx, admin.ID, admin.Version, false)
	require.NoError(t, err)
	principal, err = store.AuthenticateSession(ctx, adminKey, 1)
	require.NoError(t, err)
	require.False(t, principal.IsServerAdmin)
	require.Equal(t, RoleAdmin, principal.Role)
	dormant := adminTestUser(t, store, "dormant", "", "Dormant", 1, true)
	dormant, err = store.SetUserDisabled(ctx, dormant.ID, dormant.Version, true)
	require.NoError(t, err)
	_, err = store.SetUserServerAdmin(ctx, secondary.ID, secondary.Version, false)
	require.ErrorIs(t, err, ErrLastAdministrator, "a disabled instance admin does not count")
	_, err = store.SetUserDisabled(ctx, secondary.ID, secondary.Version, true)
	require.ErrorIs(t, err, ErrLastAdministrator, "disable and demote share the same guard")
	dormant, err = store.SetUserServerAdmin(ctx, dormant.ID, dormant.Version, false)
	require.NoError(t, err)
	require.True(t, dormant.Disabled)
	admin, err = store.SetUserServerAdmin(ctx, admin.ID, admin.Version, true)
	require.NoError(t, err)
	secondary, err = store.SetUserServerAdmin(ctx, secondary.ID, secondary.Version, false)
	require.NoError(t, err)
	principal, err = store.AuthenticateSession(ctx, secondaryKey, 1)
	require.NoError(t, err)
	require.False(t, principal.IsServerAdmin)
	require.Equal(t, RoleViewer, principal.Role)
	principal, err = store.AuthenticateServiceToken(ctx, tokenKey, 1)
	require.NoError(t, err)
	require.False(t, principal.IsServerAdmin)
	require.Equal(t, RoleAdmin, principal.Role)
	_, err = store.SetUserServerAdmin(ctx, admin.ID, 0, false)
	require.ErrorIs(t, err, ErrVersionConflict)
	_, err = store.SetUserServerAdmin(ctx, 999, 1, true)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.NoError(t, store.Close())
	store, err = Open(location)
	require.NoError(t, err)
	restored, err := store.User(ctx, admin.ID)
	require.NoError(t, err)
	require.Equal(t, admin, restored)
	principal, err = store.AuthenticateSession(ctx, secondaryKey, 1)
	require.NoError(t, err)
	require.False(t, principal.IsServerAdmin)
}

func TestUserServerAdminInactiveBackupsCannotAuthorizeDemotion(t *testing.T) {
	ctx := context.Background()
	store, org := openServiceAccountTestStore(t)
	active := adminTestUser(t, store, "active-admin", "", "Active admin", 1, true)
	backup := adminTestUser(t, store, "backup-admin", "", "Backup admin", org.ID, true)
	org, err := store.SetOrganizationState(ctx, org.ID, org.Version, OrganizationDisabled)
	require.NoError(t, err)
	_, err = store.AuthenticatePassword(ctx, backup.Login, "profile-password-233", org.ID)
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = store.SetUserServerAdmin(ctx, active.ID, active.Version, false)
	require.ErrorIs(t, err, ErrLastAdministrator, "an enabled flag in a disabled organization is not an available administrator")
	_, err = store.SetUserDisabled(ctx, active.ID, active.Version, true)
	require.ErrorIs(t, err, ErrLastAdministrator)
	org, err = store.SetOrganizationState(ctx, org.ID, org.Version, OrganizationActive)
	require.NoError(t, err)
	membership, err := store.Membership(ctx, org.ID, backup.ID)
	require.NoError(t, err)
	membership, err = store.RemoveOrganizationUser(ctx, org.ID, backup.ID, membership.Version)
	require.NoError(t, err)
	_, err = store.SetUserServerAdmin(ctx, active.ID, active.Version, false)
	require.ErrorIs(t, err, ErrLastAdministrator, "removing membership cannot be the remaining administrator's fallback")
	require.NoError(t, store.CompleteMembershipRemoval(ctx, org.ID, backup.ID, membership.Version))
	_, err = store.SetUserDisabled(ctx, active.ID, active.Version, true)
	require.ErrorIs(t, err, ErrLastAdministrator)
	_, err = store.AddOrganizationUser(ctx, 1, backup.ID, RoleNone)
	require.NoError(t, err)
	_, err = store.UserPrincipal(ctx, backup.ID, 1)
	require.NoError(t, err, "an active membership with RoleNone can authenticate a server admin")
	_, err = store.SetUserServerAdmin(ctx, active.ID, active.Version, false)
	require.NoError(t, err)
}

func TestUserGlobalSearchFiltersPaginationAndSafeDTOs(t *testing.T) {
	ctx := context.Background()
	store, org := openServiceAccountTestStore(t)
	admin := adminTestUser(t, store, "admin", "admin@example.test", "Instance administrator", 1, true)
	reader := adminTestUser(t, store, "reader", "reader@example.test", "Operations team", org.ID, false)
	literal := adminTestUser(t, store, "literal%_name", "", "Literal", 1, false)
	unicode := adminTestUser(t, store, "Ågent", "", "Unicode alias", 1, false)
	reader, err := store.SetUserDisabled(ctx, reader.ID, reader.Version, true)
	require.NoError(t, err)
	_, err = store.CreateServiceAccount(ctx, org.ID, ServiceAccountInput{Name: "Operations team", Role: RoleAdmin})
	require.NoError(t, err)
	page, err := store.SearchUsers(ctx, UserSearch{Query: "OPERATIONS"})
	require.NoError(t, err)
	require.Equal(t, []User{reader}, page.Users)
	require.EqualValues(t, 1, page.TotalCount)
	page, err = store.SearchUsers(ctx, UserSearch{Query: "%_"})
	require.NoError(t, err)
	require.Equal(t, []User{literal}, page.Users)
	page, err = store.SearchUsers(ctx, UserSearch{Query: "ÅG"})
	require.NoError(t, err)
	require.Equal(t, []User{unicode}, page.Users)
	page, err = store.SearchUsers(ctx, UserSearch{Disabled: serviceValue(true)})
	require.NoError(t, err)
	require.Equal(t, []User{reader}, page.Users)
	page, err = store.SearchUsers(ctx, UserSearch{ServerAdmin: serviceValue(true), Disabled: serviceValue(false)})
	require.NoError(t, err)
	require.Equal(t, []User{admin}, page.Users)
	page, err = store.SearchUsers(ctx, UserSearch{Page: 2, PerPage: 1})
	require.NoError(t, err)
	require.EqualValues(t, 4, page.TotalCount)
	require.Equal(t, []User{literal}, page.Users)
	page, err = store.SearchUsers(ctx, UserSearch{})
	require.NoError(t, err)
	require.Len(t, page.Users, 4)
	require.Equal(t, 1, page.Page)
	require.Equal(t, 1000, page.PerPage)
	serialized, err := json.Marshal(page)
	require.NoError(t, err)
	for _, private := range []string{`"password"`, `"token"`, `"hash"`, `"salt"`, "profile-password-233"} {
		require.NotContains(t, string(serialized), private)
	}
	page, err = store.SearchUsers(ctx, UserSearch{Page: 10, PerPage: 2})
	require.NoError(t, err)
	require.EqualValues(t, 4, page.TotalCount)
	require.Empty(t, page.Users)
	for _, query := range []UserSearch{{Page: -1}, {PerPage: 1001}, {Page: math.MaxInt, PerPage: 1000}, {Query: "bad\nquery"}, {Query: string([]byte{0xff})}} {
		_, err := store.SearchUsers(ctx, query)
		require.Error(t, err)
	}
	for _, identifier := range []string{"", "bad\nidentifier", string([]byte{0xff})} {
		_, err := store.UserByIdentifier(ctx, identifier)
		require.Error(t, err)
	}
	_, err = store.UserByIdentifier(ctx, "absent")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestUserAdministrationConcurrentCASAliasesAndLastAdmin(t *testing.T) {
	ctx := context.Background()
	store, _ := openServiceAccountTestStore(t)
	first := adminTestUser(t, store, "first", "", "First", 1, true)
	second := adminTestUser(t, store, "second", "", "Second", 1, true)
	run := func(writes []func() error, allowed error) {
		var wg sync.WaitGroup
		results := make(chan error, len(writes))
		for _, write := range writes {
			wg.Add(1)
			go func() { defer wg.Done(); results <- write() }()
		}
		wg.Wait()
		close(results)
		winners := 0
		for err := range results {
			if err == nil {
				winners++
			} else {
				require.ErrorIs(t, err, allowed)
			}
		}
		require.Equal(t, 1, winners)
	}
	var writes []func() error
	for range 8 {
		writes = append(writes, func() error {
			_, err := store.UpdateUser(ctx, first.ID, first.Version, UserProfileUpdate{Name: serviceValue("CAS winner")})
			return err
		})
	}
	run(writes, ErrVersionConflict)
	current, err := store.User(ctx, first.ID)
	require.NoError(t, err)
	require.EqualValues(t, first.Version+1, current.Version)
	require.Equal(t, "CAS winner", current.Name)
	first = current
	run([]func() error{
		func() error {
			_, err := store.UpdateUser(ctx, first.ID, first.Version, UserProfileUpdate{Login: serviceValue("shared@example.test")})
			return err
		},
		func() error {
			_, err := store.UpdateUser(ctx, second.ID, second.Version, UserProfileUpdate{Email: serviceValue("shared@example.test")})
			return err
		},
	}, ErrAccountExists)
	owner, err := store.UserByIdentifier(ctx, "SHARED@EXAMPLE.TEST")
	require.NoError(t, err)
	require.True(t, owner.ID == first.ID || owner.ID == second.ID)
	first, err = store.User(ctx, first.ID)
	require.NoError(t, err)
	second, err = store.User(ctx, second.ID)
	require.NoError(t, err)
	run([]func() error{
		func() error { _, err := store.SetUserServerAdmin(ctx, first.ID, first.Version, false); return err },
		func() error { _, err := store.SetUserServerAdmin(ctx, second.ID, second.Version, false); return err },
	}, ErrLastAdministrator)
	var count int
	require.NoError(t, store.db.QueryRow(`SELECT count(*) FROM users WHERE is_server_admin=1 AND disabled=0`).Scan(&count))
	require.Equal(t, 1, count)
	for _, id := range []int64{first.ID, second.ID} {
		user, err := store.User(ctx, id)
		require.NoError(t, err)
		if !user.IsServerAdmin {
			_, err := store.SetUserServerAdmin(ctx, id, user.Version, true)
			require.NoError(t, err)
		}
	}
	first, err = store.User(ctx, first.ID)
	require.NoError(t, err)
	second, err = store.User(ctx, second.ID)
	require.NoError(t, err)
	run([]func() error{
		func() error { _, err := store.SetUserDisabled(ctx, first.ID, first.Version, true); return err },
		func() error { _, err := store.SetUserServerAdmin(ctx, second.ID, second.Version, false); return err },
	}, ErrLastAdministrator)
	require.NoError(t, store.db.QueryRow(`SELECT count(*) FROM users WHERE is_server_admin=1 AND disabled=0`).Scan(&count))
	require.Equal(t, 1, count)
	for _, baseline := range []User{first, second} {
		current, err := store.User(ctx, baseline.ID)
		require.NoError(t, err)
		if current.Version == baseline.Version {
			continue
		}
		_, err = store.UpdateUser(ctx, baseline.ID, baseline.Version, UserProfileUpdate{Name: serviceValue("Stale after administrative change")})
		require.ErrorIs(t, err, ErrVersionConflict)
		after, err := store.User(ctx, baseline.ID)
		require.NoError(t, err)
		require.Equal(t, current, after)
	}
}

func TestUserLifecycleMembershipRemovalPreservesAvailableAdmin(t *testing.T) {
	for _, role := range []Role{RoleViewer, RoleNone} {
		t.Run(string(role), func(t *testing.T) {
			ctx := context.Background()
			store, org := openServiceAccountTestStore(t)
			first := adminTestUser(t, store, "first-admin", "", "First", 1, true)
			last := adminTestUser(t, store, "last-admin", "", "Last", org.ID, true)
			membership, err := store.Membership(ctx, org.ID, last.ID)
			require.NoError(t, err)
			if role != membership.Role {
				membership, err = store.ChangeOrganizationUserRole(ctx, org.ID, last.ID, membership.Version, role)
				require.NoError(t, err)
			}
			first, err = store.SetUserServerAdmin(ctx, first.ID, first.Version, false)
			require.NoError(t, err)
			_, session, key, err := store.Login(ctx, last.Login, "profile-password-233", org.ID, time.Hour, time.Hour)
			require.NoError(t, err)
			_, err = store.RemoveOrganizationUser(ctx, org.ID, last.ID, membership.Version)
			require.ErrorIs(t, err, ErrLastAdministrator, "removing this Viewer/None membership would strand the only available instance administrator")
			unchanged, err := store.Membership(ctx, org.ID, last.ID)
			require.NoError(t, err)
			require.Equal(t, membership, unchanged)
			principal, err := store.AuthenticateSession(ctx, key, org.ID)
			require.NoError(t, err)
			require.True(t, principal.IsServerAdmin)
			require.Equal(t, role, principal.Role)
			var revokedAt int64
			require.NoError(t, store.db.QueryRow(`SELECT revoked_at FROM browser_sessions WHERE id=?`, session.ID).Scan(&revokedAt))
			require.Zero(t, revokedAt)
			_, err = store.SetUserServerAdmin(ctx, first.ID, first.Version, true)
			require.NoError(t, err)
			removing, err := store.RemoveOrganizationUser(ctx, org.ID, last.ID, membership.Version)
			require.NoError(t, err)
			require.Equal(t, "removing", removing.State)
			_, err = store.AuthenticateSession(ctx, key, org.ID)
			require.ErrorIs(t, err, ErrUnauthenticated)
		})
	}
}

func TestUserLifecycleOrganizationTransitionPreservesAvailableAdmin(t *testing.T) {
	for _, state := range []OrganizationState{OrganizationDisabled, OrganizationDeleting} {
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			store, org := openServiceAccountTestStore(t)
			first := adminTestUser(t, store, "first-admin", "", "First", 1, true)
			last := adminTestUser(t, store, "last-admin", "", "Last", org.ID, true)
			_, err := store.SetUserServerAdmin(ctx, first.ID, first.Version, false)
			require.NoError(t, err)
			_, err = store.SetOrganizationState(ctx, org.ID, org.Version, state)
			require.ErrorIs(t, err, ErrLastAdministrator, "the only available human administrator cannot lose their last active organization")
			unchanged, err := store.Organization(ctx, org.ID)
			require.NoError(t, err)
			require.Equal(t, org, unchanged)
			principal, err := store.AuthenticatePassword(ctx, last.Login, "profile-password-233", org.ID)
			require.NoError(t, err)
			require.True(t, principal.IsServerAdmin)
			// The same administrator has another valid organization, so this
			// transition is safe without requiring a different human account.
			_, err = store.AddOrganizationUser(ctx, 1, last.ID, RoleNone)
			require.NoError(t, err)
			changed, err := store.SetOrganizationState(ctx, org.ID, org.Version, state)
			require.NoError(t, err)
			require.Equal(t, state, changed.State)
			principal, err = store.AuthenticatePassword(ctx, last.Login, "profile-password-233", 1)
			require.NoError(t, err)
			require.True(t, principal.IsServerAdmin)
			require.Equal(t, RoleNone, principal.Role)
			_, err = store.UserPrincipal(ctx, last.ID, org.ID)
			require.ErrorIs(t, err, ErrUnauthenticated)
			_, err = store.SetOrganizationState(ctx, 1, 1, OrganizationDisabled)
			require.ErrorIs(t, err, ErrPrimaryOrganization)
		})
	}
}

func TestUserLifecycleSingleHumanOtherOrganizationSurvivesRemoval(t *testing.T) {
	ctx := context.Background()
	store, org := openServiceAccountTestStore(t)
	admin := adminTestUser(t, store, "only-human", "", "Only human", org.ID, true)
	membership, err := store.Membership(ctx, org.ID, admin.ID)
	require.NoError(t, err)
	other, err := store.AddOrganizationUser(ctx, 1, admin.ID, RoleNone)
	require.NoError(t, err)
	removing, err := store.RemoveOrganizationUser(ctx, org.ID, admin.ID, membership.Version)
	require.NoError(t, err, "exclude only the removed relationship, not the whole human")
	require.Equal(t, "removing", removing.State)
	principal, err := store.AuthenticatePassword(ctx, admin.Login, "profile-password-233", 1)
	require.NoError(t, err)
	require.True(t, principal.IsServerAdmin)
	require.Equal(t, RoleNone, principal.Role)
	_, err = store.RemoveOrganizationUser(ctx, 1, admin.ID, other.Version)
	require.ErrorIs(t, err, ErrLastAdministrator)
	_, err = store.ChangeOrganizationUserRole(ctx, 1, admin.ID, other.Version, RoleViewer)
	require.NoError(t, err, "changing a basic role does not revoke global administration availability")
}

func TestUserLifecycleOrdinaryAndOrganizationRolesStayIndependent(t *testing.T) {
	ctx := context.Background()
	store, org := openServiceAccountTestStore(t)
	viewer := adminTestUser(t, store, "ordinary", "", "Ordinary", org.ID, false)
	membership, err := store.Membership(ctx, org.ID, viewer.ID)
	require.NoError(t, err)
	_, err = store.RemoveOrganizationUser(ctx, org.ID, viewer.ID, membership.Version)
	require.NoError(t, err, "an adminless catalog must not block ordinary member cleanup")
	orgAdmin, err := store.CreateUser(ctx, UserInput{Login: "org-admin", Password: "profile-password-233", OrgID: org.ID, Role: RoleAdmin})
	require.NoError(t, err)
	membership, err = store.Membership(ctx, org.ID, orgAdmin.ID)
	require.NoError(t, err)
	_, err = store.RemoveOrganizationUser(ctx, org.ID, orgAdmin.ID, membership.Version)
	require.ErrorIs(t, err, ErrLastAdministrator, "the pre-existing last organization Admin rule remains enforced")
	_, err = store.ChangeOrganizationUserRole(ctx, org.ID, orgAdmin.ID, membership.Version, RoleNone)
	require.ErrorIs(t, err, ErrLastAdministrator)
	instance := adminTestUser(t, store, "instance-viewer", "", "Instance admin", 1, true)
	_, err = store.RemoveOrganizationUser(ctx, org.ID, orgAdmin.ID, membership.Version)
	require.ErrorIs(t, err, ErrLastAdministrator, "an instance administrator is not an organization role replacement")
	second, err := store.CreateUser(ctx, UserInput{Login: "second-org-admin", Password: "profile-password-233", OrgID: org.ID, Role: RoleAdmin})
	require.NoError(t, err)
	_, err = store.RemoveOrganizationUser(ctx, org.ID, orgAdmin.ID, membership.Version)
	require.NoError(t, err)
	principal, err := store.UserPrincipal(ctx, second.ID, org.ID)
	require.NoError(t, err)
	require.Equal(t, RoleAdmin, principal.Role)
	require.False(t, principal.IsServerAdmin)
	_, err = store.SetOrganizationState(ctx, org.ID, org.Version, OrganizationDisabled)
	require.NoError(t, err, "the unrelated instance administrator still has an active organization")
	principal, err = store.UserPrincipal(ctx, instance.ID, 1)
	require.NoError(t, err)
	require.Equal(t, RoleViewer, principal.Role)
	require.True(t, principal.IsServerAdmin)
}

func TestUserLifecycleAdminlessAndUnavailableCleanupRemainAllowed(t *testing.T) {
	ctx := context.Background()
	store, org := openServiceAccountTestStore(t)
	// Pure organization fixtures do not acquire an implicit human admin policy.
	disabled, err := store.SetOrganizationState(ctx, org.ID, org.Version, OrganizationDisabled)
	require.NoError(t, err)
	_, err = store.SetOrganizationState(ctx, org.ID, disabled.Version, OrganizationDeleting)
	require.NoError(t, err)
	creating, err := store.BeginOrganization(ctx, "Interrupted initialization")
	require.NoError(t, err)
	_, err = store.SetOrganizationState(ctx, creating.ID, creating.Version, OrganizationDeleting)
	require.NoError(t, err)
	other, err := store.BeginOrganization(ctx, "Unavailable organization")
	require.NoError(t, err)
	other, err = store.ActivateOrganization(ctx, other.ID, other.Version)
	require.NoError(t, err)
	backup := adminTestUser(t, store, "disabled-human", "", "Disabled", other.ID, true)
	main := adminTestUser(t, store, "available-human", "", "Available", 1, true)
	backup, err = store.SetUserDisabled(ctx, backup.ID, backup.Version, true)
	require.NoError(t, err)
	membership, err := store.Membership(ctx, other.ID, backup.ID)
	require.NoError(t, err)
	_, err = store.RemoveOrganizationUser(ctx, other.ID, backup.ID, membership.Version)
	require.NoError(t, err, "removing an already unavailable human leaves current availability unchanged")
	other, err = store.SetOrganizationState(ctx, other.ID, other.Version, OrganizationDisabled)
	require.NoError(t, err)
	_, err = store.SetOrganizationState(ctx, other.ID, other.Version, OrganizationDeleting)
	require.NoError(t, err, "already disabled organization cleanup does not remove an available admin")
	principal, err := store.UserPrincipal(ctx, main.ID, 1)
	require.NoError(t, err)
	require.True(t, principal.IsServerAdmin)
}

func TestUserLifecycleConcurrentTransitionsAndRestartKeepAnAvailableAdmin(t *testing.T) {
	for _, kind := range []string{"remove/remove", "remove/disable", "disable/delete"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			location := filepath.Join(t.TempDir(), "identity.db")
			store, err := Open(location)
			require.NoError(t, err)
			defer func() { require.NoError(t, store.Close()) }()
			newOrg := func(name string) Organization {
				org, err := store.BeginOrganization(ctx, name)
				require.NoError(t, err)
				org, err = store.ActivateOrganization(ctx, org.ID, org.Version)
				require.NoError(t, err)
				return org
			}
			first, second := newOrg("First"), newOrg("Second")
			admin := adminTestUser(t, store, "only-human", "", "Only human", first.ID, true)
			firstMember, err := store.Membership(ctx, first.ID, admin.ID)
			require.NoError(t, err)
			secondMember, err := store.AddOrganizationUser(ctx, second.ID, admin.ID, RoleNone)
			require.NoError(t, err)
			// Even an organization Admin service account cannot be the last
			// available global human administrator after these transitions.
			_, err = store.CreateServiceAccount(ctx, 1, ServiceAccountInput{Name: "Agent", Role: RoleAdmin})
			require.NoError(t, err)
			writes := []func() error{
				func() error {
					_, err := store.RemoveOrganizationUser(ctx, first.ID, admin.ID, firstMember.Version)
					return err
				},
				func() error {
					_, err := store.RemoveOrganizationUser(ctx, second.ID, admin.ID, secondMember.Version)
					return err
				},
			}
			if kind == "remove/disable" {
				writes[1] = func() error {
					_, err := store.SetOrganizationState(ctx, second.ID, second.Version, OrganizationDisabled)
					return err
				}
			}
			if kind == "disable/delete" {
				writes[0] = func() error {
					_, err := store.SetOrganizationState(ctx, first.ID, first.Version, OrganizationDisabled)
					return err
				}
				writes[1] = func() error {
					_, err := store.SetOrganizationState(ctx, second.ID, second.Version, OrganizationDeleting)
					return err
				}
			}
			type outcome struct {
				index int
				err   error
			}
			var wg sync.WaitGroup
			results := make(chan outcome, len(writes))
			for index, write := range writes {
				wg.Add(1)
				go func() { defer wg.Done(); results <- outcome{index, write()} }()
			}
			wg.Wait()
			close(results)
			winners, loser := 0, -1
			for result := range results {
				if result.err == nil {
					winners++
				} else {
					require.ErrorIs(t, result.err, ErrLastAdministrator)
					loser = result.index
				}
			}
			require.Equal(t, 1, winners)
			require.GreaterOrEqual(t, loser, 0)
			assertAvailable := func() {
				available := 0
				for _, org := range []Organization{first, second} {
					principal, err := store.UserPrincipal(ctx, admin.ID, org.ID)
					if err == nil {
						available++
						require.True(t, principal.IsServerAdmin)
						principal, err = store.AuthenticatePassword(ctx, admin.Login, "profile-password-233", org.ID)
						require.NoError(t, err)
						require.True(t, principal.IsServerAdmin)
					} else {
						require.ErrorIs(t, err, ErrUnauthenticated)
					}
				}
				require.Equal(t, 1, available)
			}
			assertAvailable()
			require.NoError(t, store.Close())
			store, err = Open(location)
			require.NoError(t, err)
			assertAvailable()
			require.ErrorIs(t, writes[loser](), ErrLastAdministrator, "the failed target revision and protected availability survive restart")
		})
	}
}

func TestUserMembershipVersionsFenceOldDraftsAndCleanupAfterRejoin(t *testing.T) {
	for _, operation := range []string{"role", "remove", "complete", "highest-prior-version"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			store, _ := openServiceAccountTestStore(t)
			user := adminTestUser(t, store, "viewer", "", "Viewer", 1, false)
			first, err := store.Membership(ctx, 1, user.ID)
			require.NoError(t, err)
			current := first
			if operation == "highest-prior-version" {
				for _, role := range []Role{RoleEditor, RoleViewer, RoleNone, RoleViewer} {
					current, err = store.ChangeOrganizationUserRole(ctx, 1, user.ID, current.Version, role)
					require.NoError(t, err)
				}
			}
			removed, err := store.RemoveOrganizationUser(ctx, 1, user.ID, current.Version)
			require.NoError(t, err)
			require.NoError(t, store.CompleteMembershipRemoval(ctx, 1, user.ID, removed.Version))
			second, err := store.AddOrganizationUser(ctx, 1, user.ID, RoleViewer)
			require.NoError(t, err)
			require.Greater(t, second.Generation, first.Generation)
			switch operation {
			case "role":
				_, err = store.ChangeOrganizationUserRole(ctx, 1, user.ID, first.Version, RoleEditor)
				require.ErrorIs(t, err, ErrVersionConflict)
			case "remove":
				_, err = store.RemoveOrganizationUser(ctx, 1, user.ID, first.Version)
				require.ErrorIs(t, err, ErrVersionConflict)
			case "complete":
				second, err = store.RemoveOrganizationUser(ctx, 1, user.ID, second.Version)
				require.NoError(t, err)
				require.ErrorIs(t, store.CompleteMembershipRemoval(ctx, 1, user.ID, removed.Version), ErrVersionConflict, "both lives can be removing, but the old cleanup must not finalize the new one")
			case "highest-prior-version":
				require.Greater(t, second.Version, removed.Version, "new version must exceed every prior role/removal revision, not just the generation number")
			}
			unchanged, err := store.Membership(ctx, 1, user.ID)
			require.NoError(t, err)
			require.Equal(t, second, unchanged)
		})
	}
}

func TestUserMembershipMonotonicVersionsConcurrentCleanupAndRestart(t *testing.T) {
	ctx := context.Background()
	location := filepath.Join(t.TempDir(), "identity.db")
	store, err := Open(location)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	user := adminTestUser(t, store, "viewer", "", "Viewer", 1, false)
	first, err := store.Membership(ctx, 1, user.ID)
	require.NoError(t, err)
	current := first
	for _, role := range []Role{RoleEditor, RoleViewer} {
		current, err = store.ChangeOrganizationUserRole(ctx, 1, user.ID, current.Version, role)
		require.NoError(t, err)
		require.Equal(t, first.Generation, current.Generation)
	}
	removed, err := store.RemoveOrganizationUser(ctx, 1, user.ID, current.Version)
	require.NoError(t, err)
	require.NoError(t, store.CompleteMembershipRemoval(ctx, 1, user.ID, removed.Version))
	run := func(write func() error, conflict error) {
		var wg sync.WaitGroup
		results := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() { defer wg.Done(); results <- write() }()
		}
		wg.Wait()
		close(results)
		winners := 0
		for err := range results {
			if err == nil {
				winners++
			} else {
				require.ErrorIs(t, err, conflict)
			}
		}
		require.Equal(t, 1, winners)
	}
	run(func() error { _, err := store.AddOrganizationUser(ctx, 1, user.ID, RoleViewer); return err }, ErrAccountExists)
	second, err := store.Membership(ctx, 1, user.ID)
	require.NoError(t, err)
	require.EqualValues(t, first.Generation+1, second.Generation, "duplicate writers must not consume another generation")
	require.EqualValues(t, removed.Version+1, second.Version)
	run(func() error {
		_, err := store.ChangeOrganizationUserRole(ctx, 1, user.ID, second.Version, RoleEditor)
		return err
	}, ErrVersionConflict)
	second, err = store.Membership(ctx, 1, user.ID)
	require.NoError(t, err)
	require.EqualValues(t, removed.Version+2, second.Version)
	require.EqualValues(t, first.Generation+1, second.Generation)
	second, err = store.RemoveOrganizationUser(ctx, 1, user.ID, second.Version)
	require.NoError(t, err)
	run(func() error { return store.CompleteMembershipRemoval(ctx, 1, user.ID, second.Version) }, ErrVersionConflict)
	require.NoError(t, store.Close())
	store, err = Open(location)
	require.NoError(t, err)
	third, err := store.AddOrganizationUser(ctx, 1, user.ID, RoleViewer)
	require.NoError(t, err)
	require.EqualValues(t, second.Generation+1, third.Generation)
	require.EqualValues(t, second.Version+1, third.Version)
	_, err = store.ChangeOrganizationUserRole(ctx, 1, user.ID, first.Version, RoleEditor)
	require.ErrorIs(t, err, ErrVersionConflict)
	_, err = store.RemoveOrganizationUser(ctx, 1, user.ID, second.Version)
	require.ErrorIs(t, err, ErrVersionConflict)
	third, err = store.RemoveOrganizationUser(ctx, 1, user.ID, third.Version)
	require.NoError(t, err)
	require.ErrorIs(t, store.CompleteMembershipRemoval(ctx, 1, user.ID, second.Version), ErrVersionConflict)
	unchanged, err := store.Membership(ctx, 1, user.ID)
	require.NoError(t, err)
	require.Equal(t, third, unchanged)
	require.NoError(t, store.CompleteMembershipRemoval(ctx, 1, user.ID, third.Version))
	var lastVersion int64
	require.NoError(t, store.db.QueryRow(`SELECT last_version FROM membership_generations WHERE org_id=1 AND user_id=?`, user.ID).Scan(&lastVersion))
	require.Equal(t, third.Version, lastVersion)
}

func TestUserMembershipFinalVersionAndRemovalAreAtomic(t *testing.T) {
	ctx := context.Background()
	store, _ := openServiceAccountTestStore(t)
	user := adminTestUser(t, store, "viewer", "", "Viewer", 1, false)
	membership, err := store.Membership(ctx, 1, user.ID)
	require.NoError(t, err)
	team, err := store.CreateTeam(ctx, 1, TeamInput{Name: "Operations"})
	require.NoError(t, err)
	_, err = store.AddTeamMember(ctx, 1, team.ID, team.Version, TeamPrincipal{Kind: TypeUser, ID: user.ID}, TeamMemberRole)
	require.NoError(t, err)
	membership, err = store.RemoveOrganizationUser(ctx, 1, user.ID, membership.Version)
	require.NoError(t, err)
	var beforeVersion int64
	require.NoError(t, store.db.QueryRow(`SELECT last_version FROM membership_generations WHERE org_id=1 AND user_id=?`, user.ID).Scan(&beforeVersion))
	_, err = store.db.Exec(`CREATE TRIGGER reject_member_cleanup BEFORE DELETE ON organization_memberships BEGIN SELECT RAISE(ABORT,'injected membership cleanup failure'); END`)
	require.NoError(t, err)
	require.Error(t, store.CompleteMembershipRemoval(ctx, 1, user.ID, membership.Version))
	unchanged, err := store.Membership(ctx, 1, user.ID)
	require.NoError(t, err)
	require.Equal(t, membership, unchanged)
	var afterVersion int64
	require.NoError(t, store.db.QueryRow(`SELECT last_version FROM membership_generations WHERE org_id=1 AND user_id=?`, user.ID).Scan(&afterVersion))
	require.Equal(t, beforeVersion, afterVersion, "ledger write rolled back with failed physical removal")
	var members int
	require.NoError(t, store.db.QueryRow(`SELECT count(*) FROM team_members WHERE org_id=1 AND user_id=?`, user.ID).Scan(&members))
	require.Equal(t, 1, members)
	_, err = store.db.Exec(`DROP TRIGGER reject_member_cleanup`)
	require.NoError(t, err)
	require.NoError(t, store.CompleteMembershipRemoval(ctx, 1, user.ID, membership.Version))
	require.NoError(t, store.db.QueryRow(`SELECT last_version FROM membership_generations WHERE org_id=1 AND user_id=?`, user.ID).Scan(&afterVersion))
	require.Equal(t, membership.Version, afterVersion)
	_, err = store.AddOrganizationUser(ctx, 1, user.ID, RoleViewer)
	require.NoError(t, err)
	teamTestNoGrant(t, store, 1, TeamPrincipal{Kind: TypeUser, ID: user.ID})
}
