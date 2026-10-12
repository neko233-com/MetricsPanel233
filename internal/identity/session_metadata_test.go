package identity

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestSessionMetadataPeerOnlyAndBoundedUserAgent(t *testing.T) {
	r := httptest.NewRequest("POST", "http://localhost/login", nil)
	r.RemoteAddr = "[::ffff:192.0.2.25]:4321"
	r.Header.Set("Forwarded", "for=203.0.113.99")
	r.Header.Set("X-Forwarded-For", "203.0.113.98")
	r.Header.Set("X-Real-IP", "203.0.113.97")
	r.Header.Set("Authorization", "Bearer must-not-persist")
	r.Header.Set("Cookie", "secret=must-not-persist")
	r.Header.Set("User-Agent", "Windows Edge\x00\r\n"+strings.Repeat("汉", 600))
	metadata := normalizeSessionMetadata(SessionMetadataFromRequest(r))
	require.Equal(t, "192.0.2.25", metadata.clientIP)
	require.Equal(t, "password", metadata.authModule)
	require.True(t, utf8.ValidString(metadata.userAgent))
	require.LessOrEqual(t, len(metadata.userAgent), maxSessionUserAgentBytes)
	require.NotContains(t, metadata.userAgent, "\r")
	require.NotContains(t, metadata.userAgent, "\x00")
	require.NotContains(t, metadata.userAgent, "must-not-persist")
	for _, input := range []struct{ peer, expected string }{{"127.0.0.1:1234", "127.0.0.1"}, {"2001:db8::1", "2001:db8::1"}, {"[fe80::1%eth0]:80", "fe80::1"}, {"malicious.example:80", ""}, {"127.0.0.1:999999", ""}, {"", ""}} {
		require.Equal(t, input.expected, normalizeSessionMetadata(SessionMetadataInput{RemoteAddr: input.peer}).clientIP)
	}
	require.Empty(t, normalizeSessionMetadata(SessionMetadataInput{UserAgent: string([]byte{0xff, 0, 10})}).userAgent)
	require.Equal(t, SessionMetadataInput{}, SessionMetadataFromRequest(nil))
}

func TestSessionMetadataIssuanceRotationAndRestart(t *testing.T) {
	ctx := context.Background()
	location := filepath.Join(t.TempDir(), "identity.db")
	store, err := Open(location)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	user := adminTestUser(t, store, "alice", "", "Alice", 1, false)
	input := SessionMetadataInput{RemoteAddr: "192.0.2.10:1234", UserAgent: "Mozilla/5.0 Windows Edge desktop"}
	_, session, key, err := store.LoginWithMetadata(ctx, user.Login, "profile-password-233", 1, time.Hour, time.Hour, input)
	require.NoError(t, err)
	require.Equal(t, "192.0.2.10", session.ClientIP)
	require.Equal(t, input.UserAgent, session.UserAgent)
	require.Equal(t, "password", session.AuthModule)
	sessions, err := store.Sessions(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, []Session{session}, sessions)
	// Make ordinary rotation due without changing absolute/idle expiration.
	_, err = store.db.Exec(`UPDATE browser_sessions SET rotated_at=? WHERE id=?`, time.Now().Add(-10*time.Second).UnixMilli(), session.ID)
	require.NoError(t, err)
	_, rotated, replacement, err := store.RotateSession(ctx, key)
	require.NoError(t, err)
	require.NotEqual(t, key, replacement)
	require.Equal(t, session.ID, rotated.ID)
	require.Equal(t, session.ExpiresAt, rotated.ExpiresAt)
	require.Equal(t, session.IdleTimeoutMS, rotated.IdleTimeoutMS)
	require.Equal(t, session.ClientIP, rotated.ClientIP)
	require.Equal(t, session.UserAgent, rotated.UserAgent)
	require.Equal(t, session.AuthModule, rotated.AuthModule)
	require.NoError(t, store.Close())
	store, err = Open(location)
	require.NoError(t, err)
	sessions, err = store.Sessions(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, []Session{rotated}, sessions)
	serialized, err := json.Marshal(sessions)
	require.NoError(t, err)
	for _, private := range []string{key, replacement, "profile-password-233", "token_hash", "previous_token_hash"} {
		require.NotContains(t, string(serialized), private)
	}
	principal, err := store.AuthenticateSession(ctx, replacement, 1)
	require.NoError(t, err)
	require.Equal(t, user.ID, principal.ID)
	_, _, _, err = store.LoginWithMetadata(ctx, user.Login, "wrong-password", 1, time.Hour, time.Hour, input)
	require.ErrorIs(t, err, ErrUnauthenticated)
	sessions, err = store.Sessions(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
}

func TestSessionMetadataFailureIsAtomicWithCredentialIssuance(t *testing.T) {
	ctx := context.Background()
	store, _ := openServiceAccountTestStore(t)
	user := adminTestUser(t, store, "alice", "", "Alice", 1, false)
	_, err := store.db.Exec(`CREATE TRIGGER reject_session_metadata BEFORE INSERT ON browser_sessions WHEN NEW.client_ip='192.0.2.10' BEGIN SELECT RAISE(ABORT,'injected issuance failure'); END`)
	require.NoError(t, err)
	principal, session, key, err := store.LoginWithMetadata(ctx, user.Login, "profile-password-233", 1, time.Hour, time.Hour, SessionMetadataInput{RemoteAddr: "192.0.2.10:1234", UserAgent: "Desktop"})
	require.Error(t, err)
	require.Equal(t, Principal{}, principal)
	require.Equal(t, Session{}, session)
	require.Empty(t, key)
	sessions, err := store.Sessions(ctx, user.ID)
	require.NoError(t, err)
	require.Empty(t, sessions)
	_, err = store.db.Exec(`DROP TRIGGER reject_session_metadata`)
	require.NoError(t, err)
	_, session, key, err = store.LoginWithMetadata(ctx, user.Login, "profile-password-233", 1, time.Hour, time.Hour, SessionMetadataInput{RemoteAddr: "192.0.2.10:1234", UserAgent: "Desktop"})
	require.NoError(t, err)
	require.NotEmpty(t, key)
	require.Equal(t, "Desktop", session.UserAgent)
}
