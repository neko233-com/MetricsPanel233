package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func adapterHuman(t *testing.T, s *Store, login string) (User, Principal, context.Context) {
	t.Helper()
	u := adminTestUser(t, s, login, "", login, 1, false)
	p, err := s.AuthenticatePassword(context.Background(), login, "profile-password-233", 1)
	require.NoError(t, err)
	return u, p, WithPrincipal(context.Background(), p)
}

func TestGrafanaCurrentProfileRealCatalogAndPrecision(t *testing.T) {
	s, org := openServiceAccountTestStore(t)
	u, p, ctx := adapterHuman(t, s, "profile")
	_, err := s.AddOrganizationUser(ctx, org.ID, u.ID, RoleNone)
	require.NoError(t, err)
	// Header-selected request context does not rewrite the persisted profile org.
	selected, err := s.AuthenticatePassword(ctx, u.Login, "profile-password-233", org.ID)
	require.NoError(t, err)
	profile, err := s.CurrentUserProfile(WithPrincipal(ctx, selected), selected)
	require.NoError(t, err)
	require.Equal(t, u.ID, profile.ID)
	require.Equal(t, u.UID, profile.UID)
	require.EqualValues(t, 1, profile.OrgID)
	require.Equal(t, time.UnixMilli(u.CreatedAt).UTC(), profile.CreatedAt)
	require.NotNil(t, profile.AuthLabels)
	_, err = s.UpdateUser(ctx, u.ID, u.Version, UserProfileUpdate{Name: serviceValue("Current name")})
	require.NoError(t, err)
	p.Name = "Stale name"
	p.IsServerAdmin = true
	profile, err = s.CurrentUserProfile(ctx, p)
	require.NoError(t, err)
	require.Equal(t, "Current name", profile.Name)
	require.False(t, profile.IsGrafanaAdmin)
	profile.ID = 9007199254740993
	raw, err := json.Marshal(profile)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"id":9007199254740993`)
	require.Contains(t, string(raw), `"authLabels":[]`)
	require.Contains(t, string(raw), profile.CreatedAt.Format(time.RFC3339Nano))
	for _, private := range []string{"profile-password-233", "current_org_id", "password", "hash", "version", "created_at"} {
		require.NotContains(t, string(raw), private)
	}
	var input RevokeAuthTokenInput
	require.NoError(t, json.Unmarshal([]byte(`{"authTokenId":9007199254740993}`), &input))
	require.EqualValues(t, 9007199254740993, input.AuthTokenID)
	for _, raw := range []string{`{"authTokenId":9223372036854775808}`, `{"authTokenId":1.5}`, `{"authTokenId":"1"}`} {
		require.Error(t, json.Unmarshal([]byte(raw), &input))
	}
}

func TestGrafanaCurrentAdaptersExactTypedAndLiveGuards(t *testing.T) {
	s, _ := openServiceAccountTestStore(t)
	u, p, ctx := adapterHuman(t, s, "guard")
	_, err := s.CurrentUserProfile(context.Background(), p)
	require.ErrorIs(t, err, ErrUnauthenticated)
	foreign := p
	foreign.ID++
	_, err = s.CurrentUserProfile(ctx, foreign)
	require.ErrorIs(t, err, ErrForbidden)
	foreign = p
	foreign.OrgID = 2
	_, err = s.CurrentUserProfile(ctx, foreign)
	require.ErrorIs(t, err, ErrForbidden)
	sa, err := s.CreateServiceAccount(ctx, 1, ServiceAccountInput{Name: "Same numeric ID", Role: RoleAdmin})
	require.NoError(t, err)
	require.Equal(t, u.ID, sa.ID)
	sp := Principal{Type: TypeServiceAccount, ID: sa.ID, OrgID: 1, Login: sa.Login, Role: sa.Role}
	sc := WithPrincipal(ctx, sp)
	_, err = s.CurrentUserProfile(sc, sp)
	require.ErrorIs(t, err, ErrForbidden)
	_, err = s.CurrentUserOrganizations(sc, sp)
	require.ErrorIs(t, err, ErrForbidden)
	_, err = s.CurrentUserAuthTokens(sc, sp, 0)
	require.ErrorIs(t, err, ErrForbidden)
	require.ErrorIs(t, s.RevokeCurrentUserAuthToken(sc, sp, 0, 1), ErrForbidden)
	_, err = s.SetUserDisabled(ctx, u.ID, u.Version, true)
	require.NoError(t, err)
	_, err = s.CurrentUserProfile(ctx, p)
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = s.CurrentUserOrganizations(ctx, p)
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = s.CurrentUserAuthTokens(ctx, p, 0)
	require.ErrorIs(t, err, ErrUnauthenticated)
}

func TestGrafanaCurrentOrganizationsLiveMemberships(t *testing.T) {
	s, org := openServiceAccountTestStore(t)
	u, p, ctx := adapterHuman(t, s, "orgs")
	m, err := s.AddOrganizationUser(ctx, org.ID, u.ID, RoleNone)
	require.NoError(t, err)
	items, err := s.CurrentUserOrganizations(ctx, p)
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, RoleNone, items[1].Role)
	_, err = s.RemoveOrganizationUser(ctx, org.ID, u.ID, m.Version)
	require.NoError(t, err)
	items, err = s.CurrentUserOrganizations(ctx, p)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, s.CompleteMembershipRemoval(ctx, org.ID, u.ID, m.Version+1))
	_, err = s.AddOrganizationUser(ctx, org.ID, u.ID, RoleViewer)
	require.NoError(t, err)
	_, err = s.SetOrganizationState(ctx, org.ID, org.Version, OrganizationDisabled)
	require.NoError(t, err)
	items, err = s.CurrentUserOrganizations(ctx, p)
	require.NoError(t, err)
	require.Len(t, items, 1)
}

func TestGrafanaCurrentAuthTokensOwnershipAtomicRevokeAndRestart(t *testing.T) {
	location := filepath.Join(t.TempDir(), "identity.db")
	s, err := Open(location)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	u, p, ctx := adapterHuman(t, s, "devices")
	_, _, otherCtx := adapterHuman(t, s, "other")
	login := func(login string) (Session, string) {
		_, v, key, err := s.LoginWithMetadata(ctx, login, "profile-password-233", 1, time.Hour, time.Hour, SessionMetadataInput{RemoteAddr: "127.0.0.1:4321", UserAgent: "raw-private-agent"})
		require.NoError(t, err)
		return v, key
	}
	active, key := login(u.Login)
	target, targetKey := login(u.Login)
	expired, _ := login(u.Login)
	revoked, _ := login(u.Login)
	foreign, _ := login("other")
	_, err = s.db.Exec(`UPDATE browser_sessions SET expires_at=1 WHERE id=?`, expired.ID)
	require.NoError(t, err)
	require.NoError(t, s.RevokeSession(ctx, u.ID, revoked.ID))
	items, err := s.CurrentUserAuthTokens(ctx, p, active.ID)
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, target.ID, items[0].ID)
	require.False(t, items[0].IsActive)
	require.True(t, items[1].IsActive)
	_, err = s.CurrentUserAuthTokens(ctx, p, foreign.ID)
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = s.CurrentUserAuthTokens(otherCtx, p, 0)
	require.ErrorIs(t, err, ErrForbidden)
	require.ErrorIs(t, s.RevokeCurrentUserAuthToken(ctx, p, active.ID, active.ID), ErrActiveAuthToken)
	for _, id := range []int64{foreign.ID, expired.ID, revoked.ID, 99999} {
		require.ErrorIs(t, s.RevokeCurrentUserAuthToken(ctx, p, active.ID, id), sql.ErrNoRows)
	}
	require.ErrorIs(t, s.RevokeCurrentUserAuthToken(ctx, p, active.ID, 0), ErrInvalidAuthToken)
	_, err = s.db.Exec(`CREATE TRIGGER fail_adapter_revoke BEFORE UPDATE OF revoked_at ON browser_sessions WHEN OLD.id=` + jsonInt(target.ID) + ` BEGIN SELECT RAISE(ABORT,'injected revoke failure'); END`)
	require.NoError(t, err)
	require.Error(t, s.RevokeCurrentUserAuthToken(ctx, p, active.ID, target.ID))
	_, err = s.AuthenticateSession(ctx, targetKey, 1)
	require.NoError(t, err)
	_, err = s.db.Exec(`DROP TRIGGER fail_adapter_revoke`)
	require.NoError(t, err)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.RevokeCurrentUserAuthToken(ctx, p, active.ID, target.ID) }()
	}
	wg.Wait()
	close(results)
	ok, missing := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, sql.ErrNoRows) {
			missing++
		} else {
			t.Fatal(err)
		}
	}
	require.Equal(t, 1, ok)
	require.Equal(t, 1, missing)
	_, err = s.AuthenticateSession(ctx, targetKey, 1)
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = s.AuthenticateSession(ctx, key, 1)
	require.NoError(t, err)
	raw, err := json.Marshal(items)
	require.NoError(t, err)
	for _, private := range []string{key, targetKey, "raw-private-agent", "password", "token_hash", "expires_at", "userId"} {
		require.NotContains(t, string(raw), private)
	}
	require.NoError(t, s.Close())
	s, err = Open(location)
	require.NoError(t, err)
	items, err = s.CurrentUserAuthTokens(ctx, p, 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.False(t, items[0].IsActive)
	_, err = s.AuthenticateSession(ctx, targetKey, 1)
	require.ErrorIs(t, err, ErrUnauthenticated)
}

func jsonInt(value int64) string { data, _ := json.Marshal(value); return string(data) }
