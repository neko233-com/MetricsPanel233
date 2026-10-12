package identity

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSessionRotationKeepsDeviceExpiryAndRevokesBothCredentials(t *testing.T) {
	ctx := context.Background()
	catalogPath := filepath.Join(t.TempDir(), "identity.db")
	catalog, err := Open(catalogPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, catalog.Close()) })
	user, err := catalog.BootstrapAdmin(ctx, UserInput{Login: "admin", Password: "session-password-233"})
	require.NoError(t, err)
	_, original, oldKey, err := catalog.Login(ctx, user.Login, "session-password-233", 1, time.Hour, time.Minute)
	require.NoError(t, err)
	_, skipped, unchanged, err := catalog.RotateSession(ctx, oldKey)
	require.NoError(t, err)
	require.Equal(t, oldKey, unchanged, "a freshly issued token is not rotated again within five seconds")
	require.Equal(t, original.ID, skipped.ID)
	_, err = catalog.db.Exec(`UPDATE browser_sessions SET rotated_at=? WHERE id=?`, time.Now().Add(-6*time.Second).UnixMilli(), original.ID)
	require.NoError(t, err)
	p, rotated, newKey, err := catalog.RotateSession(ctx, oldKey)
	require.NoError(t, err)
	require.NotEqual(t, oldKey, newKey)
	require.Equal(t, user.ID, p.ID)
	require.Equal(t, original.ID, rotated.ID)
	require.Equal(t, original.CreatedAt, rotated.CreatedAt)
	require.Equal(t, original.ExpiresAt, rotated.ExpiresAt)
	require.Equal(t, original.CurrentOrgID, rotated.CurrentOrgID)
	require.Equal(t, original.IdleTimeoutMS, rotated.IdleTimeoutMS)
	require.False(t, rotated.TokenSeen)
	// The preceding cookie remains usable until the replacement is observed.
	_, pending, err := catalog.AuthenticateSessionWithRotation(ctx, oldKey, 0, 10*time.Minute)
	require.NoError(t, err)
	require.False(t, pending.TokenSeen)
	_, seen, err := catalog.AuthenticateSessionWithRotation(ctx, newKey, 0, 10*time.Minute)
	require.NoError(t, err)
	require.True(t, seen.TokenSeen)
	_, _, err = catalog.AuthenticateSessionWithRotation(ctx, oldKey, 0, 10*time.Minute)
	require.ErrorIs(t, err, ErrSessionNeedsRotation, "a lost replacement must request rotation instead of authorizing an overdue previous cookie")
	// Once the replacement has been observed, rotating again discards the
	// original credential without adding another device/session row.
	_, err = catalog.db.Exec(`UPDATE browser_sessions SET rotated_at=? WHERE id=?`, time.Now().Add(-6*time.Second).UnixMilli(), original.ID)
	require.NoError(t, err)
	_, second, secondKey, err := catalog.RotateSession(ctx, newKey)
	require.NoError(t, err)
	require.NotEqual(t, newKey, secondKey)
	require.Equal(t, original.ExpiresAt, second.ExpiresAt)
	_, err = catalog.AuthenticateSession(ctx, oldKey, 0)
	require.ErrorIs(t, err, ErrUnauthenticated)
	sessions, err := catalog.Sessions(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	encoded, err := json.Marshal(sessions)
	require.NoError(t, err)
	for _, credential := range []string{oldKey, newKey, secondKey} {
		require.NotContains(t, string(encoded), credential)
		var leaked int
		require.NoError(t, catalog.db.QueryRow(`SELECT count(*) FROM browser_sessions WHERE cast(token_hash AS TEXT)=? OR cast(previous_token_hash AS TEXT)=?`, credential, credential).Scan(&leaked))
		require.Zero(t, leaked)
	}
	require.NoError(t, catalog.Close())
	catalog, err = Open(catalogPath)
	require.NoError(t, err)
	_, err = catalog.AuthenticateSession(ctx, secondKey, 0)
	require.NoError(t, err)
	_, err = catalog.AuthenticateSession(ctx, oldKey, 0)
	require.ErrorIs(t, err, ErrUnauthenticated)
	// Logging out through the still-valid previous cookie revokes the entire
	// device, including its replacement, and is idempotent after restart.
	require.NoError(t, catalog.Logout(ctx, newKey))
	require.NoError(t, catalog.Logout(ctx, newKey))
	for _, credential := range []string{newKey, secondKey} {
		_, _, _, err = catalog.RotateSession(ctx, credential)
		require.ErrorIs(t, err, ErrUnauthenticated)
		_, err = catalog.AuthenticateSession(ctx, credential, 0)
		require.ErrorIs(t, err, ErrUnauthenticated)
	}
}

func TestSessionRotationConcurrentRequestsAndCurrentAuthorization(t *testing.T) {
	ctx := context.Background()
	catalog, err := Open(filepath.Join(t.TempDir(), "identity.db"))
	require.NoError(t, err)
	defer catalog.Close()
	user, err := catalog.BootstrapAdmin(ctx, UserInput{Login: "admin", Password: "session-password-233"})
	require.NoError(t, err)
	_, session, key, err := catalog.Login(ctx, user.Login, "session-password-233", 1, time.Hour, time.Minute)
	require.NoError(t, err)
	_, err = catalog.db.Exec(`UPDATE browser_sessions SET rotated_at=? WHERE id=?`, time.Now().Add(-6*time.Second).UnixMilli(), session.ID)
	require.NoError(t, err)
	type result struct {
		key string
		err error
	}
	results := make(chan result, 8)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, _, rotated, err := catalog.RotateSession(ctx, key)
			results <- result{rotated, err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	var replacement string
	rotations := 0
	for result := range results {
		require.NoError(t, result.err)
		if result.key != key {
			rotations++
			replacement = result.key
		}
	}
	require.Equal(t, 1, rotations, "overlapping requests issue one replacement; the others leave their previous cookie unchanged")
	_, err = catalog.AuthenticateSession(ctx, replacement, 0)
	require.NoError(t, err)
	foreign, err := catalog.BeginOrganization(ctx, "Foreign")
	require.NoError(t, err)
	foreign, err = catalog.ActivateOrganization(ctx, foreign.ID, foreign.Version)
	require.NoError(t, err)
	_, _, err = catalog.AuthenticateSessionWithRotation(ctx, replacement, foreign.ID, 10*time.Minute)
	require.ErrorIs(t, err, ErrUnauthenticated)
	org, err := catalog.BeginOrganization(ctx, "Operations")
	require.NoError(t, err)
	org, err = catalog.ActivateOrganization(ctx, org.ID, org.Version)
	require.NoError(t, err)
	_, err = catalog.AddOrganizationUser(ctx, org.ID, user.ID, RoleAdmin)
	require.NoError(t, err)
	_, err = catalog.SwitchSessionOrganization(ctx, replacement, org.ID)
	require.NoError(t, err)
	_, err = catalog.db.Exec(`UPDATE browser_sessions SET rotated_at=? WHERE id=?`, time.Now().Add(-6*time.Second).UnixMilli(), session.ID)
	require.NoError(t, err)
	p, moved, movedKey, err := catalog.RotateSession(ctx, replacement)
	require.NoError(t, err)
	require.Equal(t, org.ID, p.OrgID)
	require.Equal(t, org.ID, moved.CurrentOrgID)
	org, err = catalog.SetOrganizationState(ctx, org.ID, org.Version, OrganizationDisabled)
	require.NoError(t, err)
	_, _, _, err = catalog.RotateSession(ctx, movedKey)
	require.ErrorIs(t, err, ErrUnauthenticated, "rotation cannot escape a disabled original organization")
	_, _, err = catalog.AuthenticateSessionWithRotation(ctx, movedKey, 1, 10*time.Minute)
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, err = catalog.SetOrganizationState(ctx, org.ID, org.Version, OrganizationActive)
	require.NoError(t, err)
	_, err = catalog.db.Exec(`UPDATE browser_sessions SET expires_at=? WHERE id=?`, time.Now().UnixMilli(), session.ID)
	require.NoError(t, err)
	_, _, _, err = catalog.RotateSession(ctx, movedKey)
	require.ErrorIs(t, err, ErrUnauthenticated)
	_, _, _, err = catalog.RotateSession(ctx, "mp233_session_not-a-credential")
	require.ErrorIs(t, err, ErrUnauthenticated)
}

func TestSessionRotationSchedulingAndSchemaThreeMigration(t *testing.T) {
	now := time.Unix(10000, 0)
	session := Session{RotatedAt: now.Add(-10 * time.Minute).UnixMilli(), TokenSeen: true}
	require.False(t, session.NeedsRotationAt(now, 10*time.Minute), "the tagged source uses a strict-before boundary")
	require.True(t, session.NeedsRotationAt(now.Add(time.Millisecond), 10*time.Minute))
	require.Equal(t, now.Add(-5*time.Second), session.NextRotation(10*time.Minute))
	session.TokenSeen = false
	session.RotatedAt = now.Add(-time.Minute).UnixMilli()
	require.False(t, session.NeedsRotationAt(now, 10*time.Minute))
	require.True(t, session.NeedsRotationAt(now.Add(time.Millisecond), 10*time.Minute))
	ctx := context.Background()
	catalogPath := filepath.Join(t.TempDir(), "identity.db")
	catalog, err := Open(catalogPath)
	require.NoError(t, err)
	user, err := catalog.BootstrapAdmin(ctx, UserInput{Login: "admin", Password: "session-password-233"})
	require.NoError(t, err)
	_, original, key, err := catalog.Login(ctx, user.Login, "session-password-233", 1, time.Hour, time.Minute)
	require.NoError(t, err)
	var originalDigest []byte
	require.NoError(t, catalog.db.QueryRow(`SELECT token_hash FROM browser_sessions WHERE id=?`, original.ID).Scan(&originalDigest))
	_, err = catalog.db.Exec(`ALTER TABLE browser_sessions DROP COLUMN previous_token_hash;
ALTER TABLE browser_sessions DROP COLUMN rotated_at;
ALTER TABLE browser_sessions DROP COLUMN token_seen;
UPDATE identity_schema SET version=3 WHERE id=1`)
	require.NoError(t, err)
	require.NoError(t, catalog.Close())
	catalog, err = Open(catalogPath)
	require.NoError(t, err)
	defer catalog.Close()
	var version int
	require.NoError(t, catalog.db.QueryRow(`SELECT version FROM identity_schema WHERE id=1`).Scan(&version))
	require.Equal(t, 6, version)
	var digest, previous []byte
	var migrated Session
	require.NoError(t, catalog.db.QueryRow(`SELECT token_hash,previous_token_hash FROM browser_sessions WHERE id=?`, original.ID).Scan(&digest, &previous))
	require.Equal(t, originalDigest, digest)
	require.Equal(t, digest, previous)
	migrated, err = scanSession(catalog.db.QueryRow(`SELECT `+sessionColumns+` FROM browser_sessions WHERE id=?`, original.ID))
	require.NoError(t, err)
	require.Equal(t, original.CreatedAt, migrated.RotatedAt)
	require.Equal(t, original.ExpiresAt, migrated.ExpiresAt)
	require.True(t, migrated.TokenSeen)
	p, _, err := catalog.AuthenticateSessionWithRotation(ctx, key, 0, 10*time.Minute)
	require.NoError(t, err)
	require.Equal(t, user.ID, p.ID)
	_, _, err = catalog.AuthenticateSessionWithRotation(ctx, key, 0, time.Second)
	require.Error(t, err)
}
